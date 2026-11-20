package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/OrdalieTech/orb/ai/auth"
	docport "github.com/OrdalieTech/orb/internal/document"
)

func TestSharedSyncsNamedAccountsActivatesAndProtectsLocal(t *testing.T) {
	ctx := context.Background()
	macBase := auth.NewMemoryStore(map[string]*auth.Credential{
		"openai-codex": auth.OAuthCredential("r-default", "a-default", 1000),
	})
	mac := NewStoreWithDocument(&memoryDocument{}, macBase)
	if err := mac.Rename(ctx, "openai-codex", DefaultID, "Personal"); err != nil {
		t.Fatal(err)
	}
	lea, err := mac.Add(ctx, "openai-codex", "lea", auth.OAuthCredential("r-lea", "a-lea", 2000))
	if err != nil {
		t.Fatal(err)
	}
	zen, err := mac.Add(ctx, "opencode", "opencode zen", auth.APIKeyCredential("zen-key"))
	if err != nil {
		t.Fatal(err)
	}
	claudeCred := &auth.Credential{Type: auth.CredentialOAuth}
	claudeCred.SetExtra("claudeConfigDir", json.RawMessage(`"/Users/baud/.orb/agent/plugins/claude-sessions/accounts/abc"`))
	claude, err := mac.Add(ctx, "claude-sessions", "Claude Work", claudeCred)
	if err != nil {
		t.Fatal(err)
	}

	shared, err := mac.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := shared[claude.ID]; ok {
		t.Fatalf("shared local claudeConfigDir account: %v", shared)
	}
	if _, ok := shared[lea.ID]; !ok {
		t.Fatalf("missing codex account lea: %v", shared)
	}
	if _, ok := shared[zen.ID]; !ok {
		t.Fatalf("missing opencode zen account: %v", shared)
	}
	if _, ok := shared[defaultNamePrefix+"openai-codex"]; !ok {
		t.Fatalf("missing default account display name: %v", shared)
	}

	// Browser tab receives the shared accounts; base has openai-codex default (from auth.json)
	// but no opencode entry, so opencode zen must become active automatically.
	tabBase := auth.NewMemoryStore(map[string]*auth.Credential{
		"openai-codex": auth.OAuthCredential("r-default", "a-default", 1000),
	})
	tab := NewStoreWithDocument(&memoryDocument{}, tabBase)
	changed, err := tab.MergeShared(ctx, []map[string]json.RawMessage{shared}, false, nil)
	if err != nil || len(changed) != 3 {
		t.Fatalf("tab merge: changed=%v err=%v", changed, err)
	}
	rows, err := tab.Accounts(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatalf("tab accounts = %#v, %v", rows, err)
	}
	byID := map[string]Account{}
	for _, r := range rows {
		byID[r.Provider+"/"+r.ID] = r
	}
	if byID["openai-codex/"+DefaultID].Name != "Personal" || !byID["openai-codex/"+DefaultID].Active {
		t.Fatalf("default codex account = %#v", byID["openai-codex/"+DefaultID])
	}
	if byID["openai-codex/"+lea.ID].Name != "lea" || byID["openai-codex/"+lea.ID].Active {
		t.Fatalf("lea account = %#v", byID["openai-codex/"+lea.ID])
	}
	if byID["opencode/"+zen.ID].Name != "opencode zen" || !byID["opencode/"+zen.ID].Active {
		t.Fatalf("opencode zen account not auto-activated: %#v", byID["opencode/"+zen.ID])
	}
	if cred, err := tab.Read(ctx, "opencode"); err != nil || cred == nil || cred.Key == nil || *cred.Key != "zen-key" {
		t.Fatalf("tab Read(opencode) = %#v, %v", cred, err)
	}

	// OAuth token refresh on the tab propagates back to the owner (keepOwn=true).
	if _, err := tab.View("openai-codex", lea.ID).Modify(ctx, "openai-codex", func(*auth.Credential) (*auth.Credential, error) {
		return auth.OAuthCredential("r-lea-2", "a-lea-2", 5000), nil
	}); err != nil {
		t.Fatal(err)
	}
	handin, err := tab.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := mac.MergeShared(ctx, []map[string]json.RawMessage{handin}, true, nil); err != nil || len(changed) != 1 || changed[0] != lea.ID {
		t.Fatalf("owner merge refreshed OAuth: changed=%v err=%v", changed, err)
	}
	if cred, err := mac.View("openai-codex", lea.ID).Read(ctx, "openai-codex"); err != nil || cred.Expires != 5000 {
		t.Fatalf("owner lea credential not refreshed: %#v, %v", cred, err)
	}

	// Removing opencode zen on the owner removes it on the tab and leaves accounts.json valid.
	if changed, err := tab.MergeShared(ctx, nil, false, []string{zen.ID}); err != nil || len(changed) != 1 {
		t.Fatalf("tab remove zen: changed=%v err=%v", changed, err)
	}
	if rows, err := tab.Accounts(ctx); err != nil || len(rows) != 2 {
		t.Fatalf("tab accounts after remove = %#v, %v", rows, err)
	}
	// A remote removal never drops a local-only claude account on the Mac.
	if changed, err := mac.MergeShared(ctx, nil, false, []string{claude.ID}); err != nil || len(changed) != 0 {
		t.Fatalf("mac removed local claude account: changed=%v err=%v", changed, err)
	}
}

