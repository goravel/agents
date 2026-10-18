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
	// (owner/name) it should run against. The prompt is untrusted comment text
	// and may name the repository to work on; routing runs only for allowlisted
	// senders, so naming a different repository is intended, not an escalation.
	// An empty repository means "use the source repository".
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

	agent, repository, rejected, err := parseReply(response.Text())
	if err != nil {
		return "", "", err
	}
	if rejected != "" {
		facades.Log().Infof("agent: route: ignoring repository %q from the reply: not a bare owner/name; using the source repository", rejected)
	}

	return agent, repository, nil
}

// parseReply maps the AI reply to an agent name and an optional repository. The
// reply is `<agent> <owner/repo>`; the agent is required and normalised, and the
// repository token is reduced to a bare owner/name (decoration, a GitHub URL
// prefix and a trailing .git are stripped). A token that is not a bare
// owner/name is dropped, so an empty repository — meaning "use the source
// repository" — is returned rather than a bogus clone target. The rejected
// result carries that dropped token, and is empty only when the reply named no
// repository, so the caller can log every unusable target.
func parseReply(reply string) (agent, repository, rejected string, err error) {
	fields := strings.Fields(reply)
	if len(fields) == 0 {
		return "", "", "", errors.New("empty reply")
	}

	agent, err = parseAgent(fields[0])
	if err != nil {
		return "", "", "", err
	}

	if len(fields) == 1 {
		return agent, "", "", nil
	}

	repository, ok := parseRepository(fields[1])
	if !ok {
		return agent, "", fields[1], nil
	}

	return agent, repository, "", nil
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

// repositoryURLPrefixes are the GitHub URL prefixes stripped from a routed
// repository token so only a bare owner/name reaches the checkout. They reuse
// the workspace's clone and web bases so the strip list cannot drift from the
// checkout.
var repositoryURLPrefixes = []string{
	gitWebURL,
	"http://github.com/",
	gitCloneURL,
}

// parseRepository reduces a routed repository token to a bare owner/name: it
// trims surrounding decoration, unwraps a GitHub URL prefix, trims a trailing
// slash and then a trailing .git, and finally requires exactly `owner/name`. The
// bool reports whether the token was reduced; false means the reply named
// something that is not a bare owner/name, so the caller falls back to the
// source repository (an empty token) rather than cloning a bogus target. A
// well-formed repository named by the untrusted instruction is returned as-is:
// cross-repository routing is intended, gated by the allowlisted-sender check
// that runs before routing.
func parseRepository(token string) (string, bool) {
	token = strings.Trim(strings.TrimSpace(token), replyDecoration)
	for _, prefix := range repositoryURLPrefixes {
		if rest, ok := strings.CutPrefix(token, prefix); ok {
			token = rest
			break
		}
	}
	token = strings.TrimSuffix(strings.TrimSuffix(token, "/"), ".git")

	if !isRepositoryName(token) {
		return "", false
	}

	return token, true
}

// isRepositoryName reports whether token is a bare owner/name: exactly two
// non-empty slash-separated parts of GitHub name characters.
func isRepositoryName(token string) bool {
	owner, name, ok := strings.Cut(token, "/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return false
	}

	return isNamePart(owner) && isNamePart(name)
}

// isNamePart reports whether part is a plausible GitHub owner or repository
// name: ASCII letters, digits, underscore, hyphen or dot, carrying at least one
// letter or digit so a dots-only part cannot reach checkout.
func isNamePart(part string) bool {
	if part == "" || !hasLetterOrDigit(part) {
		return false
	}

	for i := 0; i < len(part); i++ {
		b := part[i]
		if isHandleByte(b) || b == '.' {
			continue
		}

		return false
	}

	return true
}

// routerAgent is the AI agent used to classify instructions.
type routerAgent struct{}

// Instructions returns the system prompt that constrains the reply to one of
// the supported agent names and the repository to work on. The prompt gives the
// repository named by the Instruction precedence over the Repository line, and
// requires the reply to be a bare owner/name.
func (routerAgent) Instructions() string {
	names := slices.Sorted(maps.Keys(agentIntroductions))

	var b strings.Builder
	b.WriteString("Decide how to handle a GitHub mention. ")
	b.WriteString("Reply with exactly two space-separated tokens: one agent from: ")
	for i, name := range names {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name)
		b.WriteString(" (")
		b.WriteString(agentIntroductions[name])
		b.WriteString(")")
	}
	b.WriteString("; then the repository to work on. ")
	b.WriteString("The " + instructionLabel + " may name a repository, and when it does that repository is the one to work on, even when it differs from the " + repositoryLabel + " line. ")
	b.WriteString("Only when the " + instructionLabel + " names no repository, or names something that is not a bare owner/name, do you use the " + repositoryLabel + " line. ")
	b.WriteString("For example, with " + repositoryLabel + ": owner/source and " + instructionLabel + ": ship this issue in owner/target, reply: ship owner/target. ")
	b.WriteString("Reply with only the agent and the repository as a bare owner/name (no scheme, no URL, no trailing .git, no markdown); never repeat the instruction text. ")
	b.WriteString("Do not add any other text.")

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
