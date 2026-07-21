package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type authStorageFixture struct {
	InitialRaw   string                        `json:"initialRaw"`
	InitialReads map[string]json.RawMessage    `json:"initialReads"`
	InitialList  []authStorageFixtureInfo      `json:"initialList"`
	Operations   []authStorageFixtureOperation `json:"operations"`
	ExpectedRaw  string                        `json:"expectedRaw"`
	ExpectedList []authStorageFixtureInfo      `json:"expectedList"`
	OAuthPages   struct {
		Success string `json:"success"`
		Error   string `json:"error"`
	} `json:"oauthPages"`
	Migration authMigrationFixture `json:"migration"`
}

type authMigrationFixture struct {
	InitialOAuthRaw     string   `json:"initialOAuthRaw"`
	InitialSettingsRaw  string   `json:"initialSettingsRaw"`
	ExpectedProviders   []string `json:"expectedProviders"`
	ExpectedAuthRaw     string   `json:"expectedAuthRaw"`
	ExpectedSettingsRaw string   `json:"expectedSettingsRaw"`
	OAuthRenamed        string   `json:"oauthRenamed"`
}

type authStorageFixtureInfo struct {
	ProviderID string                `json:"providerId"`
	Type       aiauth.CredentialType `json:"type"`
}

type authStorageFixtureOperation struct {
	Type       string          `json:"type"`
	Provider   string          `json:"provider"`
	Credential json.RawMessage `json:"credential"`
}

func TestAuthStorageConformance(t *testing.T) {
	var fixture authStorageFixture
	runner.LoadJSON(t, "F2", "auth-storage.json", &fixture)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(fixture.InitialRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := NewAuthStorage(authPath)
	if err != nil {
		t.Fatal(err)
	}

	for provider, expected := range fixture.InitialReads {
		credential, err := storage.Read(context.Background(), provider)
		if err != nil {
			t.Fatalf("read %s: %v", provider, err)
		}
		actual, err := json.Marshal(credential)
		if err != nil {
			t.Fatal(err)
		}
		wantCanonical, err := runner.CanonicalJSON(expected)
		if err != nil {
			t.Fatal(err)
		}
		gotCanonical, err := runner.CanonicalJSON(actual)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotCanonical, wantCanonical) {
			t.Fatalf("read %s = %s, want %s", provider, actual, expected)
		}
	}
	if got := fixtureInfo(t, storage); !reflect.DeepEqual(got, fixture.InitialList) {
		t.Fatalf("initial list = %#v, want %#v", got, fixture.InitialList)
	}

	for _, operation := range fixture.Operations {
		switch operation.Type {
		case "modify":
			var credential aiauth.Credential
			if err := json.Unmarshal(operation.Credential, &credential); err != nil {
				t.Fatal(err)
			}
			if _, err := storage.Modify(context.Background(), operation.Provider, func(*aiauth.Credential) (*aiauth.Credential, error) {
				return &credential, nil
			}); err != nil {
				t.Fatal(err)
			}
		case "delete":
			if err := storage.Delete(context.Background(), operation.Provider); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unknown auth fixture operation %q", operation.Type)
		}
	}
	contents, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != fixture.ExpectedRaw {
		t.Fatalf("Go auth.json differs from TS pi\n%s", runner.ByteDiff([]byte(fixture.ExpectedRaw), contents))
	}
	if got := fixtureInfo(t, storage); !reflect.DeepEqual(got, fixture.ExpectedList) {
		t.Fatalf("final list = %#v, want %#v", got, fixture.ExpectedList)
	}
	if got := oauth.OAuthSuccessHTML(`A&B <done> "quoted" 'single'`); got != fixture.OAuthPages.Success {
		t.Fatalf("OAuth success page differs from upstream\n%s", runner.ByteDiff([]byte(fixture.OAuthPages.Success), []byte(got)))
	}
	if got := oauth.OAuthErrorHTML(`A&B <failed> "quoted" 'single'`, `detail & <trace> "quoted" 'single'`); got != fixture.OAuthPages.Error {
		t.Fatalf("OAuth error page differs from upstream\n%s", runner.ByteDiff([]byte(fixture.OAuthPages.Error), []byte(got)))
	}
	verifyAuthMigrationFixture(t, fixture.Migration)

	if os.Getenv("PIGO_AUTH_TS_VERIFY") == "1" {
		verifyAuthFileWithUpstream(t, authPath)
		verifyAuthLockWithUpstream(t, authPath, storage)
	}
}

