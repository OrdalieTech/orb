package providers

import (
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
)

var githubCopilotProvider = Provider{
	ID:   "github-copilot",
	Name: "GitHub Copilot",
	Auth: AuthAPIKey,
	Methods: auth.ProviderAuth{
		APIKey: auth.EnvAPIKeyAuth{
			DisplayName: "GitHub Copilot token",
			EnvVars:     []string{"COPILOT_GITHUB_TOKEN"},
		},
		OAuth: oauth.NewGitHubCopilot(nil),
	},
}

func GitHubCopilot() Provider { return registered("github-copilot") }
