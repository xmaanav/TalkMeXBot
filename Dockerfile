# Multi-stage Dockerfile for ultra-lightweight, secure serverless deployment
# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install CA certificates for secure HTTPS calls to Telegram & Gemini APIs
RUN apk update && apk add --no-cache ca-certificates git

# Cache Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build static binary without CGO, stripped of debug info and symbol tables
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o bot .

# Final minimal scratch stage
FROM scratch

# Import CA certificates from builder stage
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy compiled binary
COPY --from=builder /app/bot /bot

# Expose port (default 8080)
EXPOSE 8080

ENTRYPOINT ["/bot"]
