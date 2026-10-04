package providers

import (
	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/oauth"
)

var metaProvider = Provider{
	ID:   "meta",
	Name: "Meta",
	Auth: AuthAPIKey,
	Methods: auth.ProviderAuth{
		APIKey: auth.EnvAPIKeyAuth{DisplayName: "Meta Model API key", EnvVars: []string{"META_API_KEY"}},
		OAuth:  oauth.NewMeta(nil),
	},
}
