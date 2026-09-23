package websearch

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func mustOK(err error) {
	if err != nil {
		panic(err)
	}
}

func require(t *testing.T, condition bool, format string, args ...any) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestWebSearchBackendsAndFetchContent(t *testing.T) {
	tests := []struct {
		name, env, endpoint, method, header, body, response, want string
	}{
		{name: "exa", env: "EXA_API_KEY", endpoint: "api.exa.ai/search", method: http.MethodPost, header: "x-api-key", body: `"query":"orb"`, response: `{"results":[{"title":"Exa result","url":"https://exa.test","highlights":["match"]}]}`, want: "Exa result\nhttps://exa.test\nmatch"},
		{name: "brave", env: "BRAVE_API_KEY", endpoint: "api.search.brave.com/res/v1/web/search", method: http.MethodGet, header: "X-Subscription-Token", response: `{"web":{"results":[{"title":"Brave result","url":"https://brave.test","description":"match"}]}}`, want: "Brave result\nhttps://brave.test\nmatch"},
		{name: "tavily", env: "TAVILY_API_KEY", endpoint: "api.tavily.com/search", method: http.MethodPost, header: "Authorization", body: `"query":"orb"`, response: `{"results":[{"title":"Tavily result","url":"https://tavily.test","content":"match"}]}`, want: "Tavily result\nhttps://tavily.test\nmatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setHome(t, t.TempDir())
			for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
				t.Setenv(key, "")
			}
			t.Setenv(test.env, "secret")
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				require(t, request.Method == test.method && strings.Contains(request.URL.String(), test.endpoint), "request = %s %s", request.Method, request.URL)
				require(t, request.Header.Get(test.header) != "", "missing %s header", test.header)
				if test.body != "" {
					body, _ := io.ReadAll(request.Body)
					require(t, strings.Contains(string(body), test.body), "body = %s", body)
				}
				return response(http.StatusOK, "application/json", test.response), nil
			})}
			tool := pluginTool(t, "websearch", "web_search", Extension(client), extensions.RunnerOptions{})
			result := must(tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil))
			require(t, ai.ContentText(result.Content) == test.want, "result = %q, want %q", ai.ContentText(result.Content), test.want)
		})
	}

	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	stubDNS(t, map[string]string{"example.test": "93.184.216.34"})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(http.StatusOK, "text/html", `<html><style>no</style><body><h1>Hello &amp; hi</h1><script>no</script><p>Readable text.</p></body></html>`), nil
	})}
	fetch := pluginTool(t, "websearch", "fetch_content", Extension(client), extensions.RunnerOptions{})
	result := must(fetch.Execute(context.Background(), "fetch", map[string]any{"url": "https://example.test/page"}, nil))
	// Block tags keep their line breaks so oversized pages stay truncatable.
	require(t, ai.ContentText(result.Content) == "Hello & hi\nReadable text.", "content = %q", ai.ContentText(result.Content))
}

// stubDNS pins hostname resolution so the SSRF guard is exercised without
// depending on the network. Unlisted hosts fail to resolve, as they would live.
func stubDNS(t *testing.T, addresses map[string]string) {
	t.Helper()
	original := lookupIP
	lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		address, ok := addresses[host]
		if !ok {
			return nil, fmt.Errorf("no such host")
		}
		return []net.IPAddr{{IP: net.ParseIP(address)}}, nil
	}
	t.Cleanup(func() { lookupIP = original })
}

