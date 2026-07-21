package codingagent

import (
	"context"
	"net/url"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

// Session-affinity headers from upstream core/provider-attribution.ts. The
// telemetry-gated attribution headers (OpenRouter/NVIDIA/Cloudflare) were
// removed with telemetry per the DECISIONS divergence ledger; the opencode
// session headers are unconditional upstream and are kept.

const opencodeHost = "opencode.ai"

func matchesHost(baseURL, expectedHost string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return parsed.Hostname() == expectedHost
}

// opencodeSessionHeaders mirrors upstream getSessionHeaders: opencode models
// carry the session id and client name on every request.
func opencodeSessionHeaders(model *ai.Model, sessionID string) map[string]string {
	if model == nil || sessionID == "" {
		return nil
	}
	if model.Provider != "opencode" && model.Provider != "opencode-go" && !matchesHost(model.BaseURL, opencodeHost) {
		return nil
	}
	return map[string]string{"x-opencode-session": sessionID, "x-opencode-client": "pi"}
}

// withSessionAffinityHeaders layers the session headers under the configured
// model-header resolver. Resolver headers win on collision, mirroring
// upstream mergeProviderAttributionHeaders' spread order (session headers
// first, request headers assigned over them).
func withSessionAffinityHeaders(base agent.GetModelHeadersFunc, manager *sessionstore.SessionManager) agent.GetModelHeadersFunc {
	return func(ctx context.Context, model *ai.Model, apiKey *string, env ai.ProviderEnv) (*map[string]string, error) {
		var resolved *map[string]string
		if base != nil {
			var err error
			resolved, err = base(ctx, model, apiKey, env)
			if err != nil {
				return nil, err
			}
		}
		session := opencodeSessionHeaders(model, manager.GetSessionID())
		if session == nil {
			return resolved, nil
		}
		merged := make(map[string]string, len(session)+2)
		for name, value := range session {
			merged[name] = value
		}
		if resolved != nil {
			for name, value := range *resolved {
				merged[name] = value
			}
		}
		return &merged, nil
	}
}
