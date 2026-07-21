package auth

import (
	"context"
	"encoding/json"
)

type CredentialType string

const (
	CredentialAPIKey CredentialType = "api_key"
	CredentialOAuth  CredentialType = "oauth"
)

// Credential retains unknown fields because auth.json is shared with TS pi and
// later provider OAuth flows attach provider-specific token metadata.
type Credential struct {
	Type    CredentialType
	Key     *string
	Env     map[string]string
	Refresh string
	Access  string
	Expires int64
	Extra   map[string]json.RawMessage

	order           []string
	envOrder        []string
	expiresJSON     json.RawMessage
	expiresNumber   float64
	expiresBaseline int64
}

func APIKeyCredential(key string) *Credential {
	return &Credential{Type: CredentialAPIKey, Key: &key, order: []string{"type", "key"}}
}

func APIKeyEnvCredential(env map[string]string, envOrder ...string) *Credential {
	return &Credential{
		Type: CredentialAPIKey, Env: cloneStrings(env),
		order: []string{"type", "env"}, envOrder: append([]string(nil), envOrder...),
	}
}

func OAuthCredential(refresh, access string, expires int64) *Credential {
	return &Credential{
		Type: CredentialOAuth, Refresh: refresh, Access: access, Expires: expires,
		order: []string{"type", "refresh", "access", "expires"},
	}
}

// OAuthCredentialAccessFirst preserves the property insertion order used by
// providers whose upstream credential literals place access before refresh.
func OAuthCredentialAccessFirst(access, refresh string, expires int64) *Credential {
	return &Credential{
		Type: CredentialOAuth, Refresh: refresh, Access: access, Expires: expires,
		order: []string{"type", "access", "refresh", "expires"},
	}
}

func (credential *Credential) SetExtra(name string, value json.RawMessage) {
	if credential.Extra == nil {
		credential.Extra = make(map[string]json.RawMessage)
	}
	credential.Extra[name] = append(json.RawMessage(nil), value...)
	if !contains(credential.order, name) {
		credential.order = append(credential.order, name)
	}
}

func (credential *Credential) Clone() *Credential {
	if credential == nil {
		return nil
	}
	cloned := *credential
	if credential.Key != nil {
		key := *credential.Key
		cloned.Key = &key
	}
	cloned.Env = cloneStrings(credential.Env)
	cloned.Extra = cloneRaw(credential.Extra)
	cloned.order = append([]string(nil), credential.order...)
	cloned.envOrder = append([]string(nil), credential.envOrder...)
	cloned.expiresJSON = append(json.RawMessage(nil), credential.expiresJSON...)
	return &cloned
}

type CredentialInfo struct {
	ProviderID string
	Type       CredentialType
}

type ModifyFunc func(current *Credential) (*Credential, error)

// CredentialStore is the upstream seam that lets applications own persistence.
type CredentialStore interface {
	Read(context.Context, string) (*Credential, error)
	List(context.Context) ([]CredentialInfo, error)
	Modify(context.Context, string, ModifyFunc) (*Credential, error)
	Delete(context.Context, string) error
}

type ModelAuth struct {
	APIKey  *string            `json:"apiKey,omitempty"`
	Headers map[string]*string `json:"headers,omitempty"`
	BaseURL *string            `json:"baseUrl,omitempty"`
}

type AuthResult struct {
	Auth   ModelAuth         `json:"auth"`
	Env    map[string]string `json:"env,omitempty"`
	Source string            `json:"source,omitempty"`
}

type AuthContext interface {
	Env(context.Context, string) (string, bool)
	FileExists(context.Context, string) bool
}

type AuthType string

const (
	AuthTypeAPIKey AuthType = "api_key"
	AuthTypeOAuth  AuthType = "oauth"
)

type PromptType string

const (
	PromptText       PromptType = "text"
	PromptSecret     PromptType = "secret"
	PromptSelect     PromptType = "select"
	PromptManualCode PromptType = "manual_code"
)

type PromptOption struct {
	ID          string
	Label       string
	Description string
}

type AuthPrompt struct {
	Type        PromptType
	Message     string
	Placeholder string
	Options     []PromptOption
}

type AuthEventType string

const (
	EventInfo       AuthEventType = "info"
	EventAuthURL    AuthEventType = "auth_url"
	EventDeviceCode AuthEventType = "device_code"
	EventProgress   AuthEventType = "progress"
)

type AuthInfoLink struct {
	URL   string
	Label string
}

type AuthEvent struct {
	Type             AuthEventType
	Message          string
	Links            []AuthInfoLink
	URL              string
	Instructions     string
	UserCode         string
	VerificationURI  string
	IntervalSeconds  int
	ExpiresInSeconds int
}

type AuthInteraction interface {
	Prompt(context.Context, AuthPrompt) (string, error)
	Notify(AuthEvent)
}

type OAuth interface {
	Name() string
	Login(context.Context, AuthInteraction) (*Credential, error)
	Refresh(context.Context, *Credential) (*Credential, error)
	ToAuth(*Credential) (ModelAuth, error)
}

type OAuthLoginLabel interface {
	LoginLabel() string
}

func cloneStrings(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneRaw(source map[string]json.RawMessage) map[string]json.RawMessage {
	if source == nil {
		return nil
	}
	cloned := make(map[string]json.RawMessage, len(source))
	for key, value := range source {
		cloned[key] = append(json.RawMessage(nil), value...)
	}
	return cloned
}
