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
		if shareable(credential) {
			if out[provider], err = json.Marshal(credential); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// MergeShared folds in credentials a sync group shared and returns the providers it changed.
// keepOwn (an owner folding hand-ins) takes only providers it lacks; otherwise it takes the group's
// over its own and drops removed, the providers the group no longer shares. Between two OAuth logins the one
// that expires later always wins, so a refreshed token replaces the copies its refresh made stale.
// Invalid or unshareable entries are ignored, and auth.json is written only when something changed.
func (store *Store) MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error) {
	var changed []string
	merge := func(stored *credentials) (bool, error) {
		changed = nil
		for _, entries := range incoming {
			for provider, raw := range entries {
				var next Credential
				if json.Unmarshal(raw, &next) != nil || !shareable(&next) {
					continue
				}
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
			if _, ok := stored.byProvider[provider]; ok && !keepOwn {
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
	if err != nil || len(changed) == 0 {
		return nil, err
	}
	return changed, store.update(ctx, merge)
}

func shareable(credential *Credential) bool {
	if credential.Type != CredentialAPIKey && credential.Type != CredentialOAuth {
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
	return true
}

func compactJSON(raw []byte) []byte {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return raw
	}
	return out.Bytes()
}
