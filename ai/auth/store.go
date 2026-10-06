package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// Store keeps credentials in a document shaped like upstream's auth.json.
type Store struct{ document document.Document }

func NewDocumentStore(document document.Document) *Store { return &Store{document: document} }

func NewMemoryStore(initial map[string]*Credential) *Store {
	store := NewDocumentStore(&document.Memory{})
	for provider, credential := range initial {
		_, _ = store.Modify(context.Background(), provider, func(*Credential) (*Credential, error) { return credential, nil })
	}
	return store
}

func (store *Store) load(ctx context.Context) (credentials, error) {
	data, err := store.document.Read(ctx)
	if err != nil {
		return credentials{}, err
	}
	return parseCredentials(data)
}

func (store *Store) Read(ctx context.Context, provider string) (*Credential, error) {
	stored, err := store.load(ctx)
	return stored.byProvider[provider], err
}

func (store *Store) List(ctx context.Context) ([]CredentialInfo, error) {
	stored, err := store.load(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]CredentialInfo, 0, len(stored.order))
	for _, provider := range stored.order {
		result = append(result, CredentialInfo{ProviderID: provider, Type: stored.byProvider[provider].Type})
	}
	return result, nil
}

func (store *Store) Modify(ctx context.Context, provider string, modify ModifyFunc) (result *Credential, err error) {
	err = store.update(ctx, func(stored *credentials) (bool, error) {
		current, exists := stored.byProvider[provider]
		next, err := modify(current)
		if err != nil {
			return false, err
		}
		if next == nil {
			result = current
			return false, nil
		}
		if !exists {
			stored.order = append(stored.order, provider)
		}
		stored.byProvider[provider] = next
		result = next.Clone()
		return true, nil
	})
	return result, err
}

func (store *Store) Delete(ctx context.Context, provider string) error {
	return store.update(ctx, func(stored *credentials) (bool, error) {
		delete(stored.byProvider, provider)
		for index, item := range stored.order {
			if item == provider {
				stored.order = append(stored.order[:index], stored.order[index+1:]...)
				break
			}
		}
		return true, nil
	})
}

func (store *Store) update(ctx context.Context, change func(*credentials) (bool, error)) error {
	return store.document.Update(ctx, func(current []byte) ([]byte, error) {
		stored, err := parseCredentials(current)
		if err != nil {
			return nil, err
		}
		if write, err := change(&stored); err != nil || !write {
			return current, err
		}
		return MarshalCredentials(stored.order, stored.byProvider)
	})
}

// BindCredentialStore freezes a selectable store for one provider resolution.
// Ordinary stores pass through unchanged.
func BindCredentialStore(ctx context.Context, store CredentialStore, provider string) (CredentialStore, error) {
	if selectable, ok := store.(interface {
		ForProvider(context.Context, string) (CredentialStore, error)
	}); ok {
		return selectable.ForProvider(ctx, provider)
	}
	return store, nil
}

// credentials keeps auth.json's member order.
type credentials struct {
	order      []string
	byProvider map[string]*Credential
}

// ParseCredentials decodes auth.json content; empty content is an empty store
// (upstream parseStorageData), which self-heals a 0-byte file.
func ParseCredentials(data []byte) (map[string]*Credential, error) {
	stored, err := parseCredentials(data)
	return stored.byProvider, err
}

func parseCredentials(data []byte) (credentials, error) {
	stored := credentials{byProvider: map[string]*Credential{}}
	if len(data) == 0 {
		return stored, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	token, err := decoder.Token()
	if err != nil {
		return credentials{}, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return credentials{}, errors.New("auth.json must contain a JSON object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return credentials{}, err
		}
		provider, ok := token.(string)
		if !ok {
			return credentials{}, errors.New("auth.json contains an invalid provider key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return credentials{}, err
		}
		var credential Credential
		if err := json.Unmarshal(raw, &credential); err != nil {
			return credentials{}, fmt.Errorf("auth.json provider %q: %w", provider, err)
		}
		if _, exists := stored.byProvider[provider]; !exists {
			stored.order = append(stored.order, provider)
		}
		stored.byProvider[provider] = &credential
	}
	if _, err := decoder.Token(); err != nil {
		return credentials{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return credentials{}, errors.New("auth.json contains multiple JSON values")
		}
		return credentials{}, err
	}
	return stored, nil
}

// MarshalCredentials matches upstream's JSON.stringify(data, null, 2) of auth.json.
func MarshalCredentials(order []string, byProvider map[string]*Credential) ([]byte, error) {
	var compact bytes.Buffer
	compact.WriteByte('{')
	for index, provider := range order {
		if index > 0 {
			compact.WriteByte(',')
		}
		name, err := jsonwire.Marshal(provider)
		if err != nil {
			return nil, err
		}
		value, err := byProvider[provider].MarshalJSON()
		if err != nil {
			return nil, err
		}
		compact.Write(name)
		compact.WriteByte(':')
		compact.Write(value)
	}
	compact.WriteByte('}')
	normalized, err := ai.NormalizeJSONStringifyJSON(compact.Bytes())
	if err != nil || len(order) == 0 {
		return normalized, err
	}
	var indented bytes.Buffer
	err = json.Indent(&indented, normalized, "", "  ")
	return indented.Bytes(), err
}
