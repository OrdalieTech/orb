package telegram

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestClientHonors429RetryAfter(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	var slept []time.Duration
	adapter.client.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	f.stub("sendMessage", errorBody(429, "Too Many Requests: retry after 3", 3))

	message, err := adapter.client.sendMessage(context.Background(), sendMessageParams{ChatID: 7, Text: "hi"})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if message.MessageID == 0 {
		t.Fatal("expected a message id after the retry")
	}
	if len(f.callsTo("sendMessage")) != 2 {
		t.Fatalf("expected 2 sendMessage calls, got %d", len(f.callsTo("sendMessage")))
	}
	if len(slept) != 1 || slept[0] != 3*time.Second {
		t.Fatalf("expected one 3s flood-control sleep, got %v", slept)
	}
}

func TestClientTreatsNotModifiedAsSuccess(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	f.stub("editMessageText", errorBody(400,
		"Bad Request: message is not modified: specified new message content and reply markup are exactly the same", 0))

	err := adapter.client.editMessageText(context.Background(), editMessageParams{ChatID: 7, MessageID: 5, Text: "same"})
	if err != nil {
		t.Fatalf("expected not-modified to be success, got %v", err)
	}
}

func TestClientRedactsTokenFromTransportErrors(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	f.server.Close() // force a transport error whose URL embeds the token

	_, err := adapter.client.sendMessage(context.Background(), sendMessageParams{ChatID: 7, Text: "hi"})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error leaks the bot token: %v", err)
	}
}
