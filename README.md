# Production Serverless Telegram AI Assistant in Go

A lightweight, zero-persistence Telegram bot powered by Google Gemini (`gemini-2.5-flash`). Built for 1-on-1 direct messages and group chats (with direct mentions, quotes, and reply-chain tracking).

---

## Features
- **Model**: `gemini-2.5-flash` with custom personality system instructions & dynamic prompt formatting.
- **Fast Webhook Execution**: Immediate `200 OK` handshake to Telegram to avoid retries, paired with background asynchronous LLM generation.
- **Visual Feedback**: Triggers Telegram `typing` chat action immediately upon receipt of valid triggers.
- **Zero-Persistence Privacy**: No database, no local logs of messages, purely in-memory lifecycle.
- **Group & DM Intelligent Triggers**:
  - Private chats: Replies to all messages.
  - Group chats: Responds when tagged `@BotUsername` OR when users reply directly to one of the bot's messages.
  - Quoted text handling: Extracts Telegram quotes and `reply_to_message` content to provide context-aware comebacks.

---

## Environment Variables

| Variable | Description | Example |
| :--- | :--- | :--- |
| `TELEGRAM_BOT_TOKEN` | Token provided by `@BotFather` | `123456789:ABCdefGhIJKlmNoPQRsTUVwxyZ` |
| `GEMINI_API_KEY` | Google AI Studio Gemini API Key | `AIzaSy...` |
| `BOT_USERNAME` | Handle of the bot (with or without `@`) | `@MyBanterBot` |
| `PORT` | Listening HTTP port (defaults to `8080`) | `8080` |

---

## BotFather Setup Guide

1. Open Telegram and search for [@BotFather](https://t.me/BotFather).
2. Create your bot with `/newbot` and save your token.
3. **Important for Group Quotes & Banter:**
   - Send `/setprivacy` to `@BotFather`.
   - Select your bot.
   - Choose **Disable** (`Privacy mode is disabled`). This enables the bot to see quoted messages and replies seamlessly in group chats.
4. Optional: Set bot description and profile pic via `/setdescription` and `/setuserpic`.

---

## Local Development & Testing

### 1. Build and Run Locally
```bash
# Set your environment variables in PowerShell
$env:TELEGRAM_BOT_TOKEN="your_telegram_bot_token"
$env:GEMINI_API_KEY="your_gemini_api_key"
$env:BOT_USERNAME="@YourBotUsername"
$env:PORT="8080"

# Run the bot
go run main.go
```

### 2. Expose Local Server via ngrok
```bash
ngrok http 8080
```
Copy the generated HTTPS forwarding URL (e.g., `https://abc-123.ngrok-free.app`).

### 3. Register Webhook with Telegram
```bash
curl -F "url=https://abc-123.ngrok-free.app/webhook" https://api.telegram.org/bot<YOUR_TELEGRAM_BOT_TOKEN>/setWebhook
```

To verify webhook status:
```bash
curl https://api.telegram.org/bot<YOUR_TELEGRAM_BOT_TOKEN>/getWebhookInfo
```

---

## Production Deployment

### Option A: Google Cloud Run (Serverless)

Build and deploy with Google Cloud Build / Cloud Run:

```bash
gcloud run deploy telegram-ai-bot \
  --source . \
  --platform managed \
  --region us-central1 \
  --allow-unauthenticated \
  --set-env-vars TELEGRAM_BOT_TOKEN="<TOKEN>",GEMINI_API_KEY="<KEY>",BOT_USERNAME="@YourBotUsername",PORT="8080"
```

Once deployed, set the webhook to the Cloud Run URL:
```bash
curl -F "url=https://<CLOUD-RUN-URL>/webhook" https://api.telegram.org/bot<TOKEN>/setWebhook
```

### Option B: Docker Container
```bash
docker build -t telegram-gemini-bot .
docker run -p 8080:8080 \
  -e TELEGRAM_BOT_TOKEN="<TOKEN>" \
  -e GEMINI_API_KEY="<KEY>" \
  -e BOT_USERNAME="@YourBotUsername" \
  telegram-gemini-bot
```
