package goraveler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/goravel/framework/contracts/ai"
	"github.com/goravel/framework/contracts/process"

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

const (
	// opencodeBin is the CLI used to run agents.
	opencodeBin = "opencode"
	// opencodeTimeout caps a single agent run.
	opencodeTimeout = 30 * time.Minute
	// stderrLimit bounds how much agent stderr is kept for the error message.
	stderrLimit = 64 * 1024
)

// dispatchRequest is one agent invocation: which directory to run in, which
// agent, and the prompt.
type dispatchRequest struct {
	dir    string
	agent  string
	prompt string
}

// opencodeDispatcher runs agents through the opencode CLI.
type opencodeDispatcher struct {
	bin      string
	deadline time.Duration
}

// newOpencodeDispatcher returns a dispatcher using the hardcoded binary and
// timeout constants.
func newOpencodeDispatcher() *opencodeDispatcher {
	return &opencodeDispatcher{
		bin:      opencodeBin,
		deadline: opencodeTimeout,
	}
}

// Run executes `opencode run --agent <agent> -- <prompt>` in the request's
// directory with a scrubbed environment. The agent is steered by untrusted
// comment text, so it only receives PATH and HOME; the poller's secrets
// (GORAVELER_API_TOKEN, OPENAI_*, database credentials, ...) are not inherited.
// Output is not buffered in full: only a bounded prefix of stderr is captured
// (and later escaped) for the error message, and the live stream is suppressed.
func (d *opencodeDispatcher) Run(ctx context.Context, req dispatchRequest) error {
	var (
		mu     sync.Mutex
		stderr []byte
	)
	capture := func(typ process.OutputType, line []byte) {
		if typ != process.OutputTypeStderr {
			return
		}

		mu.Lock()
		defer mu.Unlock()
		if remaining := stderrLimit - len(stderr); remaining > 0 {
			if len(line) > remaining {
				line = line[:remaining]
			}
			stderr = append(stderr, line...)
			stderr = append(stderr, '\n')
		}
	}

	result := facades.Process().
		WithContext(ctx).
		Path(req.dir).
		Timeout(d.deadline).
		DisableBuffering().
		Quietly().
		OnOutput(capture).
		Run("env", "-i",
			"PATH="+os.Getenv("PATH"),
			"HOME="+os.Getenv("HOME"),
			d.bin, "run", "--agent", req.agent, "--", req.prompt)

	if result.Failed() {
		mu.Lock()
		out := strings.TrimSpace(string(stderr))
		mu.Unlock()

		// Prefer the captured stderr, escaped so untrusted agent output cannot
		// forge multi-line log entries; fall back to the start error.
		if out != "" {
			return fmt.Errorf("opencode run failed (exit %d): %q", result.ExitCode(), out)
		}

		return fmt.Errorf("opencode run failed (exit %d): %w", result.ExitCode(), result.Error())
	}

	return nil
}
