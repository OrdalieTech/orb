package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/internal/truncate"
)

const webBodyLimit = 2 << 20

var (
	webSearchSchema = ai.JSONSchema(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"}}}`)
	fetchSchema     = ai.JSONSchema(`{"type":"object","required":["url"],"properties":{"url":{"type":"string"}}}`)
	scriptPattern   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	stylePattern    = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	commentPattern  = regexp.MustCompile(`(?s)<!--.*?-->`)
	tagPattern      = regexp.MustCompile(`(?s)<[^>]*>`)
)

type webKeys struct {
	Exa    string `json:"exaApiKey"`
	Brave  string `json:"braveApiKey"`
	Tavily string `json:"tavilyApiKey"`
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
	switch {
	case keys.Exa != "":
		return searchExa(ctx, client, query, keys.Exa)
	case keys.Brave != "":
		return searchBrave(ctx, client, query, keys.Brave)
	case keys.Tavily != "":
		return searchTavily(ctx, client, query, keys.Tavily)
	default:
		return nil, fmt.Errorf("web_search: set EXA_API_KEY, BRAVE_API_KEY, or TAVILY_API_KEY, or add one to ~/.pi/web-search.json")
	}
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
		return fmt.Errorf("web_search: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("web_search: decode response: %w", err)
	}
	return nil
}

func fetchContent(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("fetch_content: url must use http or https")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "pigo/first-party-websearch")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch_content: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, webBodyLimit))
	if err != nil {
		return "", fmt.Errorf("fetch_content: read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("fetch_content: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "pdf") {
		return "", fmt.Errorf("fetch_content: PDF extraction is not supported")
	}
	if strings.Contains(contentType, "html") || bytes.Contains(bytes.ToLower(body), []byte("<html")) {
		return htmlText(string(body)), nil
	}
	return strings.TrimSpace(string(body)), nil
}

// ponytail: This intentionally handles HTML/text only; add MIME-specific PDF
// or media extractors when those formats are demanded by real usage.
func htmlText(source string) string {
	source = scriptPattern.ReplaceAllString(source, " ")
	source = stylePattern.ReplaceAllString(source, " ")
	source = commentPattern.ReplaceAllString(source, " ")
	source = tagPattern.ReplaceAllString(source, " ")
	return strings.Join(strings.Fields(html.UnescapeString(source)), " ")
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
	if result.Truncated {
		return result.Content + "\n\n[output truncated]"
	}
	return result.Content
}
