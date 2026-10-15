package config

import (
	"os"
	"path/filepath"

	"goravel/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("goraveler", map[string]any{
		// Full URL of the relay events endpoint.
		"base_url": config.Env("GORAVELER_BASE_URL", "http://localhost:4500/webhooks/events"),

		// Token sent as the X-Api-Token header on every relay request.
		"api_token": config.Env("GORAVELER_API_TOKEN", ""),

		// Directory holding repository checkouts; repositories are cloned here on
		// demand. Defaults to a `goraveler` directory under the system temp dir.
		"workspace": config.Env("GORAVELER_WORKSPACE", filepath.Join(os.TempDir(), "goraveler")),

		// Comma-separated GitHub senders allowed to trigger agent runs;
		// empty allows every sender.
		"allowed_senders": config.Env("GORAVELER_ALLOWED_SENDERS", ""),
	})
}
