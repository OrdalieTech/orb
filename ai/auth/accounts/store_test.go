package accounts

import (
	"context"
	"encoding/base64"
	"strings"
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

// A credential held by another program (a CLI's own sign-in) has no token to
// read a name from; it names itself, and deselecting it returns the provider to
// its default source even when Orb stores no default.
func TestExternalAccountNamesItselfAndDeselects(t *testing.T) {
	ctx := context.Background()
	store := NewStoreWithDocument(&memoryDocument{}, nil)
	credential := &auth.Credential{Type: auth.CredentialOAuth}
	credential.SetExtra("label", []byte(`"second@example.com · Max"`))
	added, err := store.Add(ctx, "external", "", credential)
	if err != nil || added.Name != "second@example.com · Max" || !added.Active {
		t.Fatalf("added = %#v, %v", added, err)
	}
	if err := store.Deselect(ctx, "external"); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Accounts(ctx)
	if err != nil || len(rows) != 1 || rows[0].Active {
		t.Fatalf("after deselect = %#v, %v", rows, err)
	}
	if current, err := store.Read(ctx, "external"); err != nil || current != nil {
		t.Fatalf("deselected provider still reads %#v, %v", current, err)
	}
}

func TestRenameEveryAccountAndClearName(t *testing.T) {
	ctx := t.Context()
	base := auth.NewMemoryStore(map[string]*auth.Credential{"provider": auth.APIKeyCredential("default-key")})
	doc := &memoryDocument{data: []byte(`{"version":1,"accounts":[],"active":{},"names":{"provider":"Existing default"}}`)}
	store := NewStoreWithDocument(doc, base)
	added, err := store.Add(ctx, "provider", "Work", auth.APIKeyCredential("work-key"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{DefaultID, added.ID, "ambient", "runtime", "external-source"} {
		t.Run(id, func(t *testing.T) {
			if err := store.Rename(ctx, "provider", id, "  Renamed  "); err != nil {
				t.Fatal(err)
			}
			if err := store.Rename(ctx, "provider", id, strings.Repeat("x", 129)); err == nil {
				t.Fatal("accepted an oversized name")
			}
			if err := store.Rename(ctx, "provider", id, " \t "); err != nil {
				t.Fatalf("clear name: %v", err)
			}
		})
	}
	rows, err := NewStoreWithDocument(doc, base).Accounts(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("accounts = %v, %v", rows, err)
	}
	for _, row := range rows {
		if row.Name != "Default" {
			t.Fatalf("cleared name = %q", row.Name)
		}
	}
	credential, err := store.Read(ctx, "provider")
	if err != nil || credential == nil || credential.Key == nil || *credential.Key != "work-key" {
		t.Fatal("renaming changed the active credential", err)
	}
}

func TestSynthesizedNamesSurviveReopeningAndStayScopedToIdentity(t *testing.T) {
	doc := &memoryDocument{data: []byte(`{"version":1,"accounts":[],"active":{},"names":{"provider":"Existing default"}}`)}
	store := NewStoreWithDocument(doc, nil)
	for _, item := range []struct{ provider, id, name string }{
		{"provider", "ambient", "  Personal ☀  "},
		{"provider", "runtime", "Command line"},
		{"provider/source", "ambient", "Other provider"},
		{"provider", "source/ambient", "Other identity"},
	} {
		if err := store.Rename(t.Context(), item.provider, item.id, item.name); err != nil {
			t.Fatal(err)
		}
	}
	rows := []Account{
		{Provider: "provider", ID: DefaultID, Name: "Default"},
		{Provider: "provider", ID: "ambient", Name: "Original ambient", Active: true},
		{Provider: "provider", ID: "runtime", Name: "Original runtime"},
		{Provider: "provider/source", ID: "ambient"},
		{Provider: "provider", ID: "source/ambient"},
		{Provider: "unrelated", ID: "ambient", Name: "Unrelated"},
	}
	if err := NewStoreWithDocument(doc, nil).ApplyNames(rows); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"Existing default", "Personal ☀", "Command line", "Other provider", "Other identity", "Unrelated"} {
		if rows[i].Name != want {
			t.Errorf("row %d name = %q, want %q", i, rows[i].Name, want)
		}
	}
	if !rows[1].Active {
		t.Fatal("applying names changed selection")
	}
	if err := store.Rename(t.Context(), "provider", "ambient", ""); err != nil {
		t.Fatal(err)
	}
	rows[1].Name = "Updated ambient label"
	if err := NewStoreWithDocument(doc, nil).ApplyNames(rows); err != nil {
		t.Fatal(err)
	}
	if rows[1].Name != "Updated ambient label" {
		t.Fatal("clearing retained the old override")
	}
}
