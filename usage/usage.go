package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
)

var ErrUnavailable = errors.New("usage unavailable")

type Window struct {
	Name      string
	Remaining float64
	ResetsAt  time.Time
}
type Snapshot struct {
	Plan      string
	Windows   []Window
	CheckedAt time.Time
}

// Client reads provider-reported quota only. URLs are injectable for tests
// and private deployments; credentials never follow redirects.
type Client struct {
	HTTPClient              *http.Client
	CodexURL, OpenCodeGoURL string
}

func (c Client) Fetch(ctx context.Context, provider string, credential auth.ModelAuth) (Snapshot, error) {
	endpoint := ""
	switch provider {
	case "openai-codex":
		endpoint = c.CodexURL
		if endpoint == "" {
			endpoint = "https://chatgpt.com/backend-api/wham/usage"
		}
	case "opencode-go":
		endpoint = c.OpenCodeGoURL
		if endpoint == "" {
			endpoint = "https://opencode.ai/zen/go/v1/usage"
		}
	default:
		return Snapshot{}, ErrUnavailable
	}
	if credential.APIKey == nil || *credential.APIKey == "" {
		return Snapshot{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Snapshot{}, errors.New("invalid usage endpoint")
	}
	request.Header.Set("Authorization", "Bearer "+*credential.APIKey)
	request.Header.Set("Accept", "application/json")
	if provider == "openai-codex" {
		parts := strings.Split(*credential.APIKey, ".")
		if len(parts) == 3 {
			data, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var token struct {
				Auth struct {
					Account string `json:"chatgpt_account_id"`
				} `json:"https://api.openai.com/auth"`
			}
			if json.Unmarshal(data, &token) == nil && token.Auth.Account != "" {
				request.Header.Set("ChatGPT-Account-Id", token.Auth.Account)
			}
		}
	}
	client := http.Client{}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{}, errors.New("usage request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("usage unavailable (HTTP %d)", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return Snapshot{}, errors.New("invalid usage response")
	}
	usage := Snapshot{CheckedAt: time.Now()}
	if provider == "openai-codex" {
		var result struct {
			Plan      string `json:"plan_type"`
			RateLimit struct {
				Primary   *codexWindow `json:"primary_window"`
				Secondary *codexWindow `json:"secondary_window"`
			} `json:"rate_limit"`
		}
		if json.Unmarshal(data, &result) != nil {
			return Snapshot{}, ErrUnavailable
		}
		usage.Plan = result.Plan
		for _, window := range []*codexWindow{result.RateLimit.Primary, result.RateLimit.Secondary} {
			if window == nil || window.Used == nil || window.Seconds <= 0 || window.Reset <= 0 {
				continue
			}
			duration := time.Duration(window.Seconds) * time.Second
			name := duration.String()
			if duration%(24*time.Hour) == 0 {
				name = fmt.Sprintf("%dd", window.Seconds/86400)
			} else if duration%time.Hour == 0 {
				name = fmt.Sprintf("%dh", window.Seconds/3600)
			}
			usage.Windows = append(usage.Windows, Window{Name: name, Remaining: max(0, min(100, 100-*window.Used)), ResetsAt: time.Unix(window.Reset, 0)})
		}
	} else {
		var result struct {
			Usage map[string]struct {
				Percent  *float64  `json:"percent"`
				ResetsAt time.Time `json:"resetsAt"`
			} `json:"usage"`
		}
		if json.Unmarshal(data, &result) != nil {
			return Snapshot{}, ErrUnavailable
		}
		for _, name := range []string{"rolling", "weekly", "monthly"} {
			window, ok := result.Usage[name]
			if !ok || window.Percent == nil || window.ResetsAt.IsZero() {
				continue
			}
			label := map[string]string{"rolling": "rolling", "weekly": "7d", "monthly": "month"}[name]
			usage.Windows = append(usage.Windows, Window{Name: label, Remaining: max(0, min(100, 100-*window.Percent)), ResetsAt: window.ResetsAt})
		}
	}
	if len(usage.Windows) == 0 {
		return Snapshot{}, ErrUnavailable
	}
	return usage, nil
}

type codexWindow struct {
	Used    *float64 `json:"used_percent"`
	Seconds int64    `json:"limit_window_seconds"`
	Reset   int64    `json:"reset_at"`
}

func (u Snapshot) Summary() string {
	parts := make([]string, 0, len(u.Windows))
	for _, window := range u.Windows {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", window.Name, window.Remaining))
	}
	return strings.Join(parts, " · ") + " left"
}
