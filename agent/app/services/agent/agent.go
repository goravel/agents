package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goravel/framework/contracts/ai"
	"github.com/goravel/framework/contracts/process"

	"goravel/app/facades"
	"goravel/app/services/agent/dispatch"
)

const (
	agentShip      = "ship"
	agentImplement = "implement"
	agentReview    = "review"

	// replyDecoration is the surrounding whitespace, punctuation and markdown
	// stripped from the router's reply tokens before they are matched.
	replyDecoration = " \t\r\n.!?\"'`*():[],;"
)

// AgentRouter chooses which agent should handle an instruction.
type AgentRouter interface {
	// Route decides which agent should handle the prompt and which repository
	// (owner/name) it should run against. An empty repository means "use the
	// source repository".
	Route(ctx context.Context, prompt string) (string, string, error)
}

// Dispatcher runs an agent for a dispatch request. The request struct lives in
// its own package so the generated mocks do not have to import the service
// package: the in-package tests that use those mocks would otherwise be an
// import cycle.
type Dispatcher interface {
	Run(ctx context.Context, req dispatch.Request) error
}

// agentIntroductions maps each routable agent to the one-line introduction the
// AI facade uses to choose it. It is the single source of truth for the routable
// set: the parser accepts exactly these keys and the routing prompt is built
// from them, keyed through slices.Sorted so it stays deterministic.
var agentIntroductions = map[string]string{
	agentShip:      "plans the task first, then implements and drives it through review to a pull request",
	agentImplement: "implements a plan that is already provided, reviews the changes, and opens a pull request",
	agentReview:    "reviews an existing GitHub pull request and reports findings",
}

// aiRouter routes instructions to an agent using the configured AI provider.
type aiRouter struct{}

// newAIRouter returns the AI-backed agent router.
func newAIRouter() *aiRouter {
	return &aiRouter{}
}

// Route asks the AI provider which agent should handle the prompt and which
// repository it should run against.
func (r *aiRouter) Route(ctx context.Context, prompt string) (string, string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", "", errors.New("empty prompt")
	}

	conversation, err := facades.AI().WithContext(ctx).Agent(routerAgent{})
	if err != nil {
		return "", "", fmt.Errorf("create routing conversation: %w", err)
	}

	response, err := conversation.Prompt(prompt)
	if err != nil {
		return "", "", fmt.Errorf("route prompt: %w", err)
	}

	return parseRoute(response.Text())
}

// parseRoute maps the AI reply to an agent name and an optional repository. The
// reply is `<agent> <owner/repo>`; the agent is required and normalised, the
// repository is taken verbatim (minus decoration) so the AI decides the target.
// An empty repository means "use the source repository".
func parseRoute(reply string) (string, string, error) {
	fields := strings.Fields(reply)
	if len(fields) == 0 {
		return "", "", errors.New("empty reply")
	}

	agent, err := parseAgent(fields[0])
	if err != nil {
		return "", "", err
	}

	var repository string
	if len(fields) > 1 {
		repository = strings.Trim(fields[1], replyDecoration)
	}

	return agent, repository, nil
}

// parseAgent maps a single token to a supported agent name, tolerating
// surrounding whitespace, punctuation and markdown decoration.
func parseAgent(token string) (string, error) {
	normalized := strings.Trim(strings.ToLower(strings.TrimSpace(token)), replyDecoration)

	if _, ok := agentIntroductions[normalized]; ok {
		return normalized, nil
	}

	return "", fmt.Errorf("unrecognised agent reply %q", token)
}

// routerAgent is the AI agent used to classify instructions.
type routerAgent struct{}

// Instructions returns the system prompt that constrains the reply to one of
// the supported agent names and the repository to work on.
func (routerAgent) Instructions() string {
	names := slices.Sorted(maps.Keys(agentIntroductions))

	var b strings.Builder
	b.WriteString("Decide how to handle a GitHub mention. The " + repositoryLabel + " line and links are the source; ")
	b.WriteString("reply with exactly two space-separated tokens: one agent from: ")
	for i, name := range names {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name)
		b.WriteString(" (")
		b.WriteString(agentIntroductions[name])
		b.WriteString(")")
	}
	b.WriteString("; then the repository to work on as owner/name. Do not add any other text.")

	return b.String()
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
// (AGENT_API_TOKEN, DEEPSEEK_*, database credentials, ...) are not inherited.
// Output is not buffered in full: only a bounded prefix of stderr is captured
// (and later escaped) for the error message, and the live stream is suppressed.
func (d *opencodeDispatcher) Run(ctx context.Context, req dispatch.Request) error {
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
		Path(req.Dir).
		Timeout(d.deadline).
		DisableBuffering().
		Quietly().
		OnOutput(capture).
		Run("env", "-i",
			"PATH="+os.Getenv("PATH"),
			"HOME="+os.Getenv("HOME"),
			d.bin, "run", "--agent", req.Agent, "--", req.Prompt)

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
