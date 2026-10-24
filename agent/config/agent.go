package config

import (
	"os"
	"path/filepath"

	"goravel/app/constants"
	"goravel/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("agent", map[string]any{
		// Full URL of the relay events endpoint.
		"base_url": config.Env("AGENT_BASE_URL", "http://localhost:4500/webhooks/events"),

		// Token sent as the X-Api-Token header on every relay request.
		"api_token": config.Env("AGENT_API_TOKEN", ""),

		// Directory holding repository checkouts; repositories are cloned here on
		// demand. Defaults to a `goravel-agent` directory under the system temp dir.
		"workspace": config.Env("AGENT_WORKSPACE", filepath.Join(os.TempDir(), "goravel-agent")),

		// Comma-separated GitHub senders allowed to trigger agent runs. Required:
		// the poller refuses to run when this is empty.
		"allowed_senders": config.Env("AGENT_ALLOWED_SENDERS", ""),

		// Handle that triggers an agent run, e.g. `@goravel-agent review`. Blank
		// falls back to the built-in default (`@goravel-agent`); a value without
		// the leading `@` is normalized.
		"mention": config.Env("AGENT_MENTION", ""),

		// GitHub token used to add the 👀 reaction to a mention comment. A
		// fine-grained token needs `issues: write` for issue and pull request
		// conversation comments and `pull-requests: write` for inline review
		// comments; a classic token needs `repo`. Blank disables reactions; the
		// poller still runs agents.
		"github_token": config.Env("GITHUB_TOKEN", ""),

		// Model each agent runs with, keyed by mode: `<model>[#<thinking>]`.
		// Every routable agent needs an entry.
		"models": map[string]string{
			constants.AgentShip:      "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentImplement: "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentReview:    "opencode-go/deepseek-v4.1-flash#high",
		},
	})
}
