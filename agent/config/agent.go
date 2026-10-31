package config

import (
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

		// Name of the paseo project every run workspace is created in; its folder
		// holds the empty per-run directories. The run's `setup` subagent clones
		// the repository inside one and creates the run's worktree. The id is
		// resolved lazily with `paseo project ls --json` on the first mention that
		// needs a workspace. There is no default: a blank value is a configuration
		// error the poller refuses to start with.
		"project_name": config.Env("AGENT_PROJECT_NAME", ""),

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

		// Clone settings for the run's `setup` subagent, passed to `paseo run` as
		// GITHUB_CLONE_PREFIX and CLONE_DEPTH: paseo starts it with a scrubbed
		// environment, so these are the only way the worker gets them. SSH at depth
		// 1 keeps a run to one tip commit; a blank depth means a full clone.
		"clone_prefix": config.Env("AGENT_CLONE_PREFIX", "git@github.com:"),
		"clone_depth":  config.Env("AGENT_CLONE_DEPTH", "1"),

		// Model each agent runs with, keyed by mode: `<model>[#<thinking>]`.
		// Every routable agent needs an entry.
		"models": map[string]string{
			constants.AgentShip:    "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentReview:  "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentAnalyze: "opencode-go/deepseek-v4.1-flash#high",
		},
	})
}
