package accounts

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/orb/ai/auth"
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
