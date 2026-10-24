package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/goravel/framework/contracts/ai"

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

// agentIntroductions maps each routable agent to the introduction the router
// prompt shows. Every key needs an agent.models entry.
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

	facades.Log().Debugf("Routing prompt: \n%s\nresponse: %s", prompt, response.Text())

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
	// paseoBin is the CLI that dispatches runs.
	paseoBin = "paseo"
	// opencodeProvider is the paseo agent provider.
	opencodeProvider = "opencode"
	// stderrLimit bounds the agent stderr kept for the error message.
	stderrLimit = 64 * 1024
)

// opencodeDispatcher dispatches agent runs through paseo.
type opencodeDispatcher struct {
	bin      string
	provider string
	// models maps a mode to its `<model>[#<thinking>]` entry.
	models map[string]string
}

// newOpencodeDispatcher returns a dispatcher for the mode → model map.
func newOpencodeDispatcher(models map[string]string) *opencodeDispatcher {
	return &opencodeDispatcher{
		bin:      paseoBin,
		provider: opencodeProvider,
		models:   models,
	}
}

// parseModelEntry splits a `<model>[#<thinking>]` entry; ok is false without a model.
func parseModelEntry(entry string) (model, thinking string, ok bool) {
	model, thinking, _ = strings.Cut(strings.TrimSpace(entry), "#")

	model = strings.TrimSpace(model)
	thinking = strings.TrimSpace(thinking)

	return model, thinking, model != ""
}

// modelFor returns the model and thinking level configured for mode.
func (d *opencodeDispatcher) modelFor(mode string) (string, string, error) {
	model, thinking, ok := parseModelEntry(d.models[mode])
	if !ok {
		return "", "", fmt.Errorf("no model configured for mode %q", mode)
	}

	return model, thinking, nil
}

// agentModels reads agent.models and checks every routable agent has a usable entry.
func agentModels(value any) (map[string]string, error) {
	models, _ := value.(map[string]string)
	if len(models) == 0 {
		return nil, errors.New("agent: agent.models must map every agent to a <model>[#<thinking>] entry")
	}

	for _, mode := range slices.Sorted(maps.Keys(agentIntroductions)) {
		if _, _, ok := parseModelEntry(models[mode]); !ok {
			return nil, fmt.Errorf("agent: agent.models has no model for the %q agent", mode)
		}
	}

	return models, nil
}

// Run executes `paseo run --mode <mode> --model <model> [--thinking <level>]
// --no-wait -- <prompt>` in the request's directory with a scrubbed environment.
func (d *opencodeDispatcher) Run(ctx context.Context, req dispatch.Request) error {
	model, thinking, err := d.modelFor(req.Agent)
	if err != nil {
		return err
	}

	args := []string{
		"-i",
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		d.bin, "run",
		"--provider", d.provider,
		"--mode", req.Agent,
		"--model", model,
	}
	if thinking != "" {
		args = append(args, "--thinking", thinking)
	}
	// `--` keeps the prompt from being read as a flag.
	args = append(args, "--no-wait", "--", req.Prompt)

	facades.Log().Debug("Running paseo:", "\ndirectory: ", req.Dir, "\ncommand: ", strings.Join(args[3:], " "))

	result := facades.Process().
		WithContext(ctx).
		Path(req.Dir).
		DisableBuffering().
		Quietly().
		Run("env", args...)

	if result.Failed() {
		return fmt.Errorf("paseo run failed (exit %d): %w", result.ExitCode(), result.Error())
	}

	return nil
}
