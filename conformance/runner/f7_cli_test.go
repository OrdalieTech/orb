package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type f7CLIFixture struct {
	SchemaVersion int         `json:"schemaVersion"`
	Model         f7CLIModel  `json:"model"`
	Server        f7CLIServer `json:"server"`
	Cases         []f7CLICase `json:"cases"`
}

type f7CLIModel struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

type f7CLIServer struct {
	Path   string `json:"path"`
	Stream string `json:"stream"`
}

type f7CLICase struct {
	Name             string             `json:"name"`
	Route            string             `json:"route"`
	Args             []string           `json:"args"`
	Stdin            string             `json:"stdin"`
	ConfiguredModel  bool               `json:"configuredModel"`
	ExpectedPrompt   string             `json:"expectedPrompt"`
	ExpectedRequests int                `json:"expectedRequests"`
	Expected         f7CLIProcessResult `json:"expected"`
}

type f7CLIProcessResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

const f7CLIPackageDir = "/fixture/pi-package"

func TestF7CLIOutputMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "F7-cli")
	if manifest.Family != "F7-cli" || manifest.Generator != "conformance/extract/f7-cli.ts" {
		t.Fatalf("unexpected F7 CLI manifest: %+v", manifest)
	}
	var fixture f7CLIFixture
	runner.LoadJSON(t, "F7-cli", "cases.json", &fixture)
	if fixture.SchemaVersion != 1 || fixture.Model.Provider == "" || fixture.Model.ID == "" || len(fixture.Cases) != 4 {
		t.Fatalf("F7 CLI fixture = version %d, model %s/%s, cases %d", fixture.SchemaVersion, fixture.Model.Provider, fixture.Model.ID, len(fixture.Cases))
	}
	binary := buildF7CLIBinary(t)
	for _, fixtureCase := range fixture.Cases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			runF7CLICase(t, binary, fixture, fixtureCase)
		})
	}
}

func buildF7CLIBinary(t testing.TB) string {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "pi-go")
	command := exec.Command("go", "build", "-o", binary, "./cmd/pi")
	command.Dir = repoRoot
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, buildErr := command.CombinedOutput(); buildErr != nil {
		t.Fatalf("build pi-go: %v\n%s", buildErr, output)
	}
	return binary
}

func runF7CLICase(t *testing.T, binary string, fixture f7CLIFixture, fixtureCase f7CLICase) {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	projectDir := filepath.Join(root, "project")
	homeDir := filepath.Join(root, "home")
	for _, directory := range []string{agentDir, projectDir, homeDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	var requestMu sync.Mutex
	requests := make([][]byte, 0)
	serverErrors := make([]string, 0)
	var server *httptest.Server
	if fixtureCase.ConfiguredModel {
		server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				requestMu.Lock()
				serverErrors = append(serverErrors, err.Error())
				requestMu.Unlock()
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			requestMu.Lock()
			if request.Method != http.MethodPost || request.URL.Path != fixture.Server.Path {
				serverErrors = append(serverErrors, request.Method+" "+request.URL.Path)
			}
			requests = append(requests, bytes.Clone(body))
			requestMu.Unlock()
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, fixture.Server.Stream)
		}))
		defer server.Close()
		writeF7CLIModels(t, agentDir, server.URL+"/v1", fixture.Model)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, fixtureCase.Args...)
	command.Dir = projectDir
	command.Env = f7CLIEnvironment(homeDir, agentDir)
	command.Stdin = strings.NewReader(fixtureCase.Stdin)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("CLI timed out: %v", ctx.Err())
	}
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("run CLI: %v", err)
		}
		exitCode = exitError.ExitCode()
	}
	if exitCode != fixtureCase.Expected.ExitCode {
		t.Errorf("exit code = %d, want %d", exitCode, fixtureCase.Expected.ExitCode)
	}
	gotStdout := stdout.Bytes()
	if fixtureCase.Route == "json" {
		gotStdout = f7CLICanonicalizeJSONStdout(gotStdout, projectDir)
	}
	gotStdout = bytes.ReplaceAll(gotStdout, []byte(f7CLIPackageDir), []byte("<package>"))
	if diff := runner.ByteDiff([]byte(fixtureCase.Expected.Stdout), gotStdout); diff != "" {
		t.Errorf("stdout differs:\n%s", diff)
	}
	gotStderr := bytes.ReplaceAll(stderr.Bytes(), []byte(f7CLIPackageDir), []byte("<package>"))
	if diff := runner.ByteDiff([]byte(fixtureCase.Expected.Stderr), gotStderr); diff != "" {
		t.Errorf("stderr differs:\n%s", diff)
	}

	requestMu.Lock()
	defer requestMu.Unlock()
	if len(serverErrors) > 0 {
		t.Errorf("server errors: %v", serverErrors)
	}
	if len(requests) != fixtureCase.ExpectedRequests {
		t.Errorf("requests = %d, want %d", len(requests), fixtureCase.ExpectedRequests)
	}
	if fixtureCase.ExpectedPrompt != "" {
		if len(requests) == 0 {
			t.Error("no request captured for expected prompt")
		} else if !f7CLIRequestContainsString(t, requests[0], fixtureCase.ExpectedPrompt) {
			t.Errorf("request did not contain prompt %q: %s", fixtureCase.ExpectedPrompt, requests[0])
		}
	}
}

func writeF7CLIModels(t testing.TB, agentDir, baseURL string, model f7CLIModel) {
	t.Helper()
	models := map[string]any{
		"providers": map[string]any{
			model.Provider: map[string]any{
				"baseUrl": baseURL,
				"api":     "openai-completions",
				"apiKey":  "fixture-key",
				"models": []any{map[string]any{
					"id": model.ID, "name": "CLI Fixture", "reasoning": false,
					"input":         []string{"text"},
					"cost":          map[string]float64{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
					"contextWindow": 128000, "maxTokens": 16384,
				}},
			},
		},
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func f7CLIEnvironment(homeDir, agentDir string) []string {
	environment := []string{
		"HOME=" + homeDir,
		"USERPROFILE=" + homeDir,
		"LANG=C.UTF-8",
		"TZ=UTC",
		"TERM=dumb",
		"CI=1",
		"NO_COLOR=1",
		"PI_OFFLINE=1",
		"PI_SKIP_VERSION_CHECK=1",
		"PI_PACKAGE_DIR=" + f7CLIPackageDir,
		config.EnvAgentDir + "=" + agentDir,
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	return environment
}

var f7CLITimestamp = regexp.MustCompile(`"timestamp":"[^"]+"`)

func f7CLICanonicalizeJSONStdout(encoded []byte, projectDir string) []byte {
	canonical := []byte(runner.ReplacePathAliases(string(encoded), projectDir, "<cwd>"))
	return f7CLITimestamp.ReplaceAll(canonical, []byte(`"timestamp":"<timestamp>"`))
}

func f7CLIRequestContainsString(t testing.TB, encoded []byte, expected string) bool {
	t.Helper()
	var value any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Errorf("decode request: %v", err)
		return false
	}
	return f7CLIContainsString(value, expected)
}

func f7CLIContainsString(value any, expected string) bool {
	switch typed := value.(type) {
	case string:
		return typed == expected
	case []any:
		for _, entry := range typed {
			if f7CLIContainsString(entry, expected) {
				return true
			}
		}
	case map[string]any:
		for _, entry := range typed {
			if f7CLIContainsString(entry, expected) {
				return true
			}
		}
	}
	return false
}
