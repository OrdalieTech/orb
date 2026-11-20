package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// Shareable returns the credentials a device may share with its Mitosis sync group, each as its
// auth.json value: API keys and OAuth logins, never one whose key or environment is a shell
// command ("!…"), which would run on every device that received it.
func (store *Store) Shareable(ctx context.Context) (map[string]json.RawMessage, error) {
	stored, err := store.load(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for provider, credential := range stored.byProvider {
		if ShareableCredential(credential) {
			if out[provider], err = json.Marshal(credential); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// Removed returns credentials explicitly deleted locally (via Delete/logout), each mapped to the
// digest of the last shareable value it held before deletion. No credential value or secret is
// stored in the tombstone document; a wiped device loses both auth.json and auth-removed.json.
func (store *Store) Removed(ctx context.Context) (map[string]string, error) {
	if store.removed == nil {
		return nil, nil
	}
	stored, err := store.load(ctx)
	if err != nil {
		return nil, err
	}
	remData, err := store.removed.Read(ctx)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(remData)) == 0 {
		return nil, nil
	}
	var all map[string]string
	if err := json.Unmarshal(remData, &all); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for provider, digest := range all {
		if _, live := stored.byProvider[provider]; live || digest == "" {
			continue
		}
		out[provider] = digest
	}
	return out, nil
}

func (store *Store) recordRemoved(ctx context.Context, provider, digest string) error {
	if store.removed == nil {
		return nil
	}
	return store.removed.Update(ctx, func(current []byte) ([]byte, error) {
		all := map[string]string{}
		if len(bytes.TrimSpace(current)) > 0 {
			if err := json.Unmarshal(current, &all); err != nil {
				return nil, err
			}
		}
		all[provider] = digest
		return json.Marshal(all)
	})
}

func (store *Store) clearRemoved(ctx context.Context, providers []string) error {
	if store.removed == nil || len(providers) == 0 {
		return nil
	}
	current, err := store.removed.Read(ctx)
	if err != nil || len(bytes.TrimSpace(current)) == 0 {
		return err
	}
	var peek map[string]string
	if json.Unmarshal(current, &peek) != nil {
		return nil
	}
	need := false
	for _, p := range providers {
		if _, ok := peek[p]; ok {
			need = true
			break
		}
	}
	if !need {
		return nil
	}
	return store.removed.Update(ctx, func(data []byte) ([]byte, error) {
		all := map[string]string{}
		if len(bytes.TrimSpace(data)) > 0 && json.Unmarshal(data, &all) != nil {
			return data, nil
		}
		changed := false
		for _, p := range providers {
			if _, ok := all[p]; ok {
				delete(all, p)
				changed = true
			}
		}
		if !changed {
			return data, nil
		}
		if len(all) == 0 {
			return nil, nil
		}
		return json.Marshal(all)
	})
}

// MergeShared folds in credentials a sync group shared and returns the providers it changed.
// keepOwn (an owner folding hand-ins) takes only providers it lacks; otherwise it takes the group's
// over its own and drops removed, the providers the group no longer shares. Between two OAuth logins the one
// that expires later always wins, so a refreshed token replaces the copies its refresh made stale.
// Invalid or unshareable entries are ignored, and auth.json is written only when something changed.
func (store *Store) MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error) {
	var changed []string
	var clearList []string
	merge := func(stored *credentials) (bool, error) {
		changed = nil
		clearList = nil
		for _, entries := range incoming {
			for provider, raw := range entries {
				var next Credential
				if json.Unmarshal(raw, &next) != nil || !ShareableCredential(&next) {
					continue
				}
				clearList = append(clearList, provider)
				have, ok := stored.byProvider[provider]
				if ok {
					same, _ := json.Marshal(have)
					oauth := have.Type == CredentialOAuth && next.Type == CredentialOAuth
					if bytes.Equal(same, compactJSON(raw)) || oauth && next.Expires < have.Expires || keepOwn && (!oauth || next.Expires <= have.Expires) {
						continue
					}
				} else {
					stored.order = append(stored.order, provider)
				}
				stored.byProvider[provider] = &next
				changed = append(changed, provider)
			}
		}
		for _, provider := range removed {
			if !keepOwn {
				clearList = append(clearList, provider)
			}
			if have, ok := stored.byProvider[provider]; ok && !keepOwn && ShareableCredential(have) {
				delete(stored.byProvider, provider)
				stored.order = slices.DeleteFunc(stored.order, func(p string) bool { return p == provider })
				changed = append(changed, provider)
			}
		}
		return len(changed) > 0, nil
	}
	stored, err := store.load(ctx)
	if err == nil {
		_, err = merge(&stored)
	}
	if err != nil {
		return nil, err
	}
	if len(changed) > 0 {
		if err := store.update(ctx, merge); err != nil {
			return nil, err
		}
	}
	if err := store.clearRemoved(ctx, clearList); err != nil {
		return changed, err
	}
	return changed, nil
}

// ShareableCredential reports whether credential may travel across a Mitosis sync group:
// API keys and OAuth logins with no shell-command ("!…") values and no local CLI config directory.
func ShareableCredential(credential *Credential) bool {
	if credential == nil || (credential.Type != CredentialAPIKey && credential.Type != CredentialOAuth) {
		return false
	}
	if credential.Key != nil && strings.HasPrefix(*credential.Key, "!") {
		return false
	}
	for _, value := range credential.Env {
		if strings.HasPrefix(value, "!") {
			return false
		}
	}
	return len(credential.Extra["claudeConfigDir"]) == 0
}

func compactJSON(raw []byte) []byte {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return raw
	}
	return out.Bytes()
}
