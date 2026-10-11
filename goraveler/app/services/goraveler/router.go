package goraveler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/goravel/framework/contracts/ai"

	"goravel/app/facades"
)

const (
	agentReview    = "review"
	agentPlan      = "plan"
	agentImplement = "implement"
)

// supportedAgents is the single source of truth for the agent names: the
// parser's accepted set and the routing prompt are both derived from it.
var supportedAgents = []string{agentReview, agentPlan, agentImplement}

// aiRouter routes instructions to an agent using the configured AI provider.
type aiRouter struct{}

// newAIRouter returns the AI-backed agent router.
func newAIRouter() *aiRouter {
	return &aiRouter{}
}

// AgentFor asks the AI provider which agent should handle the instruction.
func (r *aiRouter) AgentFor(ctx context.Context, instruction string) (string, error) {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return "", errors.New("empty instruction")
	}

	conversation, err := facades.AI().WithContext(ctx).Agent(routerAgent{})
	if err != nil {
		return "", fmt.Errorf("create routing conversation: %w", err)
	}

	response, err := conversation.Prompt(instruction)
	if err != nil {
		return "", fmt.Errorf("route instruction: %w", err)
	}

	return parseAgent(response.Text())
}

// parseAgent maps the AI reply to one of the supported agent names, tolerating
// surrounding whitespace, punctuation and markdown decoration.
func parseAgent(reply string) (string, error) {
	normalized := strings.Trim(strings.ToLower(strings.TrimSpace(reply)), " \t\r\n.!?\"'`*():[],;")

	for _, agent := range supportedAgents {
		if normalized == agent {
			return agent, nil
		}
	}

	return "", fmt.Errorf("unrecognised agent reply %q", reply)
}

// routerAgent is the AI agent used to classify instructions.
type routerAgent struct{}

// Instructions returns the system prompt that constrains the reply to one of
// the supported agent names.
func (routerAgent) Instructions() string {
	return "You are a routing assistant. Given a user's instruction for a GitHub " +
		"repository, reply with exactly one word identifying the agent that should " +
		"handle it: " + strings.Join(supportedAgents, ", ") + ". Do not add any other text."
}

// Messages returns the prior conversation, which is empty for stateless routing.
func (routerAgent) Messages() []ai.Message { return nil }

// Middleware returns nil; routing applies no middleware.
func (routerAgent) Middleware() []ai.Middleware { return nil }

// Tools returns nil; routing exposes no tools to the model.
func (routerAgent) Tools() []ai.Tool { return nil }
