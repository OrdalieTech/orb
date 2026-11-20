package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/ai/auth"
)

const defaultNamePrefix = "name:"

type sharedRecord struct {
	Provider   string           `json:"provider"`
	Name       string           `json:"name"`
	Credential *auth.Credential `json:"credential"`
}

type sharedName struct {
	Name string `json:"name"`
}

// Shareable returns the named accounts and default-account display names a device may share with
// its Mitosis sync group. Local-only credentials (shell commands, Claude Code config directories)
// stay on their device.
func (s *Store) Shareable(ctx context.Context) (map[string]json.RawMessage, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, a := range d.Accounts {
		if !auth.ShareableCredential(a.Credential) {
			continue
		}
		raw, err := json.Marshal(sharedRecord{Provider: a.Provider, Name: a.Name, Credential: a.Credential})
		if err != nil {
			return nil, err
		}
		out[a.ID] = raw
	}
	for key, name := range d.Names {
		if name == "" || strings.ContainsRune(key, '\x00') {
			continue
		}
		base, err := s.base.Read(ctx, key)
		if err != nil {
			return nil, err
		}
		if !auth.ShareableCredential(base) {
			continue
		}
		raw, err := json.Marshal(sharedName{Name: name})
		if err != nil {
			return nil, err
		}
		out[defaultNamePrefix+key] = raw
	}
	return out, nil
}

// Removed returns shareable accounts and default-account display names explicitly removed on this
// device (via Remove or Rename("")), each with its last shared JSON value. Tombstones live inside
// accounts.json so wiping the store drops them too.
func (s *Store) Removed(_ context.Context) (map[string]json.RawMessage, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	if len(d.Removed) == 0 {
		return nil, nil
	}
	live := make(map[string]bool, len(d.Accounts)+len(d.Names))
	for _, a := range d.Accounts {
		live[a.ID] = true
	}
	for k, name := range d.Names {
		if name != "" && !strings.ContainsRune(k, '\x00') {
			live[defaultNamePrefix+k] = true
		}
	}
	out := map[string]json.RawMessage{}
	for key, raw := range d.Removed {
		if live[key] || len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var compact bytes.Buffer
		if json.Compact(&compact, raw) == nil {
			out[key] = compact.Bytes()
		}
	}
	return out, nil
}

// MergeShared folds in named accounts and display names a sync group shared and returns the keys
// it changed. Local-only credentials (such as Claude Code config directories) are never accepted,
// overwritten or dropped. Selection stays local: no automatic Active entry is persisted on merge,
// so receiving accounts and credentials in either order yields the same active account.
func (s *Store) MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error) {
	var changed []string
	wrote := false
	merge := func(d *document) error {
		changed = nil
		wrote = false
		dropTombstone := func(key string) {
			if _, ok := d.Removed[key]; ok {
				delete(d.Removed, key)
				wrote = true
			}
		}
		for _, entries := range incoming {
			for _, key := range slices.Sorted(maps.Keys(entries)) {
				raw := entries[key]
				if provider, ok := strings.CutPrefix(key, defaultNamePrefix); ok {
					if provider == "" || strings.ContainsRune(provider, '\x00') {
						continue
					}
					var next sharedName
					if json.Unmarshal(raw, &next) != nil {
						continue
					}
					name := strings.TrimSpace(next.Name)
					if name == "" || len(name) > 128 {
						continue
					}
					dropTombstone(key)
					if d.Names[provider] == name || (keepOwn && d.Names[provider] != "") {
						continue
					}
					if d.Names == nil {
						d.Names = map[string]string{}
					}
					d.Names[provider] = name
					changed = append(changed, key)
					wrote = true
					continue
				}
				if key == "" || key == DefaultID || strings.ContainsRune(key, '\x00') {
					continue
				}
				var next sharedRecord
				if json.Unmarshal(raw, &next) != nil || next.Provider == "" || strings.ContainsRune(next.Provider, '\x00') || !auth.ShareableCredential(next.Credential) {
					continue
				}
				name := strings.TrimSpace(next.Name)
				if name == "" {
					name = credentialName(next.Credential)
				}
				if len(name) > 128 {
					continue
				}
				dropTombstone(key)
				i := slices.IndexFunc(d.Accounts, func(a record) bool { return a.ID == key })
				if i >= 0 {
					have := d.Accounts[i]
					if !auth.ShareableCredential(have.Credential) {
						continue
					}
					haveJSON, _ := json.Marshal(sharedRecord{Provider: have.Provider, Name: have.Name, Credential: have.Credential})
					nextJSON, _ := json.Marshal(sharedRecord{Provider: next.Provider, Name: name, Credential: next.Credential})
					oauth := have.Credential.Type == auth.CredentialOAuth && next.Credential.Type == auth.CredentialOAuth
					if bytes.Equal(haveJSON, nextJSON) || oauth && next.Credential.Expires < have.Credential.Expires || keepOwn && (!oauth || next.Credential.Expires <= have.Credential.Expires) {
						continue
					}
					d.Accounts[i] = record{ID: key, Provider: next.Provider, Name: name, Credential: next.Credential.Clone()}
					changed = append(changed, key)
					wrote = true
					continue
				}
				d.Accounts = append(d.Accounts, record{ID: key, Provider: next.Provider, Name: name, Credential: next.Credential.Clone()})
				changed = append(changed, key)
				wrote = true
			}
		}
		if !keepOwn {
			for _, key := range removed {
				dropTombstone(key)
				if provider, ok := strings.CutPrefix(key, defaultNamePrefix); ok {
					if d.Names[provider] != "" {
						delete(d.Names, provider)
						changed = append(changed, key)
						wrote = true
					}
					continue
				}
				i := slices.IndexFunc(d.Accounts, func(a record) bool { return a.ID == key })
				if i < 0 || !auth.ShareableCredential(d.Accounts[i].Credential) {
					continue
				}
				provider := d.Accounts[i].Provider
				d.Accounts = slices.Delete(d.Accounts, i, i+1)
				if d.Active[provider] == key {
					delete(d.Active, provider)
				}
				changed = append(changed, key)
				wrote = true
			}
		}
		return nil
	}
	d, err := s.load()
	if err == nil {
		err = merge(&d)
	}
	if err != nil || !wrote {
		return nil, err
	}
	return changed, s.update(ctx, merge)
}
