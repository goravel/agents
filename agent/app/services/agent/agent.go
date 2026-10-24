package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/goravel/framework/contracts/ai"

	"goravel/app/constants"
	"goravel/app/facades"
	"goravel/app/services/agent/dispatch"
)

const (
	// replyDecoration is the surrounding whitespace, punctuation and markdown
	// stripped from the router's reply tokens before they are matched.
	replyDecoration = " \t\r\n.!?\"'`*():[],;"

	// titleLimit caps the title in characters. `paseo ls` truncates the NAME
	// column at 60 characters, so a longer title is cut in the list.
	titleLimit = 60
	// noRepository is the sentinel the router puts on the repository line when
	// the mention names no usable repository.
	noRepository = "-"
)

// AgentRouter chooses which agent should handle an instruction, which
// repository to run against, and the title paseo shows for the run. An empty
// title means the router inferred none; the service then names the run from the
// issue or pull request number.
type AgentRouter interface {
	// Route decides which agent should handle the prompt, which repository
	// (owner/name) it should run against, and the title paseo shows for the run.
	// The prompt is untrusted comment text and may name the repository to work
	// on; routing runs only for allowlisted senders, so naming a different
	// repository is intended, not an escalation. An empty repository means "use
	// the source repository"; an empty title means the router inferred none.
	Route(ctx context.Context, prompt string) (agent, repository, title string, err error)
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
	constants.AgentShip:      "plans the task first, then implements and drives it through review to a pull request",
	constants.AgentImplement: "implements a plan that is already provided, reviews the changes, and opens a pull request",
	constants.AgentReview:    "reviews an existing GitHub pull request and reports findings",
}

// aiRouter routes instructions to an agent using the configured AI provider.
type aiRouter struct{}

// newAIRouter returns the AI-backed agent router.
func newAIRouter() *aiRouter {
	return &aiRouter{}
}

// Route asks the AI provider which agent should handle the prompt, which
// repository it should run against, and the title paseo shows for the run.
func (r *aiRouter) Route(ctx context.Context, prompt string) (string, string, string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", "", "", errors.New("empty prompt")
	}

	conversation, err := facades.AI().WithContext(ctx).Agent(routerAgent{})
	if err != nil {
		return "", "", "", fmt.Errorf("create routing conversation: %w", err)
	}

	response, err := conversation.Prompt(prompt)

	facades.Log().Debugf("Routing prompt: \n%s\nresponse: %s", prompt, response.Text())

	if err != nil {
		return "", "", "", fmt.Errorf("route prompt: %w", err)
	}

	agent, repository, title, rejected, err := parseReply(response.Text())
	if err != nil {
		return "", "", "", err
	}
	if rejected != "" {
		facades.Log().Infof("agent: route: ignoring repository %q from the reply: not a bare owner/name; using the source repository", rejected)
	}

	return agent, repository, title, nil
}

// parseReply maps the three-line reply to the agent, an optional repository and
// the run title: `<agent>`, `<owner/repo>` (or `-`) and the title, one value per
// line because the title is free text. A blank or `-` repository line means "use
// the source repository"; a token that is not a bare owner/name is dropped and
// returned as rejected. The reply is trimmed first so a leading blank line
// cannot swallow the agent line; the title is the third line capped at titleLimit
// runes, and a reply with fewer than three lines yields an empty title, which the
// service then names from the event. Only the first token of the agent line is
// read; extra lines past the title are ignored.
func parseReply(reply string) (agent, repository, title, rejected string, err error) {
	lines := strings.Split(strings.TrimSpace(reply), "\n")

	fields := strings.Fields(lines[0])
	if len(fields) == 0 {
		return "", "", "", "", errors.New("empty reply")
	}

	agent, err = parseAgent(fields[0])
	if err != nil {
		return "", "", "", "", err
	}

	if len(lines) > 1 {
		if token := strings.TrimSpace(lines[1]); token != "" && token != noRepository {
			if parsed, ok := parseRepository(token); ok {
				repository = parsed
			} else {
				rejected = token
			}
		}
	}

	if len(lines) > 2 {
		runes := []rune(strings.TrimSpace(lines[2]))
		if len(runes) > titleLimit {
			runes = runes[:titleLimit]
		}
		title = string(runes)
	}

	return agent, repository, title, rejected, nil
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

// Instructions returns the system prompt that constrains the reply to one of the
// supported agent names on line 1, the repository to work on on line 2, and a
// clear title for the run on line 3. The prompt gives the repository named by the
// Instruction precedence over the Repository line, requires the repository line
// to be a bare owner/name (or the `-` sentinel), and asks for the title third.
func (routerAgent) Instructions() string {
	names := slices.Sorted(maps.Keys(agentIntroductions))

	var b strings.Builder
	b.WriteString("Decide how to handle a GitHub mention. ")
	b.WriteString("Reply with exactly three lines. ")
	b.WriteString("Line 1 holds one agent from: ")
	for i, name := range names {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name)
		b.WriteString(" (")
		b.WriteString(agentIntroductions[name])
		b.WriteString(")")
	}
	b.WriteString(". ")
	b.WriteString("Line 2 holds the repository to work on as a bare owner/name, or " + noRepository + " when there is no usable repository. ")
	b.WriteString("Line 3 holds a clear and simple title for the run: one line, at most " + strconv.Itoa(titleLimit) + " characters, no quotes and no markdown. ")
	b.WriteString("The " + instructionLabel + " may name a repository, and when it does that repository is the one to work on, even when it differs from the " + repositoryLabel + " line. ")
	b.WriteString("Only when the " + instructionLabel + " names no repository, or names something that is not a bare owner/name, do you use the " + repositoryLabel + " line. ")
	b.WriteString("For example, with " + repositoryLabel + ": owner/source and " + instructionLabel + ": ship this issue in owner/target, reply: ship\nowner/target\nShip issue 123 in owner/target. ")
	b.WriteString("Reply with only those three lines: on line 1 the agent, on line 2 the repository as a bare owner/name (no scheme, no URL, no trailing .git, no markdown) or " + noRepository + ", on line 3 the title; never repeat the instruction text. ")
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
// [--title <title>] --no-wait -- <prompt>` in the request's directory with a
// scrubbed environment.
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
	if req.Title != "" {
		args = append(args, "--title", req.Title)
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
