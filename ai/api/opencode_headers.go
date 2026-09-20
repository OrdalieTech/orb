package api

import (
	"net/http"
	"strings"

	"github.com/OrdalieTech/orb/ai"
)

const openCodeSessionHeader = "x-opencode-session"

func addOpenCodeSessionHeader(headers http.Header, model *ai.Model, options *ai.StreamOptions) {
	if model == nil || (model.Provider != "opencode" && model.Provider != "opencode-go") ||
		options == nil || options.SessionID == nil || *options.SessionID == "" {
		return
	}
	for name := range options.Headers {
		if strings.EqualFold(name, openCodeSessionHeader) {
			return
		}
	}
	for name := range headers {
		if strings.EqualFold(name, openCodeSessionHeader) {
			return
		}
	}
	headers.Set(openCodeSessionHeader, *options.SessionID)
}
