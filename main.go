package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/generative-ai-go/genai"
	"github.com/joho/godotenv"
	"google.golang.org/api/option"
)

// Config holds runtime configuration loaded from environment variables.
type Config struct {
	TelegramBotToken string
	GeminiAPIKey     string
	BotUsername      string // e.g. "@MyBanterBot" or "MyBanterBot"
	Port             string
}

// Telegram API Types
type TelegramUpdate struct {
	UpdateID int              `json:"update_id"`
	Message  *TelegramMessage `json:"message"`
}

type TelegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type TelegramChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"` // "private", "group", "supergroup", "channel"
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

type TelegramQuote struct {
	Text string `json:"text"`
}

type TelegramMessage struct {
	MessageID      int64            `json:"message_id"`
	From           *TelegramUser    `json:"from"`
	Chat           TelegramChat     `json:"chat"`
	Text           string           `json:"text"`
	ReplyToMessage *TelegramMessage `json:"reply_to_message,omitempty"`
	Quote          *TelegramQuote   `json:"quote,omitempty"`
}

// SendMessageRequest payload for Telegram sendMessage API
type SendMessageRequest struct {
	ChatID                int64  `json:"chat_id"`
	Text                  string `json:"text"`
	ReplyToMessageID      int64  `json:"reply_to_message_id,omitempty"`
	AllowSendingWithoutReply bool   `json:"allow_sending_without_reply,omitempty"`
}

// SendChatActionRequest payload for Telegram sendChatAction API
type SendChatActionRequest struct {
	ChatID int64  `json:"chat_id"`
	Action string `json:"action"`
}

type BotServer struct {
	cfg          Config
	genaiClient  *genai.Client
	httpClient   *http.Client
	cleanBotName string // normalized bot handle without '@'
}

func main() {
	// Attempt to load .env file if it exists locally (ignores error in production/containers)
	_ = godotenv.Load()

	cfg := Config{
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		GeminiAPIKey:     os.Getenv("GEMINI_API_KEY"),
		BotUsername:      os.Getenv("BOT_USERNAME"),
		Port:             os.Getenv("PORT"),
	}

	if cfg.Port == "" {
		cfg.Port = "8080"
	}

	if cfg.TelegramBotToken == "" {
		log.Fatal("ERROR: TELEGRAM_BOT_TOKEN environment variable is required")
	}
	if cfg.GeminiAPIKey == "" {
		log.Fatal("ERROR: GEMINI_API_KEY environment variable is required")
	}

	cleanBotName := strings.TrimPrefix(cfg.BotUsername, "@")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	genaiClient, err := genai.NewClient(ctx, option.WithAPIKey(cfg.GeminiAPIKey))
	if err != nil {
		log.Fatalf("Failed to initialize Gemini client: %v", err)
	}
	defer genaiClient.Close()

	server := &BotServer{
		cfg:          cfg,
		genaiClient:  genaiClient,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		cleanBotName: strings.ToLower(cleanBotName),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", server.handleWebhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	httpServer := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("Server listening on port %s (bot: @%s)...", cfg.Port, cleanBotName)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server gracefully...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server forced shutdown: %v", err)
	}
	log.Println("Server exited cleanly.")
}

func (s *BotServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Enforce 1MB payload limit to prevent unbounded memory consumption
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var update TelegramUpdate
	if err := json.Unmarshal(body, &update); err != nil {
		// Respond 200 even for malformed json so Telegram doesn't storm retries
		w.WriteHeader(http.StatusOK)
		return
	}

	// Always acknowledge immediately to Telegram
	w.WriteHeader(http.StatusOK)

	// Process message if present
	if update.Message == nil || strings.TrimSpace(update.Message.Text) == "" {
		return
	}

	msg := update.Message

	// Verify trigger condition
	shouldRespond, userPrompt := s.evaluateTrigger(msg)
	if !shouldRespond {
		return
	}

	// Spawn asynchronous worker with a timeout context for typing indicator & Gemini LLM call
	go func(msg *TelegramMessage, prompt string) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		// Send typing action immediately for responsive UX
		s.sendChatAction(ctx, msg.Chat.ID, "typing")

		// Extract any referenced quoted text or replied message text
		quotedText := ""
		if msg.Quote != nil && strings.TrimSpace(msg.Quote.Text) != "" {
			quotedText = strings.TrimSpace(msg.Quote.Text)
		} else if msg.ReplyToMessage != nil && strings.TrimSpace(msg.ReplyToMessage.Text) != "" {
			quotedText = strings.TrimSpace(msg.ReplyToMessage.Text)
		}

		replyText, err := s.generateGeminiResponse(ctx, quotedText, prompt)
		if err != nil {
			log.Printf("Gemini generation failed: %v", err)
			return
		}

		if strings.TrimSpace(replyText) == "" {
			return
		}

		if err := s.sendTelegramReply(ctx, msg.Chat.ID, msg.MessageID, replyText); err != nil {
			log.Printf("Failed to send Telegram message: %v", err)
		}
	}(msg, userPrompt)
}

