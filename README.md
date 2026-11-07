# agents

Prompts and skills for the Goravel agent workflows.

- `agents/` — primary agents: `ship` (plan → code → review → PR), `review` (review a pull request), `analyze` (breaking changes, usage, Laravel comparison).
- `agents/subagents/` — workers a primary delegates to: `code`, `plan`, `setup`, `review-diff`, `review-go`, `pr`, `reaction`.
- `skills/` — shared skills: `goravel-planning`, `goravel-pr`, `goravel-pr-comments`, `goravel-review-template`, `goravel-testing`.

The names in backticks are load-bearing: a primary spawns a subagent, and a subagent loads a skill, by exact
id. A rename must update every caller, every permission block, and this list.