func TestFetchContentKeepsLargePagesReadable(t *testing.T) {
	stubDNS(t, map[string]string{"example.test": "93.184.216.34"})
	var page strings.Builder
	page.WriteString("<html><body>")
	for index := range 2000 {
		fmt.Fprintf(&page, "<p>Paragraph %d carries enough prose to push this page past the fifty kilobyte cap.</p>", index)
	}
	page.WriteString("</body></html>")
	require(t, page.Len() > 50<<10, "fixture is only %d bytes", page.Len())
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "text/html", page.String()), nil
	})}
	tool := pluginTool(t, "websearch", "fetch_content", Extension(client), extensions.RunnerOptions{})
	result := must(tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://example.test/big"}, nil))
	got := ai.ContentText(result.Content)
	require(t, strings.Contains(got, "Paragraph 0 carries") && len(got) >= 40<<10, "large page returned %d bytes: %.120q", len(got), got)
	require(t, strings.Count(got, "\n") >= 100, "page collapsed onto %d lines", strings.Count(got, "\n")+1)
	require(t, strings.HasSuffix(got, "[output truncated]"), "missing truncation marker: %.120q", got[max(0, len(got)-120):])
	// A page with no break at all still has to yield its head, not just the marker.
	head := truncateWeb(strings.Repeat("x", 60<<10))
	require(t, len(head) >= 40<<10, "unbreakable line truncated to %d bytes", len(head))
}

func TestFetchContentRejectsNonPublicDestinations(t *testing.T) {
	stubDNS(t, map[string]string{
		"public.test":   "93.184.216.34",
		"internal.test": "10.0.0.5",
	})
	var requests int
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		switch request.URL.Path {
		case "/to-loopback":
			return redirect("http://127.0.0.1/admin"), nil
		case "/loop":
			return redirect("https://public.test/loop"), nil
		}
		return response(http.StatusOK, "text/plain", "reached "+request.URL.String()), nil
	})}
	tool := pluginTool(t, "websearch", "fetch_content", Extension(client), extensions.RunnerOptions{})
	for _, test := range []struct {
		name, url, want string
		wantRequests    int
	}{
		{name: "loopback literal", url: "http://127.0.0.1/", want: "blocked non-public address"},
		{name: "metadata service", url: "http://169.254.169.254/latest/meta-data/", want: "blocked non-public address"},
		{name: "rfc1918 literal", url: "http://192.168.1.1/", want: "blocked non-public address"},
		{name: "ipv6 loopback", url: "http://[::1]/", want: "blocked non-public address"},
		{name: "localhost name", url: "http://localhost:8080/", want: "blocked internal hostname"},
		{name: "private via dns", url: "http://internal.test/", want: "blocked non-public address"},
		{name: "non-http scheme", url: "file:///etc/passwd", want: "must use http or https"},
		{name: "unresolvable host", url: "http://nowhere.test/", want: "resolve nowhere.test"},
		{name: "redirect to loopback", url: "https://public.test/to-loopback", want: "blocked non-public address", wantRequests: 1},
		{name: "redirect loop", url: "https://public.test/loop", want: "too many redirects", wantRequests: webMaxRedirects + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests = 0
			_, err := tool.Execute(context.Background(), "fetch", map[string]any{"url": test.url}, nil)
			require(t, err != nil && strings.Contains(err.Error(), test.want), "error = %v, want %q", err, test.want)
			require(t, requests == test.wantRequests, "issued %d requests, want %d", requests, test.wantRequests)
		})
	}
	_ = must(tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/ok"}, nil))
}

func TestFetchContentDecodesCharsetAndRejectsBinary(t *testing.T) {
	stubDNS(t, map[string]string{"public.test": "93.184.216.34"})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/binary" {
			return response(http.StatusOK, "image/png", "\x89PNG\r\n\x1a\n\xff\xfe"), nil
		}
		if request.URL.Path == "/sjis" {
			return response(http.StatusOK, "text/html; charset=shift_jis", "<p>hello \x82\xb1\x82\xf1\x82\xc9\x82\xbf\x82\xcd</p>"), nil
		}
		// 0x92 is a right single quote in windows-1252 and invalid UTF-8.
		return response(http.StatusOK, "text/html; charset=windows-1252", "<p>caf\xe9 owner\x92s</p>"), nil
	})}
	tool := pluginTool(t, "websearch", "fetch_content", Extension(client), extensions.RunnerOptions{})
	result := must(tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/legacy"}, nil))
	got := ai.ContentText(result.Content)
	require(t, got == "café owner’s" && utf8.ValidString(got), "decoded = %q", got)
	result = must(tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/sjis"}, nil))
	got = ai.ContentText(result.Content)
	require(t, got == "hello こんにちは" && utf8.ValidString(got), "decoded = %q", got)
	_, err := tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/binary"}, nil)
	require(t, err != nil && strings.Contains(err.Error(), "unsupported content type"), "binary error = %v", err)
}

func TestWebSearchDropsProviderErrorBody(t *testing.T) {
	setHome(t, t.TempDir())
	for _, key := range []string{"BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("EXA_API_KEY", "sk-SECRET")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusUnauthorized, "application/json", `{"error":"invalid key sk-SECRET"}`), nil
	})}
	tool := pluginTool(t, "websearch", "web_search", Extension(client), extensions.RunnerOptions{})
	_, err := tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil)
	require(t, err != nil && !strings.Contains(err.Error(), "sk-SECRET"), "error leaked the provider body: %v", err)
}

