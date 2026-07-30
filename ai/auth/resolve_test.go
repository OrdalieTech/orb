package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testContext map[string]string

func (values testContext) Env(_ context.Context, name string) (string, bool) {
	value, ok := values[name]
	return value, ok && value != ""
}

func (testContext) FileExists(context.Context, string) bool { return false }

type testOAuth struct {
	refreshes atomic.Int32
	err       error
	expiresIn time.Duration
}

func (*testOAuth) Name() string { return "OAuth" }
func (*testOAuth) Login(context.Context, AuthInteraction) (*Credential, error) {
	return nil, errors.New("unused")
}
func (oauth *testOAuth) Refresh(_ context.Context, current *Credential) (*Credential, error) {
	oauth.refreshes.Add(1)
	if oauth.err != nil {
		return nil, oauth.err
	}
	time.Sleep(10 * time.Millisecond)
	expiresIn := oauth.expiresIn
	if expiresIn == 0 {
		expiresIn = time.Hour
	}
	return OAuthCredential("rotated", "fresh", time.Now().Add(expiresIn).UnixMilli()), nil
}
func (*testOAuth) ToAuth(credential *Credential) (ModelAuth, error) {
	key := credential.Access
	return ModelAuth{APIKey: &key}, nil
}

func TestResolveProviderAuthPrecedenceAndStoredOwnership(t *testing.T) {
	ctx := context.Background()
	methods := ProviderAuth{APIKey: EnvAPIKeyAuth{DisplayName: "Key", EnvVars: []string{"KEY"}}}
	store := NewMemoryStore(map[string]*Credential{"provider": APIKeyCredential("stored")})
	override := "override"
	result, err := ResolveProviderAuth(ctx, "provider", methods, store, testContext{"KEY": "ambient"}, &ResolutionOverrides{APIKey: &override})
	if err != nil || result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "override" {
		t.Fatalf("override result = %#v, %v", result, err)
	}
	result, err = ResolveProviderAuth(ctx, "provider", methods, store, testContext{"KEY": "ambient"}, nil)
	if err != nil || result == nil || *result.Auth.APIKey != "stored" {
		t.Fatalf("stored result = %#v, %v", result, err)
	}

	oauthOnly := NewMemoryStore(map[string]*Credential{"provider": OAuthCredential("refresh", "expired", 0)})
	result, err = ResolveProviderAuth(ctx, "provider", methods, oauthOnly, testContext{"KEY": "ambient"}, nil)
	if err != nil || result != nil {
		t.Fatalf("mismatched stored type fell through to ambient: %#v, %v", result, err)
	}
}

func TestEnvAPIKeyAuthReturnsStoredCredentialEnvironment(t *testing.T) {
	credential := APIKeyCredential("stored")
	credential.Env = map[string]string{"REGION": "stored-region"}
	result, err := (EnvAPIKeyAuth{DisplayName: "Key"}).Resolve(context.Background(), testContext{}, credential)
	if err != nil || result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "stored" || result.Env["REGION"] != "stored-region" {
		t.Fatalf("stored result = %#v, %v", result, err)
	}
	result.Env["REGION"] = "changed"
	if credential.Env["REGION"] != "stored-region" {
		t.Fatal("resolved environment aliases stored credential")
	}
}

func TestResolveProviderAuthRefreshesExpiredOAuthOnce(t *testing.T) {
	ctx := context.Background()
	flow := &testOAuth{}
	store := NewMemoryStore(map[string]*Credential{"provider": OAuthCredential("refresh", "expired", 0)})
	methods := ProviderAuth{OAuth: flow}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := ResolveProviderAuth(ctx, "provider", methods, store, testContext{}, nil)
			if err != nil || result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "fresh" {
				t.Errorf("refresh result = %#v, %v", result, err)
			}
		}()
	}
	wait.Wait()
	if got := flow.refreshes.Load(); got != 1 {
		t.Fatalf("refresh count = %d, want 1", got)
	}
}

func TestResolveProviderAuthRefreshesOAuthWithinDefaultValidityWindow(t *testing.T) {
	flow := &testOAuth{}
	store := NewMemoryStore(map[string]*Credential{
		"provider": OAuthCredential("refresh", "nearly-expired", time.Now().Add(2*time.Minute).UnixMilli()),
	})
	result, err := ResolveProviderAuth(context.Background(), "provider", ProviderAuth{OAuth: flow}, store, testContext{}, nil)
	if err != nil || result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "fresh" {
		t.Fatalf("refresh result = %#v, %v", result, err)
	}
	if got := flow.refreshes.Load(); got != 1 {
		t.Fatalf("refresh count = %d, want 1", got)
	}
}

func TestResolveProviderAuthEnforcesExplicitMinimumAfterRefresh(t *testing.T) {
	minimum := 30 * time.Minute
	flow := &testOAuth{expiresIn: 20 * time.Minute}
	store := NewMemoryStore(map[string]*Credential{
		"provider": OAuthCredential("refresh", "old", time.Now().Add(25*time.Minute).UnixMilli()),
	})
	_, err := ResolveProviderAuth(context.Background(), "provider", ProviderAuth{OAuth: flow}, store, testContext{}, &ResolutionOverrides{
		MinOAuthValidity: &minimum,
	})
	var authError *Error
	if !errors.As(err, &authError) || authError.Code != ErrorOAuth ||
		err.Error() != "OAuth refresh returned a token that expires too soon for provider" {
		t.Fatalf("minimum-validity error = %#v", err)
	}
	if got := flow.refreshes.Load(); got != 1 {
		t.Fatalf("refresh count = %d, want 1", got)
	}
}

func TestResolveProviderAuthDefaultRefreshDoesNotImposeProviderContract(t *testing.T) {
	flow := &testOAuth{expiresIn: 4 * time.Minute}
	store := NewMemoryStore(map[string]*Credential{
		"provider": OAuthCredential("refresh", "old", time.Now().Add(time.Minute).UnixMilli()),
	})
	result, err := ResolveProviderAuth(context.Background(), "provider", ProviderAuth{OAuth: flow}, store, testContext{}, nil)
	if err != nil || result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "fresh" {
		t.Fatalf("default refresh result = %#v, %v", result, err)
	}
}

func TestResolveProviderAuthWrapsRefreshFailure(t *testing.T) {
	flow := &testOAuth{err: errors.New("invalid grant")}
	store := NewMemoryStore(map[string]*Credential{"provider": OAuthCredential("refresh", "expired", 0)})
	_, err := ResolveProviderAuth(context.Background(), "provider", ProviderAuth{OAuth: flow}, store, testContext{}, nil)
	var authError *Error
	if !errors.As(err, &authError) || authError.Code != ErrorOAuth {
		t.Fatalf("refresh error = %#v, want OAuth error", err)
	}
	// Upstream 4cf0a729: the surfaced message keeps the underlying cause,
	// because callers show the message only.
	if err.Error() != "OAuth refresh failed for provider: invalid grant" || !errors.Is(err, flow.err) {
		t.Fatalf("refresh error message/cause = %q / %v", err, errors.Unwrap(err))
	}
}

func TestEnvironmentContextIgnoresWhitespaceValues(t *testing.T) {
	t.Setenv("ORB_AUTH_WHITESPACE", "  ")
	value, ok := (EnvironmentContext{}).Env(context.Background(), "ORB_AUTH_WHITESPACE")
	if ok || value != "" {
		t.Fatalf("environment value = %q, %t", value, ok)
	}
}