// evaluateTrigger inspects whether the bot should respond and strips mentions if necessary.
func (s *BotServer) evaluateTrigger(msg *TelegramMessage) (bool, string) {
	text := strings.TrimSpace(msg.Text)
	isPrivate := msg.Chat.Type == "private"

	// Check if the user is replying to the bot
	isReplyingToBot := false
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		if msg.ReplyToMessage.From.IsBot {
			replyUsername := strings.ToLower(msg.ReplyToMessage.From.Username)
			if s.cleanBotName != "" && replyUsername == s.cleanBotName {
				isReplyingToBot = true
			} else if s.cleanBotName == "" {
				// If BOT_USERNAME was not configured, accept reply to any bot
				isReplyingToBot = true
			}
		}
	}

	// Check if bot is mentioned via @username (case-insensitive)
	isMentioned := false
	if s.cleanBotName != "" {
		mention := "@" + s.cleanBotName
		if strings.Contains(strings.ToLower(text), mention) {
			isMentioned = true
		}
	}

	// Trigger condition:
	// 1. Private 1-on-1 chat
	// 2. Mentioned directly via @bot_username
	// 3. User replied directly to a prior message from this bot
	if !isPrivate && !isMentioned && !isReplyingToBot {
		return false, ""
	}

	// Strip the bot mention from the prompt
	cleanedPrompt := text
	if s.cleanBotName != "" {
		// Case-insensitive removal of @bot_username
		mention := "@" + s.cleanBotName
		lowerPrompt := strings.ToLower(cleanedPrompt)
		for {
			idx := strings.Index(lowerPrompt, mention)
			if idx == -1 {
				break
			}
			cleanedPrompt = cleanedPrompt[:idx] + cleanedPrompt[idx+len(mention):]
			lowerPrompt = strings.ToLower(cleanedPrompt)
		}
		cleanedPrompt = strings.TrimSpace(cleanedPrompt)
	}

	// In groups, if mentioned without any text but quoting someone, prompt can be empty
	return true, cleanedPrompt
}

// generateGeminiResponse calls gemini-2.5-flash with customized system instruction and temperature.
func (s *BotServer) generateGeminiResponse(ctx context.Context, quotedText, userPrompt string) (string, error) {
	model := s.genaiClient.GenerativeModel("gemini-2.5-flash")

	// Set temperature (0.85 - 1.0)
	temp := float32(0.9)
	model.Temperature = &temp

	// Configure system instruction
	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{
			genai.Text("You are a witty, hilariously sarcastic, and perceptive chat companion in a Telegram group with friends. " +
				"Give concise, razor-sharp comebacks, banter, and genuine answers when asked seriously. " +
				"Never sound corporate, preachy, or like a generic robotic assistant. " +
				"Keep replies under 2-4 sentences unless explicitly asked for a long breakdown."),
		},
	}

	// Dynamic prompt assembly
	var promptContent string
	if quotedText != "" && userPrompt != "" {
		promptContent = fmt.Sprintf("[Referenced Message]: \"%s\"\n[User Comment/Command]: \"%s\"", quotedText, userPrompt)
	} else if quotedText != "" && userPrompt == "" {
		promptContent = fmt.Sprintf("[Referenced Message]: \"%s\"\n[User Comment/Command]: \"What do you think about this?\"", quotedText)
	} else {
		promptContent = fmt.Sprintf("[User Command]: \"%s\"", userPrompt)
	}

	resp, err := model.GenerateContent(ctx, genai.Text(promptContent))
	if err != nil {
		return "", err
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return "", fmt.Errorf("no response candidates received from Gemini")
	}

	var sb strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if textPart, ok := part.(genai.Text); ok {
			sb.WriteString(string(textPart))
		}
	}

	return strings.TrimSpace(sb.String()), nil
}

// sendChatAction sends typing indicator to Telegram chat
func (s *BotServer) sendChatAction(ctx context.Context, chatID int64, action string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendChatAction", s.cfg.TelegramBotToken)
	reqBody := SendChatActionRequest{
		ChatID: chatID,
		Action: action,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}

// sendTelegramReply posts the generated answer back as a reply to the triggering message
func (s *BotServer) sendTelegramReply(ctx context.Context, chatID, replyToMsgID int64, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", s.cfg.TelegramBotToken)
	reqBody := SendMessageRequest{
		ChatID:                chatID,
		Text:                  text,
		ReplyToMessageID:      replyToMsgID,
		AllowSendingWithoutReply: true,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
