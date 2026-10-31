# agents

Prompts and skills for the Goravel agent workflows.

- `agents/` — primary agents: `ship`, `review`, `analyze`.
- `agents/subagents/` — workers a primary delegates to: `code`, `plan`, `review-diff`, `go-review`, `pr`, `reaction`.
- `skills/` — shared skills: `goravel-planning`, `goravel-pr`, `goravel-pr-comments`, `goravel-review-template`, `goravel-testing`.
- `agent/` — a small Goravel app that polls GitHub mentions, routes each to a mode, and dispatches it through paseo. Its project-local `goravel-development` skill lives in `agent/.agents/skills/`.
