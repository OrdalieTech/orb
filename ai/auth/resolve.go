package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type ErrorCode string

const (
	ErrorAuth  ErrorCode = "auth"
	ErrorOAuth ErrorCode = "oauth"
)

type Error struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (authError *Error) Error() string { return authError.Message }
func (authError *Error) Unwrap() error { return authError.Cause }

type APIKeyAuth interface {
	Name() string
	Resolve(context.Context, AuthContext, *Credential) (*AuthResult, error)
}

type APIKeyLogin interface {
	Login(context.Context, AuthInteraction) (*Credential, error)
}

type AuthCheck struct {
	Source string         `json:"source,omitempty"`
	Type   CredentialType `json:"type"`
}

type APIKeyCheck interface {
	Check(context.Context, AuthContext, *Credential) (*AuthCheck, error)
}

type EnvAPIKeyAuth struct {
	DisplayName string
	EnvVars     []string
}

func (method EnvAPIKeyAuth) Name() string { return method.DisplayName }

func (method EnvAPIKeyAuth) Login(ctx context.Context, interaction AuthInteraction) (*Credential, error) {
	key, err := interaction.Prompt(ctx, AuthPrompt{Type: PromptSecret, Message: "Enter " + method.DisplayName})
	if err != nil {
		return nil, err
	}
	return APIKeyCredential(key), nil
}

func (method EnvAPIKeyAuth) Resolve(
	ctx context.Context,
	authContext AuthContext,
	credential *Credential,
) (*AuthResult, error) {
	if credential != nil && credential.Key != nil && *credential.Key != "" {
		key := *credential.Key
		return &AuthResult{Auth: ModelAuth{APIKey: &key}, Env: cloneStrings(credential.Env), Source: "stored credential"}, nil
	}
	for _, name := range method.EnvVars {
		if value, ok := authContext.Env(ctx, name); ok {
			key := value
			return &AuthResult{Auth: ModelAuth{APIKey: &key}, Source: name}, nil
		}
	}
	return nil, nil
}

type ProviderAuth struct {
	APIKey APIKeyAuth
	OAuth  OAuth
}

type ResolutionOverrides struct {
	APIKey *string
	Env    map[string]string
}

func ResolveProviderAuth(
	ctx context.Context,
	providerID string,
	methods ProviderAuth,
	credentials CredentialStore,
	authContext AuthContext,
	overrides *ResolutionOverrides,
) (*AuthResult, error) {
	requestContext := authContext
	if overrides != nil && overrides.Env != nil {
		requestContext = overlayContext{base: authContext, env: overrides.Env}
	}
	if overrides != nil && overrides.APIKey != nil && methods.APIKey != nil {
		credential := &Credential{Type: CredentialAPIKey, Key: overrides.APIKey, Env: cloneStrings(overrides.Env)}
		return resolveAPIKey(ctx, providerID, methods.APIKey, requestContext, credential)
	}

	stored, err := credentials.Read(ctx, providerID)
	if err != nil {
		return nil, &Error{Code: ErrorAuth, Message: fmt.Sprintf("Credential store read failed for %s", providerID), Cause: err}
	}
	if stored != nil {
		switch {
		case stored.Type == CredentialOAuth && methods.OAuth != nil:
			return resolveStoredOAuth(ctx, providerID, methods.OAuth, credentials, stored)
		case stored.Type == CredentialAPIKey && methods.APIKey != nil:
			if overrides != nil && overrides.Env != nil {
				stored.Env = mergeStringMaps(stored.Env, overrides.Env)
			}
			return resolveAPIKey(ctx, providerID, methods.APIKey, requestContext, stored)
		default:
			return nil, nil
		}
	}
	if methods.APIKey == nil {
		return nil, nil
	}
	return resolveAPIKey(ctx, providerID, methods.APIKey, requestContext, nil)
}

func resolveStoredOAuth(
	ctx context.Context,
	providerID string,
	method OAuth,
	credentials CredentialStore,
	stored *Credential,
) (*AuthResult, error) {
	credential := stored
	if time.Now().UnixMilli() >= credential.Expires {
		post, err := credentials.Modify(ctx, providerID, func(current *Credential) (*Credential, error) {
			if current == nil || current.Type != CredentialOAuth {
				return nil, nil
			}
			if time.Now().UnixMilli() < current.Expires {
				return nil, nil
			}
			refreshed, refreshErr := method.Refresh(ctx, current)
			if refreshErr != nil {
				return nil, &Error{Code: ErrorOAuth, Message: fmt.Sprintf("OAuth refresh failed for %s", providerID), Cause: refreshErr}
			}
			return refreshed, nil
		})
		if err != nil {
			var authError *Error
			if errors.As(err, &authError) {
				return nil, authError
			}
			return nil, &Error{Code: ErrorAuth, Message: fmt.Sprintf("Credential store modify failed for %s", providerID), Cause: err}
		}
		if post == nil || post.Type != CredentialOAuth {
			return nil, nil
		}
		credential = post
	}
	modelAuth, err := method.ToAuth(credential)
	if err != nil {
		return nil, &Error{Code: ErrorOAuth, Message: fmt.Sprintf("OAuth auth derivation failed for %s", providerID), Cause: err}
	}
	return &AuthResult{Auth: modelAuth, Source: "OAuth"}, nil
}

func resolveAPIKey(
	ctx context.Context,
	providerID string,
	method APIKeyAuth,
	authContext AuthContext,
	credential *Credential,
) (*AuthResult, error) {
	result, err := method.Resolve(ctx, authContext, credential)
	if err != nil {
		return nil, &Error{Code: ErrorAuth, Message: fmt.Sprintf("API key auth failed for provider %s", providerID), Cause: err}
	}
	return result, nil
}

type overlayContext struct {
	base AuthContext
	env  map[string]string
}

func (authContext overlayContext) Env(ctx context.Context, name string) (string, bool) {
	if value := authContext.env[name]; value != "" {
		return value, true
	}
	return authContext.base.Env(ctx, name)
}

func (authContext overlayContext) FileExists(ctx context.Context, path string) bool {
	return authContext.base.FileExists(ctx, path)
}

func mergeStringMaps(base, overrides map[string]string) map[string]string {
	merged := cloneStrings(base)
	if merged == nil {
		merged = make(map[string]string)
	}
	for key, value := range overrides {
		merged[key] = value
	}
	return merged
}
