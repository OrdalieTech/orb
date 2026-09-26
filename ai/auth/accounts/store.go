// Package accounts adds named credentials without changing the provider-keyed
// compatibility store. Documents and stores are supplied by the embedding assembly.
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/ai/auth"
)

const DefaultID = "default"

// MaxSize bounds the accounts document in every backend.
const MaxSize = 1 << 20

// Document is the host.Document port, restated because ai sits below host.
// Updates must commit before returning.
type Document interface {
	Read(context.Context) ([]byte, error)
	Update(context.Context, func([]byte) ([]byte, error)) error
}

type Account struct {
	ID, Provider, Name string
	Type               auth.CredentialType
	Active             bool
}

type record struct {
	ID         string           `json:"id"`
	Provider   string           `json:"provider"`
	Name       string           `json:"name"`
	Credential *auth.Credential `json:"credential"`
}

type document struct {
	Version  int               `json:"version"`
	Accounts []record          `json:"accounts"`
	Active   map[string]string `json:"active"`
	Names    map[string]string `json:"names,omitempty"`
}

type Store struct {
	document Document
	base     auth.CredentialStore
}

// NewStoreWithDocument performs no I/O; an unused capability creates no documents.
func NewStoreWithDocument(document Document, base auth.CredentialStore) *Store {
	if base == nil {
		base = auth.NewMemoryStore(nil)
	}
	return &Store{document: document, base: base}
}

func (s *Store) load() (document, error) {
	data, err := s.document.Read(context.Background())
	if err != nil {
		return document{}, err
	}
	return decodeDocument(data)
}

func decodeDocument(data []byte) (document, error) {
	d := document{Version: 1, Active: map[string]string{}}
	if len(data) == 0 {
		return d, nil
	}
	if len(data) > MaxSize || json.Unmarshal(data, &d) != nil || d.Version != 1 || d.Active == nil {
		return d, errors.New("invalid accounts file")
	}
	seen := map[string]string{}
	for _, a := range d.Accounts {
		if a.ID == "" || a.ID == DefaultID || a.Provider == "" || a.Credential == nil || seen[a.ID] != "" {
			return d, errors.New("invalid account record")
		}
		seen[a.ID] = a.Provider
	}
	for provider, id := range d.Active {
		if seen[id] != provider {
			return d, errors.New("selected account is missing")
		}
	}
	return d, nil
}

func (s *Store) update(ctx context.Context, change func(*document) error) error {
	return s.document.Update(ctx, func(data []byte) ([]byte, error) {
		d, err := decodeDocument(data)
		if err != nil {
			return nil, err
		}
		if err = change(&d); err != nil {
			return nil, err
		}
		data, err = json.Marshal(d)
		if len(data) > MaxSize {
			return nil, errors.New("accounts store exceeds 1 MiB")
		}
		return data, err
	})
}

func (s *Store) Accounts(ctx context.Context) ([]Account, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	defaults, err := s.base.List(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]Account, 0, len(defaults)+len(d.Accounts))
	for _, a := range defaults {
		credential, err := s.base.Read(ctx, a.ProviderID)
		if err != nil {
			return nil, err
		}
		name := d.Names[a.ProviderID]
		if name == "" {
			name = credentialName(credential)
		}
		rows = append(rows, Account{ID: DefaultID, Provider: a.ProviderID, Name: name, Type: a.Type, Active: d.Active[a.ProviderID] == ""})
	}
	for _, a := range d.Accounts {
		rows = append(rows, Account{ID: a.ID, Provider: a.Provider, Name: a.Name, Type: a.Credential.Type, Active: d.Active[a.Provider] == a.ID})
	}
	return rows, nil
}