// TestSharedMergeOrderIndependent proves syncing "accounts" before "credentials" and syncing
// "credentials" before "accounts" converge on the exact same active account: the default wins
// whenever auth.json has one, the first named account is active when auth.json has none, and an
// explicit user Select still persists across later merges.
func TestSharedMergeOrderIndependent(t *testing.T) {
	ctx := context.Background()
	ownerAuthDoc := &docport.Memory{}
	ownerAuth := auth.NewDocumentStore(ownerAuthDoc)
	_, _ = ownerAuth.Modify(ctx, "openai", func(*auth.Credential) (*auth.Credential, error) {
		return auth.APIKeyCredential("sk-default"), nil
	})
	ownerAccts := NewStoreWithDocument(&memoryDocument{}, ownerAuth)
	lea, err := ownerAccts.Add(ctx, "openai", "lea", auth.APIKeyCredential("sk-lea"))
	if err != nil {
		t.Fatal(err)
	}
	zen, err := ownerAccts.Add(ctx, "opencode", "opencode zen", auth.APIKeyCredential("sk-zen"))
	if err != nil {
		t.Fatal(err)
	}
	sharedCreds, err := ownerAuth.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sharedAccts, err := ownerAccts.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}

	run := func(accountsFirst bool) ([]Account, string, string) {
		credStore := auth.NewDocumentStore(&docport.Memory{})
		acctStore := NewStoreWithDocument(&memoryDocument{}, credStore)
		if accountsFirst {
			if _, err := acctStore.MergeShared(ctx, []map[string]json.RawMessage{sharedAccts}, false, nil); err != nil {
				t.Fatal(err)
			}
			// Before credentials arrive, lea is usable as the only openai account.
			if c, err := acctStore.Read(ctx, "openai"); err != nil || c == nil || *c.Key != "sk-lea" {
				t.Fatalf("before credentials arrived: openai = %#v, %v", c, err)
			}
			if _, err := credStore.MergeShared(ctx, []map[string]json.RawMessage{sharedCreds}, false, nil); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := credStore.MergeShared(ctx, []map[string]json.RawMessage{sharedCreds}, false, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := acctStore.MergeShared(ctx, []map[string]json.RawMessage{sharedAccts}, false, nil); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := acctStore.Accounts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		openAI, err := acctStore.Read(ctx, "openai")
		if err != nil || openAI == nil {
			t.Fatalf("openai Read = %#v, %v", openAI, err)
		}
		openCode, err := acctStore.Read(ctx, "opencode")
		if err != nil || openCode == nil {
			t.Fatalf("opencode Read = %#v, %v", openCode, err)
		}
		// Explicit user selection persists even when base credential exists.
		if err := acctStore.Select(ctx, "openai", lea.ID); err != nil {
			t.Fatal(err)
		}
		if c, err := acctStore.Read(ctx, "openai"); err != nil || c == nil || *c.Key != "sk-lea" {
			t.Fatalf("explicit Select(lea) did not persist: %#v, %v", c, err)
		}
		return rows, *openAI.Key, *openCode.Key
	}

	rowsA, openAIA, openCodeA := run(true)
	rowsB, openAIB, openCodeB := run(false)
	if openAIA != "sk-default" || openAIB != "sk-default" {
		t.Fatalf("openai active key differed by sync order: accountsFirst=%q credsFirst=%q", openAIA, openAIB)
	}
	if openCodeA != "sk-zen" || openCodeB != "sk-zen" {
		t.Fatalf("opencode active key differed by sync order: accountsFirst=%q credsFirst=%q", openCodeA, openCodeB)
	}
	if len(rowsA) != len(rowsB) || len(rowsA) != 3 {
		t.Fatalf("row counts differed: %#v vs %#v", rowsA, rowsB)
	}
	for i := range rowsA {
		if rowsA[i] != rowsB[i] {
			t.Fatalf("row %d differed by sync order: accountsFirst=%#v credsFirst=%#v", i, rowsA[i], rowsB[i])
		}
	}
	// Verify opencode zen is active and lea is inactive once default arrived.
	for _, r := range rowsA {
		if r.ID == DefaultID && r.Provider == "openai" && !r.Active {
			t.Fatalf("openai default not active: %#v", r)
		}
		if r.ID == lea.ID && r.Active {
			t.Fatalf("openai lea unexpectedly active: %#v", r)
		}
		if r.ID == zen.ID && !r.Active {
			t.Fatalf("opencode zen not active: %#v", r)
		}
	}
}

type errCredentialStore struct{ auth.CredentialStore }

func (errCredentialStore) Read(context.Context, string) (*auth.Credential, error) {
	return nil, errors.New("boom")
}

// A transient error reading a base credential must fail Shareable rather than dropping the
// default account's display name (which an owner would read as a deletion).
func TestShareableReturnsErrorOnBaseReadFailure(t *testing.T) {
	ctx := context.Background()
	store := NewStoreWithDocument(&memoryDocument{}, errCredentialStore{})
	if err := store.Rename(ctx, "openai", DefaultID, "Personal"); err != nil {
		t.Fatal(err)
	}
	if shared, err := store.Shareable(ctx); err == nil {
		t.Fatalf("Shareable succeeded despite base read error: %v", shared)
	}
}
