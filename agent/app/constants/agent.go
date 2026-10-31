// Package constants holds values shared by the application configuration and
// the services that consume it.
package constants

// Agent names route a mention to a paseo mode. Each name is also a key in
// agent.models and the value passed to `paseo run --mode`.
const (
	AgentShip    = "ship"
	AgentReview  = "review"
	AgentAnalyze = "analyze"
)
