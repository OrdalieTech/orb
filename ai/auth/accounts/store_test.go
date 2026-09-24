package accounts

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/ai/auth"
)

// memoryDocument serializes updates like a host Store document.
type memoryDocument struct {
	mu   sync.Mutex
	data []byte
}

func (d *memoryDocument) Read(context.Context) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.data, nil
}

func (d *memoryDocument) Update(_ context.Context, change func([]byte) ([]byte, error)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	next, err := change(d.data)
	if err == nil {
		d.data = next
	}
	return err
}

func TestOAuthIdentityKeepsAccountsSeparateAndRefreshesExisting(t *testing.T) {
	token := func(account, plan string) string {
		return "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user","https://api.openai.com/auth":{"chatgpt_account_id":"`+account+`","chatgpt_plan_type":"`+plan+`"}}`)) + ".signature"
	}
	store := NewStoreWithDocument(&memoryDocument{}, nil)
	first, err := store.Add(t.Context(), "openai-codex", "Work", auth.OAuthCredential("first-refresh", token("work", "plus"), 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Add(t.Context(), "openai-codex", "Personal", auth.OAuthCredential("second-refresh", token("personal", "plus"), 0))
	if err != nil || first.ID == second.ID {
		t.Fatal("merged distinct subscription accounts", err)
	}
	renewed, err := store.Add(t.Context(), "openai-codex", "Work", auth.OAuthCredential("renewed-refresh", token("work", "pro"), 0))
	if err != nil || renewed.ID != first.ID {
		t.Fatal("duplicated a reconnected account", err)
	}
	rows, err := store.Accounts(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatal("unexpected account count", err)
	}
}
