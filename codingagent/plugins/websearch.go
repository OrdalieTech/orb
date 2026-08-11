package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/codingagent/extensions"
	"github.com/OrdalieTech/orb/internal/truncate"
)

const (
	webBodyLimit    = 2 << 20
	webMaxRedirects = 5
	webErrorSnippet = 200
)

var (
	webSearchSchema = ai.JSONSchema(`{"type":"object","required":["query"],"properties":{"query":{"type":"string","description":"Search terms sent verbatim to the configured provider."}}}`)
	fetchSchema     = ai.JSONSchema(`{"type":"object","required":["url"],"properties":{"url":{"type":"string","description":"Absolute http(s) URL of a publicly reachable page. Loopback, link-local, and private addresses are refused."}}}`)
	scriptPattern   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`) })
	stylePattern    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`) })
	commentPattern  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)<!--.*?-->`) })
	blockPattern    = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?i)</?(?:address|article|aside|blockquote|br|dd|div|dl|dt|fieldset|figcaption|figure|footer|form|h[1-6]|header|hr|li|main|nav|ol|p|pre|section|table|tbody|td|tfoot|th|thead|tr|ul)\b[^>]*>`)
	})
	tagPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s)<[^>]*>`) })
)

type webKeys struct {
	Provider string `json:"provider"`
	Exa      string `json:"exaApiKey"`
	Brave    string `json:"braveApiKey"`
	Tavily   string `json:"tavilyApiKey"`
}

type searchResult struct{ title, url, snippet string }

func websearchExtension(client *http.Client) extensions.Factory {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return func(api extensions.API) error {
		api.RegisterTool(extensions.ToolDefinition{
			Name: "web_search", Label: "Web Search", Description: "Search the web with Exa, Brave, or Tavily", Parameters: webSearchSchema,
			Execute: func(ctx context.Context, _ string, raw any, _ agent.AgentToolUpdateCallback, _ extensions.Context) (agent.AgentToolResult, error) {
				var input struct {
					Query string `json:"query"`
				}
				if err := decode(raw, &input); err != nil {
					return agent.AgentToolResult{}, err
				}
				input.Query = strings.TrimSpace(input.Query)
				if input.Query == "" {
					return agent.AgentToolResult{}, fmt.Errorf("web_search: query is required")
				}
				results, err := searchWeb(ctx, client, input.Query)
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				return textResult(truncateWeb(formatSearchResults(results))), nil
			},
		})
		api.RegisterTool(extensions.ToolDefinition{
			Name: "fetch_content", Label: "Fetch Content", Description: "Fetch an HTTP page as readable text", Parameters: fetchSchema,
			Execute: func(ctx context.Context, _ string, raw any, _ agent.AgentToolUpdateCallback, _ extensions.Context) (agent.AgentToolResult, error) {
				var input struct {
					URL string `json:"url"`
				}
				if err := decode(raw, &input); err != nil {
					return agent.AgentToolResult{}, err
				}
				text, err := fetchContent(ctx, client, strings.TrimSpace(input.URL))
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				return textResult(truncateWeb(text)), nil
			},
		})
		return nil
	}
}

func loadWebKeys() (webKeys, error) {
	keys := webKeys{Exa: strings.TrimSpace(os.Getenv("EXA_API_KEY")), Brave: strings.TrimSpace(os.Getenv("BRAVE_API_KEY")), Tavily: strings.TrimSpace(os.Getenv("TAVILY_API_KEY"))}
	home, err := os.UserHomeDir()
	if err != nil {
		return keys, nil
	}
	contents, err := os.ReadFile(filepath.Join(home, ".pi", "web-search.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return keys, nil
		}
		return webKeys{}, fmt.Errorf("web_search: read ~/.pi/web-search.json: %w", err)
	}
	var stored webKeys
	if err := json.Unmarshal(contents, &stored); err != nil {
		return webKeys{}, fmt.Errorf("web_search: parse ~/.pi/web-search.json: %w", err)
	}
	keys.Provider = strings.ToLower(strings.TrimSpace(stored.Provider))
	if keys.Exa == "" {
		keys.Exa = strings.TrimSpace(stored.Exa)
	}
	if keys.Brave == "" {
		keys.Brave = strings.TrimSpace(stored.Brave)
	}
	if keys.Tavily == "" {
		keys.Tavily = strings.TrimSpace(stored.Tavily)
	}
	return keys, nil
}

func searchWeb(ctx context.Context, client *http.Client, query string) ([]searchResult, error) {
	keys, err := loadWebKeys()
	if err != nil {
		return nil, err
	}
	backends := []struct {
		name, key string
		search    func(context.Context, *http.Client, string, string) ([]searchResult, error)
	}{
		{"exa", keys.Exa, searchExa},
		{"brave", keys.Brave, searchBrave},
		{"tavily", keys.Tavily, searchTavily},
	}
	for _, backend := range backends {
		if keys.Provider != "" && keys.Provider != backend.name {
			continue
		}
		if backend.key == "" {
			if keys.Provider == "" {
				continue
			}
			return nil, fmt.Errorf("web_search: ~/.pi/web-search.json selects provider %q but no %s key is set", keys.Provider, backend.name)
		}
		return backend.search(ctx, client, query, backend.key)
	}
	if keys.Provider != "" {
		return nil, fmt.Errorf("web_search: unknown provider %q in ~/.pi/web-search.json", keys.Provider)
	}
	return nil, fmt.Errorf("web_search: set EXA_API_KEY, BRAVE_API_KEY, or TAVILY_API_KEY, or add one to ~/.pi/web-search.json")
}

func searchExa(ctx context.Context, client *http.Client, query, key string) ([]searchResult, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "numResults": 8, "contents": map[string]any{"highlights": true}})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.exa.ai/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", key)
	var response struct {
		Results []struct {
			Title, URL, Text, Summary string
			Highlights                []string
		} `json:"results"`
	}
	if err := fetchJSON(client, request, &response); err != nil {
		return nil, err
	}
	results := make([]searchResult, 0, len(response.Results))
	for _, item := range response.Results {
		snippet := item.Text
		if len(item.Highlights) > 0 {
			snippet = strings.Join(item.Highlights, " … ")
		} else if snippet == "" {
			snippet = item.Summary
		}
		results = append(results, searchResult{item.Title, item.URL, snippet})
	}
	return results, nil
}

func searchBrave(ctx context.Context, client *http.Client, query, key string) ([]searchResult, error) {
	endpoint := "https://api.search.brave.com/res/v1/web/search?q=" + url.QueryEscape(query) + "&count=8"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Subscription-Token", key)
	var response struct {
		Web struct {
			Results []struct{ Title, URL, Description string } `json:"results"`
		} `json:"web"`
	}
	if err := fetchJSON(client, request, &response); err != nil {
		return nil, err
	}
	results := make([]searchResult, 0, len(response.Web.Results))
	for _, item := range response.Web.Results {
		results = append(results, searchResult{item.Title, item.URL, item.Description})
	}
	return results, nil
}

func searchTavily(ctx context.Context, client *http.Client, query, key string) ([]searchResult, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "search_depth": "basic", "max_results": 8})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	var response struct {
		Results []struct{ Title, URL, Content string } `json:"results"`
	}
	if err := fetchJSON(client, request, &response); err != nil {
		return nil, err
	}
	results := make([]searchResult, 0, len(response.Results))
	for _, item := range response.Results {
		results = append(results, searchResult{item.Title, item.URL, item.Content})
	}
	return results, nil
}

func fetchJSON(client *http.Client, request *http.Request, target any) error {
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("web_search: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, webBodyLimit))
	if err != nil {
		return fmt.Errorf("web_search: read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// The provider body is dropped, never summarised: 401 bodies echo the
		// submitted API key, and this error reaches the model verbatim.
		return fmt.Errorf("web_search: %s (provider body omitted: it can contain the API key)", response.Status)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("web_search: decode response: %w", err)
	}
	return nil
}

func fetchContent(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	target, err := validateRemoteURL(ctx, rawURL)
	if err != nil {
		return "", err
	}
	// Redirects are followed by hand so every hop is re-validated; the shared
	// client would otherwise chase a public URL straight into the metadata service.
	manual := *client
	manual.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for redirects := 0; ; redirects++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return "", err
		}
		request.Header.Set("User-Agent", "orb/first-party-websearch")
		response, err := manual.Do(request)
		if err != nil {
			return "", fmt.Errorf("fetch_content: %w", err)
		}
		location := response.Header.Get("Location")
		if !isRedirect(response.StatusCode) || location == "" {
			return readFetched(response)
		}
		_ = response.Body.Close()
		if redirects == webMaxRedirects {
			return "", fmt.Errorf("fetch_content: too many redirects")
		}
		next, err := target.Parse(location)
		if err != nil {
			return "", fmt.Errorf("fetch_content: invalid redirect target %q", location)
		}
		if target, err = validateRemoteURL(ctx, next.String()); err != nil {
			return "", err
		}
	}
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// ponytail: one process-wide resolver seam so tests can pin hostnames; give
// each client its own *net.Resolver only if split-horizon DNS shows up.
var lookupIP = net.DefaultResolver.LookupIPAddr

// validateRemoteURL mirrors pi-web-access/ssrf-protection.ts: http(s) only, and
// every resolved address must be public. Callers must re-run it per redirect.
func validateRemoteURL(ctx context.Context, rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("fetch_content: url must use http or https")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return nil, fmt.Errorf("fetch_content: url must include a hostname")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("fetch_content: blocked internal hostname %q", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		return parsed, publicAddress(ip, host)
	}
	addresses, err := lookupIP(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("fetch_content: resolve %s: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("fetch_content: resolve %s: no addresses returned", host)
	}
	for _, address := range addresses {
		if err := publicAddress(address.IP, host); err != nil {
			return nil, err
		}
	}
	return parsed, nil
}

// ponytail: the transport re-resolves after this check, so a rebinding DNS
// server can still swing a second lookup inward; pin the dial to the address
// validated here if untrusted URLs ever reach a network with real secrets.
func publicAddress(ip net.IP, host string) error {
	blocked := ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast()
	if v4 := ip.To4(); v4 != nil && !blocked {
		// Ranges no net.IP helper covers: "this network", CGNAT, benchmarking, reserved.
		blocked = v4[0] == 0 || (v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127) ||
			(v4[0] == 198 && (v4[1] == 18 || v4[1] == 19)) || v4[0] >= 240
	}
	if blocked {
		return fmt.Errorf("fetch_content: blocked non-public address %s for %s", ip, host)
	}
	return nil
}

// ponytail: HTML and text only; add MIME-specific PDF or media extractors when
// those formats are demanded by real usage.
func readFetched(response *http.Response) (string, error) {
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, webBodyLimit))
	if err != nil {
		return "", fmt.Errorf("fetch_content: read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("fetch_content: %s: %s", response.Status,
			trimTrailingBytes(strings.TrimSpace(string(body)), webErrorSnippet))
	}
	mediaType, charset := parseContentType(response.Header.Get("Content-Type"))
	looksHTML := bytes.Contains(bytes.ToLower(body), []byte("<html"))
	switch {
	case strings.Contains(mediaType, "pdf"):
		return "", fmt.Errorf("fetch_content: PDF extraction is not supported")
	case !textualMedia(mediaType) && !looksHTML:
		return "", fmt.Errorf("fetch_content: unsupported content type %q", mediaType)
	}
	text := decodeCharset(body, charset)
	if strings.Contains(mediaType, "html") || looksHTML {
		return htmlText(text), nil
	}
	return strings.TrimSpace(text), nil
}

func parseContentType(header string) (string, string) {
	mediaType, parameters, err := mime.ParseMediaType(header)
	if err != nil {
		mediaType, _, _ = strings.Cut(header, ";")
	}
	return strings.ToLower(strings.TrimSpace(mediaType)), parameters["charset"]
}

func textualMedia(mediaType string) bool {
	switch {
	case mediaType == "", strings.HasPrefix(mediaType, "text/"),
		strings.HasSuffix(mediaType, "+json"), strings.HasSuffix(mediaType, "+xml"):
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript", "application/x-javascript":
		return true
	}
	return false
}

// webCharsetEncodings maps WHATWG single-byte labels and UTF-16 to decoders.
// htmlindex is deliberately avoided: it links ~560KB of CJK legacy-charset
// tables. Absent labels (all CJK charsets included) fall through to the
// invalid-UTF-8 scrub floor in decodeCharset.
var webCharsetEncodings = sync.OnceValue(func() map[string]encoding.Encoding {
	table := make(map[string]encoding.Encoding, 160)
	add := func(enc encoding.Encoding, labels ...string) {
		for _, label := range labels {
			table[label] = enc
		}
	}
	add(charmap.CodePage866, "866", "cp866", "csibm866", "ibm866")
	add(charmap.ISO8859_2, "csisolatin2", "iso-8859-2", "iso-ir-101", "iso8859-2", "iso88592", "iso_8859-2", "iso_8859-2:1987", "l2", "latin2")
	add(charmap.ISO8859_3, "csisolatin3", "iso-8859-3", "iso-ir-109", "iso8859-3", "iso88593", "iso_8859-3", "iso_8859-3:1988", "l3", "latin3")
	add(charmap.ISO8859_4, "csisolatin4", "iso-8859-4", "iso-ir-110", "iso8859-4", "iso88594", "iso_8859-4", "iso_8859-4:1988", "l4", "latin4")
	add(charmap.ISO8859_5, "csisolatincyrillic", "cyrillic", "iso-8859-5", "iso-ir-144", "iso8859-5", "iso88595", "iso_8859-5", "iso_8859-5:1988")
	add(charmap.ISO8859_6, "arabic", "asmo-708", "csiso88596e", "csiso88596i", "csisolatinarabic", "ecma-114", "iso-8859-6", "iso-8859-6-e", "iso-8859-6-i", "iso-ir-127", "iso8859-6", "iso88596", "iso_8859-6", "iso_8859-6:1987")
	add(charmap.ISO8859_7, "csisolatingreek", "ecma-118", "elot_928", "greek", "greek8", "iso-8859-7", "iso-ir-126", "iso8859-7", "iso88597", "iso_8859-7", "iso_8859-7:1987", "sun_eu_greek")
	add(charmap.ISO8859_8, "csiso88598e", "csisolatinhebrew", "hebrew", "iso-8859-8", "iso-8859-8-e", "iso-ir-138", "iso8859-8", "iso88598", "iso_8859-8", "iso_8859-8:1988", "visual")
	add(charmap.ISO8859_8I, "csiso88598i", "iso-8859-8-i", "logical")
	add(charmap.ISO8859_10, "csisolatin6", "iso-8859-10", "iso-ir-157", "iso8859-10", "iso885910", "l6", "latin6")
	add(charmap.ISO8859_13, "iso-8859-13", "iso8859-13", "iso885913")
	add(charmap.ISO8859_14, "iso-8859-14", "iso8859-14", "iso885914")
	add(charmap.ISO8859_15, "csisolatin9", "iso-8859-15", "iso8859-15", "iso885915", "iso_8859-15", "l9")
	add(charmap.ISO8859_16, "iso-8859-16")
	add(charmap.KOI8R, "cskoi8r", "koi", "koi8", "koi8-r", "koi8_r")
	add(charmap.KOI8U, "koi8-ru", "koi8-u")
	add(charmap.Macintosh, "csmacintosh", "mac", "macintosh", "x-mac-roman")
	add(charmap.Windows874, "dos-874", "iso-8859-11", "iso8859-11", "iso885911", "tis-620", "windows-874")
	add(charmap.Windows1250, "cp1250", "windows-1250", "x-cp1250")
	add(charmap.Windows1251, "cp1251", "windows-1251", "x-cp1251")
	add(charmap.Windows1252, "ansi_x3.4-1968", "ascii", "cp1252", "cp819", "csisolatin1", "ibm819", "iso-8859-1", "iso-ir-100", "iso8859-1", "iso88591", "iso_8859-1", "iso_8859-1:1987", "l1", "latin1", "us-ascii", "windows-1252", "x-cp1252")
	add(charmap.Windows1253, "cp1253", "windows-1253", "x-cp1253")
	add(charmap.Windows1254, "cp1254", "csisolatin5", "iso-8859-9", "iso-ir-148", "iso8859-9", "iso88599", "iso_8859-9", "iso_8859-9:1989", "l5", "latin5", "windows-1254", "x-cp1254")
	add(charmap.Windows1255, "cp1255", "windows-1255", "x-cp1255")
	add(charmap.Windows1256, "cp1256", "windows-1256", "x-cp1256")
	add(charmap.Windows1257, "cp1257", "windows-1257", "x-cp1257")
	add(charmap.Windows1258, "cp1258", "windows-1258", "x-cp1258")
	add(charmap.XUserDefined, "x-user-defined")
	add(unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM), "unicodefffe", "utf-16be")
	add(unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM), "csunicode", "iso-10646-ucs-2", "ucs-2", "unicode", "unicodefeff", "utf-16", "utf-16le")
	return table
})

// ponytail: charset comes from the Content-Type header only, with an invalid-UTF-8
// scrub as the floor; read <meta charset> too if mislabelled pages show up.
func decodeCharset(body []byte, charset string) string {
	if charset != "" && !strings.EqualFold(charset, "utf-8") {
		if enc := webCharsetEncodings()[strings.ToLower(strings.TrimSpace(charset))]; enc != nil {
			if decoded, _, err := transform.Bytes(enc.NewDecoder(), body); err == nil {
				body = decoded
			}
		}
	}
	return strings.ToValidUTF8(string(body), string(utf8.RuneError))
}

func htmlText(source string) string {
	source = scriptPattern().ReplaceAllString(source, " ")
	source = stylePattern().ReplaceAllString(source, " ")
	source = commentPattern().ReplaceAllString(source, " ")
	// Block tags become newlines before the rest are dropped: a page collapsed
	// onto one line is truncated to nothing once it passes the byte cap.
	source = blockPattern().ReplaceAllString(source, "\n")
	source = tagPattern().ReplaceAllString(source, " ")
	lines := strings.Split(html.UnescapeString(source), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func formatSearchResults(results []searchResult) string {
	if len(results) == 0 {
		return "No results."
	}
	formatted := make([]string, 0, len(results))
	for _, result := range results {
		lines := []string{strings.TrimSpace(result.title), strings.TrimSpace(result.url)}
		if snippet := strings.TrimSpace(result.snippet); snippet != "" {
			lines = append(lines, snippet)
		}
		formatted = append(formatted, strings.Join(lines, "\n"))
	}
	return strings.Join(formatted, "\n\n")
}

func truncateWeb(text string) string {
	result := truncate.TruncateHead(text)
	if !result.Truncated {
		return result.Content
	}
	content := result.Content
	if result.FirstLineExceedsLimit {
		// TruncateHead returns nothing when line 1 alone busts the byte cap
		// (faithful to upstream). A genuinely unbreakable line still has to
		// yield its head rather than a bare marker.
		content = trimTrailingBytes(text, result.MaxBytes)
	}
	return content + "\n\n[output truncated]"
}

func trimTrailingBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}