func verifyAuthMigrationFixture(t *testing.T, fixture authMigrationFixture) {
	t.Helper()
	agentDir := t.TempDir()
	oauthPath := filepath.Join(agentDir, "oauth.json")
	settingsPath := filepath.Join(agentDir, "settings.json")
	authPath := filepath.Join(agentDir, "auth.json")
	if err := os.WriteFile(oauthPath, []byte(fixture.InitialOAuthRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(fixture.InitialSettingsRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	providers, err := MigrateAuthToAuthJSON(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(providers, fixture.ExpectedProviders) {
		t.Fatalf("migrated providers = %#v, want %#v", providers, fixture.ExpectedProviders)
	}
	for _, check := range []struct {
		path     string
		expected string
	}{
		{authPath, fixture.ExpectedAuthRaw},
		{settingsPath, fixture.ExpectedSettingsRaw},
		{oauthPath + ".migrated", fixture.OAuthRenamed},
	} {
		actual, err := os.ReadFile(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if string(actual) != check.expected {
			t.Fatalf("%s differs from upstream\n%s", filepath.Base(check.path), runner.ByteDiff([]byte(check.expected), actual))
		}
	}
	if _, err := os.Stat(oauthPath); !os.IsNotExist(err) {
		t.Fatalf("legacy oauth.json still exists after migration: %v", err)
	}
}

func fixtureInfo(t *testing.T, storage *AuthStorage) []authStorageFixtureInfo {
	t.Helper()
	listed, err := storage.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := make([]authStorageFixtureInfo, len(listed))
	for index, info := range listed {
		result[index] = authStorageFixtureInfo{ProviderID: info.ProviderID, Type: info.Type}
	}
	return result
}

func verifyAuthFileWithUpstream(t *testing.T, authPath string) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	upstream := filepath.Join(root, ".upstream")
	command := exec.Command("node", "--import", "tsx", "../conformance/extract/f2-auth-verify.ts", authPath)
	command.Dir = upstream
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("TS pi could not read Go auth.json: %v\n%s", err, output)
	}
}

func verifyAuthLockWithUpstream(t *testing.T, authPath string, storage *AuthStorage) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	upstream := filepath.Join(root, ".upstream")
	markerDir := t.TempDir()
	readyPath := filepath.Join(markerDir, "ready")
	releasePath := filepath.Join(markerDir, "release")
	command := exec.Command("node", "--import", "tsx", "../conformance/extract/f2-auth-verify.ts", authPath, "hold-lock", readyPath, releasePath)
	command.Dir = upstream
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			<-done
			t.Fatalf("TS pi did not acquire auth lock\n%s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := storage.Modify(ctx, "go-contender", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return aiauth.APIKeyCredential("must-not-write"), nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		_ = os.WriteFile(releasePath, nil, 0o600)
		<-done
		t.Fatalf("Go write was not blocked by TS proper-lockfile: %v", err)
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("TS lock writer failed: %v\n%s", err, output.String())
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("TS lock writer did not finish")
	}
	storage.Reload()
	credential, err := storage.Read(context.Background(), "typescript-lock")
	if err != nil || credential == nil || credential.Key == nil || *credential.Key != "typescript" {
		t.Fatalf("TS write after lock release = %#v, %v", credential, err)
	}
	if contender, err := storage.Read(context.Background(), "go-contender"); err != nil || contender != nil {
		t.Fatalf("blocked Go contender was persisted: %#v, %v", contender, err)
	}
}
