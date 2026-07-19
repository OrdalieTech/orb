// Command localbot runs a minimal Telegram chat gateway: long-poll ingress,
// the durable local spool, and per-conversation agent sessions under a local
// data directory. Sessions are created with all tools disabled (the
// LocalProvider default); wire tools explicitly via chat.WithSessionOptions
// if you need them.
//
// Environment:
//
//	TELEGRAM_BOT_TOKEN       bot token from @BotFather (required)
//	TELEGRAM_ALLOWED_SENDERS comma-separated Telegram user ids allowed to
//	                         drive the bot (optional; empty = allow all)
//	LOCALBOT_DATA_DIR        spool + session root (default ./localbot-data)
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
	"github.com/OrdalieTech/pi-go/chat/telegram"
)

func main() {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("localbot: TELEGRAM_BOT_TOKEN is required")
	}
	dataDir := os.Getenv("LOCALBOT_DATA_DIR")
	if dataDir == "" {
		dataDir = "localbot-data"
	}

	provider, err := chat.NewLocalProvider(filepath.Join(dataDir, "sessions"))
	if err != nil {
		log.Fatalf("localbot: %v", err)
	}
	adapter, err := telegram.New(telegram.Options{Token: token})
	if err != nil {
		log.Fatalf("localbot: %v", err)
	}
	processor, err := chat.New(chat.Options{
		Sessions:  provider,
		Adapters:  []chat.Adapter{adapter},
		Authorize: authorizer(os.Getenv("TELEGRAM_ALLOWED_SENDERS")),
	})
	if err != nil {
		log.Fatalf("localbot: %v", err)
	}
	local, err := chat.NewLocal(processor, filepath.Join(dataDir, "spool.jsonl"))
	if err != nil {
		log.Fatalf("localbot: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Println("localbot: polling (ctrl-c to stop)")
	pollErr := adapter.Poll(ctx, local.Publish)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := local.Close(shutdownCtx); err != nil {
		log.Printf("localbot: close spool: %v", err)
	}
	if err := processor.Close(shutdownCtx); err != nil {
		log.Printf("localbot: close processor: %v", err)
	}
	if pollErr != nil && ctx.Err() == nil {
		log.Fatalf("localbot: poll: %v", pollErr)
	}
}

// authorizer builds the sender gate from a comma-separated user id list.
func authorizer(allowed string) func(chat.Message) error {
	var ids []string
	for _, id := range strings.Split(allowed, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		// Explicit allow-all fallback: without TELEGRAM_ALLOWED_SENDERS,
		// EVERY Telegram user who can reach the bot may drive your agent
		// session (and spend your tokens). Set the allowlist in production.
		return chat.AllowAll
	}
	return func(m chat.Message) error {
		if slices.Contains(ids, m.SenderID) {
			return nil
		}
		return fmt.Errorf("sender %s is not in TELEGRAM_ALLOWED_SENDERS", m.SenderID)
	}
}
