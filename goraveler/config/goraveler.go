package config

import "goravel/app/facades"

func init() {
	config := facades.Config()
	config.Add("goraveler", map[string]any{
		// Base URL of the relay that stores platform events.
		"base_url": config.Env("GORAVELER_BASE_URL", "http://localhost:4500"),

		// Token sent as the X-Api-Token header on every relay request.
		"api_token": config.Env("GORAVELER_API_TOKEN", ""),

		// Directory holding repository checkouts; a leading `~` is expanded.
		"workspace": config.Env("GORAVELER_WORKSPACE", "~/code"),

		// Path of the events endpoint on the relay.
		"events_path": config.Env("GORAVELER_EVENTS_PATH", "/webhooks/events"),

		// Comma-separated GitHub senders allowed to trigger agent runs;
		// empty allows every sender.
		"allowed_senders": config.Env("GORAVELER_ALLOWED_SENDERS", ""),
	})
}