func TestWebSearchHonoursConfiguredProvider(t *testing.T) {
	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	setHome(t, home)
	mustOK(os.MkdirAll(filepath.Join(home, ".pi"), 0o755))
	config := `{"provider":"brave","exaApiKey":"exa-key","braveApiKey":"brave-key"}`
	mustOK(os.WriteFile(filepath.Join(home, ".pi", "web-search.json"), []byte(config), 0o600))
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require(t, strings.Contains(request.URL.Host, "brave"), "provider ignored: %s", request.URL)
		return response(http.StatusOK, "application/json", `{"web":{"results":[]}}`), nil
	})}
	tool := pluginTool(t, "websearch", "web_search", Extension(client), extensions.RunnerOptions{})
	_ = must(tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil))
}

func TestWebSearchWithoutKeyReturnsActionableError(t *testing.T) {
	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	setHome(t, t.TempDir())
	tool := pluginTool(t, "websearch", "web_search", Extension(nil), extensions.RunnerOptions{})
	_, err := tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil)
	require(t, err != nil && strings.Contains(err.Error(), "EXA_API_KEY") && strings.Contains(err.Error(), "~/.pi/web-search.json"), "error = %v", err)
}

func TestWebSearchReadsPiWebSearchConfig(t *testing.T) {
	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	setHome(t, home)
	mustOK(os.MkdirAll(filepath.Join(home, ".pi"), 0o755))
	mustOK(os.WriteFile(filepath.Join(home, ".pi", "web-search.json"), []byte(`{"exaApiKey":"stored"}`), 0o600))
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require(t, request.Header.Get("x-api-key") == "stored", "api key = %q", request.Header.Get("x-api-key"))
		return response(http.StatusOK, "application/json", `{"results":[]}`), nil
	})}
	tool := pluginTool(t, "websearch", "web_search", Extension(client), extensions.RunnerOptions{})
	_ = must(tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil))
}

func pluginTool(t *testing.T, plugin, tool string, factory extensions.Factory, runnerOptions extensions.RunnerOptions) engine.AgentTool {
	t.Helper()
	registry := extensions.NewRegistry(t.TempDir())
	if factory == nil {
		t.Fatalf("plugin %q missing", plugin)
	}
	mustOK(registry.Register("<inline:"+plugin+">", factory))
	manager := must(sessionstore.InMemory(t.TempDir()))
	runnerOptions.SessionManager = manager
	runnerOptions.Actions.GetActiveTools = func() ([]string, error) { return []string{tool}, nil }
	runner := extensions.NewRunner(registry, runnerOptions)
	for _, registered := range runner.AllRegisteredTools() {
		if registered.Definition.Name == tool {
			return extensions.WrapRegisteredTool(registered, runner)
		}
	}
	t.Fatalf("tool %q missing", tool)
	return nil
}

func response(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func redirect(location string) *http.Response {
	return &http.Response{StatusCode: http.StatusFound, Status: http.StatusText(http.StatusFound), Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader(""))}
}

// setHome points the home directory at dir on every platform: Go's
// os.UserHomeDir reads USERPROFILE on Windows and HOME elsewhere.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}