func (s *Store) Add(ctx context.Context, provider, name string, credential *auth.Credential) (Account, error) {
	if provider == "" || credential == nil || (credential.Type != auth.CredentialAPIKey && credential.Type != auth.CredentialOAuth) {
		return Account{}, errors.New("provider and credential are required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = credentialName(credential)
	}
	if len(name) > 128 {
		return Account{}, errors.New("account name is too long")
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return Account{}, err
	}
	result := Account{ID: hex.EncodeToString(idBytes), Provider: provider, Name: name, Type: credential.Type, Active: true}
	base, err := s.base.Read(ctx, provider)
	if err != nil {
		return Account{}, err
	}
	matchedDefault := false
	if base != nil && sameCredential(base, credential) {
		_, err = s.base.Modify(ctx, provider, func(current *auth.Credential) (*auth.Credential, error) {
			matchedDefault = current != nil && sameCredential(current, credential)
			if matchedDefault {
				return credential, nil
			}
			return current, nil
		})
		if err != nil {
			return Account{}, err
		}
	}
	err = s.update(ctx, func(d *document) error {
		if matchedDefault {
			result.ID = DefaultID
			if d.Names == nil {
				d.Names = map[string]string{}
			}
			d.Names[provider] = name
			delete(d.Active, provider)
			return nil
		}
		for i, a := range d.Accounts {
			if a.Provider == provider && sameCredential(a.Credential, credential) {
				result.ID = a.ID
				d.Accounts[i].Credential = credential.Clone()
				d.Accounts[i].Name = name
				d.Active[provider] = a.ID
				return nil
			}
		}
		d.Accounts = append(d.Accounts, record{ID: result.ID, Provider: provider, Name: name, Credential: credential.Clone()})
		d.Active[provider] = result.ID
		return nil
	})
	return result, err
}

func (s *Store) Rename(ctx context.Context, provider, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return errors.New("use an account name between 1 and 128 bytes")
	}
	return s.update(ctx, func(d *document) error {
		if id == DefaultID {
			current, err := s.base.Read(ctx, provider)
			if err != nil {
				return err
			}
			if current == nil {
				return errors.New("account no longer exists")
			}
			if d.Names == nil {
				d.Names = map[string]string{}
			}
			d.Names[provider] = name
			return nil
		}
		for i, a := range d.Accounts {
			if a.Provider == provider && a.ID == id {
				d.Accounts[i].Name = name
				return nil
			}
		}
		return errors.New("account no longer exists")
	})
}

func (s *Store) Select(ctx context.Context, provider, id string) error {
	return s.update(ctx, func(d *document) error {
		if id == DefaultID {
			credential, err := s.base.Read(ctx, provider)
			if err != nil {
				return err
			}
			if credential == nil {
				return errors.New("account no longer exists")
			}
			delete(d.Active, provider)
			return nil
		}
		for _, a := range d.Accounts {
			if a.Provider == provider && a.ID == id {
				d.Active[provider] = id
				return nil
			}
		}
		return errors.New("account no longer exists")
	})
}

// Deselect returns a provider to its default source, the credential it had
// before any account was chosen, whether or not Orb stores one.
func (s *Store) Deselect(ctx context.Context, provider string) error {
	return s.update(ctx, func(d *document) error {
		delete(d.Active, provider)
		return nil
	})
}

func (s *Store) Remove(ctx context.Context, provider, id string) error {
	if id == DefaultID {
		return s.base.Delete(ctx, provider)
	}
	return s.update(ctx, func(d *document) error {
		d.Accounts = slices.DeleteFunc(d.Accounts, func(a record) bool { return a.Provider == provider && a.ID == id })
		if d.Active[provider] == id {
			delete(d.Active, provider)
		}
		return nil
	})
}

// ForProvider pins account identity across read/refresh/write. Switching the
// active account cannot redirect an in-flight OAuth refresh into another one.
func (s *Store) ForProvider(_ context.Context, provider string) (auth.CredentialStore, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	id := d.Active[provider]
	if id == "" {
		return s.base, nil
	}
	return s.View(provider, id), nil
}

func (s *Store) View(provider, id string) auth.CredentialStore {
	if id == DefaultID {
		return s.base
	}
	return &accountView{store: s, provider: provider, id: id}
}

func (s *Store) Read(ctx context.Context, provider string) (*auth.Credential, error) {
	bound, err := s.ForProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	return bound.Read(ctx, provider)
}
func (s *Store) Modify(ctx context.Context, provider string, fn auth.ModifyFunc) (*auth.Credential, error) {
	bound, err := s.ForProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	return bound.Modify(ctx, provider, fn)
}
func (s *Store) Delete(ctx context.Context, provider string) error {
	bound, err := s.ForProvider(ctx, provider)
	if err != nil {
		return err
	}
	return bound.Delete(ctx, provider)
}
func (s *Store) List(ctx context.Context) ([]auth.CredentialInfo, error) {
	rows, err := s.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]auth.CredentialInfo, 0, len(rows))
	for _, a := range rows {
		if a.Active {
			result = append(result, auth.CredentialInfo{ProviderID: a.Provider, Type: a.Type})
		}
	}
	return result, nil
}

type accountView struct {
	store        *Store
	provider, id string
}

func (v *accountView) Read(ctx context.Context, provider string) (*auth.Credential, error) {
	if provider != v.provider {
		return nil, errors.New("account provider mismatch")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, err := v.store.load()
	if err != nil {
		return nil, err
	}
	for _, a := range d.Accounts {
		if a.ID == v.id && a.Provider == provider {
			return a.Credential.Clone(), nil
		}
	}
	return nil, errors.New("account no longer exists")
}
func (v *accountView) List(ctx context.Context) ([]auth.CredentialInfo, error) {
	a, err := v.Read(ctx, v.provider)
	if err != nil {
		return nil, err
	}
	return []auth.CredentialInfo{{ProviderID: v.provider, Type: a.Type}}, nil
}
func (v *accountView) Modify(ctx context.Context, provider string, fn auth.ModifyFunc) (*auth.Credential, error) {
	if provider != v.provider {
		return nil, errors.New("account provider mismatch")
	}
	var result *auth.Credential
	err := v.store.update(ctx, func(d *document) error {
		for i, a := range d.Accounts {
			if a.ID == v.id && a.Provider == provider {
				next, err := fn(a.Credential.Clone())
				if err != nil {
					return err
				}
				if next != nil {
					d.Accounts[i].Credential = next.Clone()
				}
				result = d.Accounts[i].Credential.Clone()
				return nil
			}
		}
		return errors.New("account no longer exists")
	})
	return result, err
}
func (v *accountView) Delete(ctx context.Context, provider string) error {
	if provider != v.provider {
		return errors.New("account provider mismatch")
	}
	return v.store.Remove(ctx, provider, v.id)
}

func credentialClaims(c *auth.Credential) map[string]any {
	if c == nil {
		return nil
	}
	parts := strings.Split(c.Access, ".")
	if len(parts) != 3 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	return claims
}
func credentialName(c *auth.Credential) string {
	claims := credentialClaims(c)
	if email, ok := claims["email"].(string); ok && email != "" {
		return email
	}
	if profile, ok := claims["https://api.openai.com/profile"].(map[string]any); ok {
		if email, ok := profile["email"].(string); ok && email != "" {
			return email
		}
	}
	// A credential held by another program names itself.
	if c != nil {
		var label string
		if json.Unmarshal(c.Extra["label"], &label) == nil && label != "" {
			return label
		}
	}
	return "Default"
}
func sameCredential(a, b *auth.Credential) bool {
	if a.Type != b.Type {
		return false
	}
	if a.Type == auth.CredentialAPIKey {
		return reflect.DeepEqual(a.Key, b.Key) && reflect.DeepEqual(a.Env, b.Env)
	}
	if a.Refresh != "" && a.Refresh == b.Refresh {
		return true
	}
	ac, bc := credentialClaims(a), credentialClaims(b)
	aa, _ := ac["https://api.openai.com/auth"].(map[string]any)
	ba, _ := bc["https://api.openai.com/auth"].(map[string]any)
	return ac["sub"] != nil && reflect.DeepEqual(ac["sub"], bc["sub"]) && reflect.DeepEqual(aa["chatgpt_account_id"], ba["chatgpt_account_id"])
}
