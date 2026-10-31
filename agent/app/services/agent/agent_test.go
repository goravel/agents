package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	testifymock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	mocksai "github.com/goravel/framework/mocks/ai"
	mockslog "github.com/goravel/framework/mocks/log"
	mocksprocess "github.com/goravel/framework/mocks/process"
	"github.com/goravel/framework/testing/mock"

	"goravel/app/constants"
	"goravel/app/services/agent/dispatch"
)

func TestParseReply(t *testing.T) {
	tests := []struct {
		name         string
		reply        string
		wantAgent    string
		wantRepo     string
		wantTitle    string
		wantRejected string
		wantErr      bool
	}{
		{name: "review", reply: "review", wantAgent: constants.AgentReview},
		{name: "ship with spaces", reply: "  SHIP ", wantAgent: constants.AgentShip},
		{name: "review with backticks", reply: "`review`", wantAgent: constants.AgentReview},
		{name: "review in bold markdown", reply: "**review**", wantAgent: constants.AgentReview},
		{name: "analyze", reply: "analyze", wantAgent: constants.AgentAnalyze},
		{name: "analyze in bold markdown", reply: "**analyze**", wantAgent: constants.AgentAnalyze},
		{name: "ship in parentheses", reply: "(ship)", wantAgent: constants.AgentShip},
		{name: "ship with quotes", reply: `"ship"`, wantAgent: constants.AgentShip},
		{name: "review with carriage return", reply: "review\r\n", wantAgent: constants.AgentReview},
		{
			name:      "analyze with repository",
			reply:     "analyze\ngoravel/framework",
			wantAgent: constants.AgentAnalyze,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "surrounding spaces on the repository line",
			reply:     "review\n   goravel/framework  ",
			wantAgent: constants.AgentReview,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "decorated repository",
			reply:     "review\n'goravel/framework'",
			wantAgent: constants.AgentReview,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "dotted repository name",
			reply:     "ship\ngoravel/repo.github.io",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/repo.github.io",
		},
		{
			name:      "dotted name",
			reply:     "ship\ngoravel/.github",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/.github",
		},
		{
			name:      "hyphen and underscore in names",
			reply:     "ship\nmy-org/my_repo",
			wantAgent: constants.AgentShip,
			wantRepo:  "my-org/my_repo",
		},
		{
			// A token that is not a bare owner/name is dropped so the caller
			// falls back to the source repository instead of cloning a bogus
			// target. The dropped token is reported so Route can log it.
			name:         "non owner/name token falls back to source",
			reply:        "ship\n../etc",
			wantAgent:    constants.AgentShip,
			wantRejected: "../etc",
		},
		{
			name:         "bare word token falls back to source",
			reply:        "ship\nthis",
			wantAgent:    constants.AgentShip,
			wantRejected: "this",
		},
		{
			name:      "repository with trailing .git",
			reply:     "ship\ngoravel/framework.git",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "repository as https URL",
			reply:     "ship\nhttps://github.com/goravel/framework",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "repository as https URL with .git",
			reply:     "ship\nhttps://github.com/goravel/framework.git",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "repository as ssh URL",
			reply:     "ship\ngit@github.com:goravel/framework.git",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "https URL with trailing slash after .git",
			reply:     "ship\nhttps://github.com/goravel/framework.git/",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "repository with .git and trailing slash",
			reply:     "ship\ngoravel/framework.git/",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/framework",
		},
		{
			// Only the spellings above are reduced to a bare owner/name; other
			// URL shapes are deliberately dropped so the run falls back to the
			// source repository rather than cloning a bogus target.
			name:         "uppercase scheme",
			reply:        "ship\nHTTPS://github.com/goravel/framework",
			wantAgent:    constants.AgentShip,
			wantRejected: "HTTPS://github.com/goravel/framework",
		},
		{
			name:         "www host",
			reply:        "ship\nwww.github.com/goravel/framework",
			wantAgent:    constants.AgentShip,
			wantRejected: "www.github.com/goravel/framework",
		},
		{
			name:         "ssh scheme URL",
			reply:        "ship\nssh://git@github.com/goravel/framework",
			wantAgent:    constants.AgentShip,
			wantRejected: "ssh://git@github.com/goravel/framework",
		},
		{
			name:         "scheme-less host",
			reply:        "ship\ngithub.com/goravel/framework",
			wantAgent:    constants.AgentShip,
			wantRejected: "github.com/goravel/framework",
		},
		{
			name:         "URL with a path",
			reply:        "ship\nhttps://github.com/goravel/framework/tree/main",
			wantAgent:    constants.AgentShip,
			wantRejected: "https://github.com/goravel/framework/tree/main",
		},
		{
			// A dots-only name is not a plausible repository, so it is dropped
			// rather than handed to checkout as a bogus clone target.
			name:         "dots-only name falls back to source",
			reply:        "ship\nuwu/../..",
			wantAgent:    constants.AgentShip,
			wantRejected: "uwu/../..",
		},
		{
			name:         "dots-only token falls back to source",
			reply:        "ship\nuwu/..",
			wantAgent:    constants.AgentShip,
			wantRejected: "uwu/..",
		},
		{
			name:         "empty repository token",
			reply:        "ship\n.",
			wantAgent:    constants.AgentShip,
			wantRejected: ".",
		},
		{
			name:      "agent and title",
			reply:     "ship\n-\nShip the login fix",
			wantAgent: constants.AgentShip,
			wantTitle: "Ship the login fix",
		},
		{
			name:      "agent, repository and title",
			reply:     "ship\ngoravel/framework\nShip issue 123 in goravel/framework",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/framework",
			wantTitle: "Ship issue 123 in goravel/framework",
		},
		{
			// The title is returned trimmed but otherwise verbatim: no label,
			// quote or markdown cleanup.
			name:      "title is returned verbatim",
			reply:     "ship\n-\n\"Ship issue 123\"",
			wantAgent: constants.AgentShip,
			wantTitle: "\"Ship issue 123\"",
		},
		{
			// Only the third line is the title: anything after it is ignored.
			name:      "only the third line is the title",
			reply:     "ship\n-\nShip issue 123\nand more detail",
			wantAgent: constants.AgentShip,
			wantTitle: "Ship issue 123",
		},
		{
			// An empty repository line means "use the source repository".
			name:      "empty repository line",
			reply:     "ship\n\nShip the login fix",
			wantAgent: constants.AgentShip,
			wantTitle: "Ship the login fix",
		},
		{
			// `-` is the router's sentinel for "no usable repository".
			name:      "dash repository sentinel",
			reply:     "ship\n-\nShip the login fix",
			wantAgent: constants.AgentShip,
			wantTitle: "Ship the login fix",
		},
		{
			// A two-line reply routes but names no title.
			name:      "two-line reply has no title",
			reply:     "ship\ngoravel/framework",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/framework",
			wantTitle: "",
		},
		{
			name:      "empty title line",
			reply:     "ship\ngoravel/framework\n",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/framework",
			wantTitle: "",
		},
		{
			name:      "long title is capped",
			reply:     "ship\n-\n" + strings.Repeat("a", 70),
			wantAgent: constants.AgentShip,
			wantTitle: strings.Repeat("a", 60),
		},
		{
			name:      "long unicode title is capped by rune",
			reply:     "ship\n-\n" + strings.Repeat("é", 70),
			wantAgent: constants.AgentShip,
			wantTitle: strings.Repeat("é", 60),
		},
		{
			// Only line 2 carries the repository: a token beside the agent on
			// line 1 is not parsed, so nothing is routed or reported.
			name:      "repository on the agent line is ignored",
			reply:     "ship goravel/framework",
			wantAgent: constants.AgentShip,
			wantRepo:  "",
			wantTitle: "",
		},
		{
			// A model reply that opens with a blank line must still route: the
			// reply is trimmed before it is split on the first newline.
			name:      "leading newline",
			reply:     "\nship\ngoravel/framework\nShip issue 123 in goravel/framework",
			wantAgent: constants.AgentShip,
			wantRepo:  "goravel/framework",
			wantTitle: "Ship issue 123 in goravel/framework",
		},
		{name: "empty", reply: "", wantErr: true},
		{name: "unrecognised", reply: "nonsense", wantErr: true},
		{name: "removed reply token", reply: "plan", wantErr: true},
		{name: "removed implement reply token", reply: "implement", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, repository, title, rejected, err := parseReply(test.reply)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantAgent, agent)
			require.Equal(t, test.wantRepo, repository)
			require.Equal(t, test.wantTitle, title)
			require.Equal(t, test.wantRejected, rejected)
		})
	}
}

func TestRouterAgentInstructions(t *testing.T) {
	// The prompt is pinned exactly: the reply vocabulary must stay the agent
	// names parseAgent accepts on line 1, the repository on line 2 and the run
	// title on line 3. The character budget here mirrors titleLimit (60, the
	// `paseo ls` NAME width): the prompt is built from that constant, so changing
	// the cap must be a deliberate change to this pin too.
	require.Equal(t,
		"Decide how to handle a GitHub mention. "+
			"Reply with exactly three lines. "+
			"Line 1 holds one agent from: "+
			"analyze (analyzes an existing GitHub pull request for breaking changes and usage, without editing code), "+
			"review (reviews an existing GitHub pull request and reports findings), "+
			"ship (plans the task first, then implements and drives it through review to a pull request). "+
			"Line 2 holds the repository to work on as a bare owner/name, or - when there is no usable repository. "+
			"Line 3 holds a clear and simple title for the run: one line, at most 60 characters, no quotes and no markdown. "+
			"The Instruction may name a repository, and when it does that repository is the one to work on, "+
			"even when it differs from the Repository line. "+
			"Only when the Instruction names no repository, or names something that is not a bare owner/name, do you use the Repository line. "+
			"For example, with Repository: owner/source and Instruction: ship this issue in owner/target, reply: ship\nowner/target\nShip issue 123 in owner/target. "+
			"Reply with only those three lines: on line 1 the agent, on line 2 the repository as a bare owner/name (no scheme, no URL, no trailing .git, no markdown) or -, on line 3 the title; "+
			"never repeat the instruction text. "+
			"Do not add any other text.",
		routerAgent{}.Instructions())
}

func TestAIRouterRoute(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	aiMock := factory.AI()
	conversation := mocksai.NewConversation(t)
	response := mocksai.NewAgentResponse(t)

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(conversation, nil).Once()
	conversation.EXPECT().Prompt("review this change").Return(response, nil).Once()
	// Text is read twice: once by the route debug log, once by parseReply.
	response.EXPECT().Text().Return("review\ngoravel/framework\nReview PR 42").Twice()

	router := newAIRouter()
	agent, repository, title, err := router.Route(context.Background(), "review this change")
	require.NoError(t, err)
	require.Equal(t, constants.AgentReview, agent)
	require.Equal(t, "goravel/framework", repository)
	require.Equal(t, "Review PR 42", title)
	aiMock.AssertExpectations(t)
}

func TestAIRouterRouteRejectedRepository(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	aiMock := factory.AI()
	conversation := mocksai.NewConversation(t)
	response := mocksai.NewAgentResponse(t)

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(conversation, nil).Once()
	conversation.EXPECT().Prompt("review this change").Return(response, nil).Once()
	// Text is read twice: once by the route debug log, once by parseReply.
	response.EXPECT().Text().Return("review\nthis issue").Twice()

	router := newAIRouter()
	agent, repository, title, err := router.Route(context.Background(), "review this change")
	require.NoError(t, err)
	require.Equal(t, constants.AgentReview, agent)
	require.Empty(t, repository)
	require.Empty(t, title)
	aiMock.AssertExpectations(t)
}

func TestAIRouterRouteEmptyPrompt(t *testing.T) {
	router := newAIRouter()

	_, _, _, err := router.Route(context.Background(), "   ")
	require.Error(t, err)
}

func TestAIRouterRouteAIError(t *testing.T) {
	factory := mock.Factory()
	aiMock := factory.AI()

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(nil, errors.New("boom")).Once()

	router := newAIRouter()
	_, _, _, err := router.Route(context.Background(), "do something")
	require.Error(t, err)
	aiMock.AssertExpectations(t)
}

func TestAIRouterRoutePromptError(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	aiMock := factory.AI()
	conversation := mocksai.NewConversation(t)
	response := mocksai.NewAgentResponse(t)

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(conversation, nil).Once()
	conversation.EXPECT().Prompt("do something").Return(response, errors.New("provider down")).Once()
	// The route debug log reads Text before the error is inspected, so the
	// response is non-nil here; only the wrapped error is asserted below.
	response.EXPECT().Text().Return("").Once()

	router := newAIRouter()
	_, _, _, err := router.Route(context.Background(), "do something")
	require.Error(t, err)
	require.Contains(t, err.Error(), "route prompt")
	aiMock.AssertExpectations(t)
}

func TestAIRouterRouteUnrecognisedReply(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	aiMock := factory.AI()
	conversation := mocksai.NewConversation(t)
	response := mocksai.NewAgentResponse(t)

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(conversation, nil).Once()
	conversation.EXPECT().Prompt("do something").Return(response, nil).Once()
	// Text is read twice: once by the route debug log, once by parseReply.
	response.EXPECT().Text().Return("nonsense").Twice()

	router := newAIRouter()
	_, _, _, err := router.Route(context.Background(), "do something")
	require.Error(t, err)
	aiMock.AssertExpectations(t)
}

// scrubbedEnvArgs returns the `env -i PATH=... HOME=...` prefix the dispatcher
// uses to strip secrets from the agent subprocess.
func scrubbedEnvArgs() []any {
	return []any{"env", "-i", "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
}

// testModels is the mode → model map the dispatcher tests run with.
func testModels() map[string]string {
	return map[string]string{
		constants.AgentShip:    "opencode-go/deepseek-v4.1-flash#high",
		constants.AgentReview:  "opencode-go/deepseek-v4.1-flash#high",
		constants.AgentAnalyze: "opencode-go/deepseek-v4.1-flash#high",
	}
}

// dispatcherArgs returns the argv the dispatcher builds for the run, with
// `do it` as prompt.
func dispatcherArgs(mode, model, thinking, title, workspaceID string) []any {
	args := append(scrubbedEnvArgs(), "paseo", "run",
		"--provider", "opencode",
		"--mode", mode,
		"--model", model,
	)
	if thinking != "" {
		args = append(args, "--thinking", thinking)
	}
	if title != "" {
		args = append(args, "--title", title)
	}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}

	return append(args, "--no-wait", "--", "do it")
}

// workspaceID is the id the faked `paseo workspace create` reports.
const workspaceID = "wks_0123456789abcdef"

// workspaceCommand returns the paseo argv the dispatcher builds — and logs — to
// create the run workspace for dir and title. It is the same list the dispatcher
// executes once the `env -i …` prefix is added.
func workspaceCommand(dir, title string) []string {
	return []string{"paseo", "workspace", "create",
		"--isolation", "local",
		"--path", dir,
		"--title", title,
		"--json",
	}
}

// workspaceArgs returns workspaceCommand behind the `env -i PATH=... HOME=...`
// prefix the dispatcher executes.
func workspaceArgs(dir, title string) []any {
	args := scrubbedEnvArgs()
	for _, arg := range workspaceCommand(dir, title) {
		args = append(args, arg)
	}

	return args
}

// expectWorkspaceCreate arms the shared process mock for one
// `paseo workspace create` call; the caller owns the returned result's
// expectations so the failure cases can drive it.
func expectWorkspaceCreate(processMock *mocksprocess.Process, dir, title string, result *mocksprocess.Result) {
	args := workspaceArgs(dir, title)

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path(dir).Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
}

func TestOpencodeDispatcherRun(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	processMock := factory.Process()
	createResult := mocksprocess.NewResult(t)
	result := mocksprocess.NewResult(t)

	expectWorkspaceCreate(processMock, "/tmp/ws/goravel/goravel", "Review PR 42", createResult)
	createResult.EXPECT().Failed().Return(false).Once()
	createResult.EXPECT().Output().Return(`{"workspaceId":"` + workspaceID + `"}`).Once()

	args := dispatcherArgs(constants.AgentReview, "opencode-go/deepseek-v4.1-flash", "high", "Review PR 42", workspaceID)

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path("/tmp/ws/goravel/goravel").Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
	result.EXPECT().Failed().Return(false).Once()

	dispatcher := newOpencodeDispatcher(testModels())
	require.NoError(t, dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/goravel",
		Agent:  "review",
		Prompt: "do it",
		Title:  "Review PR 42",
	}))

	processMock.AssertExpectations(t)
	createResult.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestOpencodeDispatcherRunFailure(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	processMock := factory.Process()
	createResult := mocksprocess.NewResult(t)
	result := mocksprocess.NewResult(t)

	expectWorkspaceCreate(processMock, "/tmp/ws/goravel/framework", "Ship issue 123", createResult)
	createResult.EXPECT().Failed().Return(false).Once()
	createResult.EXPECT().Output().Return(`{"workspaceId":"` + workspaceID + `"}`).Once()

	args := dispatcherArgs(constants.AgentShip, "opencode-go/deepseek-v4.1-flash", "high", "Ship issue 123", workspaceID)

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path("/tmp/ws/goravel/framework").Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
	result.EXPECT().Failed().Return(true).Once()
	result.EXPECT().ExitCode().Return(2).Once()
	result.EXPECT().Error().Return(errors.New("boom")).Once()

	dispatcher := newOpencodeDispatcher(testModels())
	err := dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/framework",
		Agent:  "ship",
		Prompt: "do it",
		Title:  "Ship issue 123",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")

	processMock.AssertExpectations(t)
	createResult.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestOpencodeDispatcherRunStartError(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	processMock := factory.Process()
	createResult := mocksprocess.NewResult(t)
	result := mocksprocess.NewResult(t)

	expectWorkspaceCreate(processMock, "/tmp/ws/goravel/goravel", "Review PR 42", createResult)
	createResult.EXPECT().Failed().Return(false).Once()
	createResult.EXPECT().Output().Return(`{"workspaceId":"` + workspaceID + `"}`).Once()

	args := dispatcherArgs(constants.AgentReview, "opencode-go/deepseek-v4.1-flash", "high", "Review PR 42", workspaceID)

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path("/tmp/ws/goravel/goravel").Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
	result.EXPECT().Failed().Return(true).Once()
	result.EXPECT().ExitCode().Return(-1).Once()
	result.EXPECT().Error().Return(errors.New("start failed")).Once()

	dispatcher := newOpencodeDispatcher(testModels())
	err := dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/goravel",
		Agent:  "review",
		Prompt: "do it",
		Title:  "Review PR 42",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "start failed")

	processMock.AssertExpectations(t)
	createResult.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestOpencodeDispatcherWorkspaceCreateFailure(t *testing.T) {
	factory := mock.Factory()
	logMock := mockslog.NewLog(t)
	factory.App().EXPECT().MakeLog().Return(logMock).Once()
	processMock := factory.Process()
	createResult := mocksprocess.NewResult(t)
	// What a real Result carries for a process that exited 1: the wait error in
	// Error() and paseo's own multi-line text in ErrorOutput().
	exitErr := errors.New("exit status 1")
	daemonDown := "Cannot connect to daemon at 127.0.0.1:6767:\n  connect ECONNREFUSED\n"

	expectWorkspaceCreate(processMock, "/tmp/ws/goravel/framework", "Ship issue 123", createResult)
	createResult.EXPECT().Failed().Return(true).Once()
	createResult.EXPECT().ExitCode().Return(1).Once()
	createResult.EXPECT().ErrorOutput().Return(daemonDown).Once()
	createResult.EXPECT().Error().Return(exitErr).Once()

	// The aborted dispatch is the only entry this run gets: the create that
	// failed and its reason, and no run command because none was built. The
	// reason is paseo's stderr collapsed to one line, before the wrapped error.
	logMock.EXPECT().Debug("Running paseo:",
		"\ndirectory: /tmp/ws/goravel/framework"+
			"\ncommand: "+shellCommand(workspaceCommand("/tmp/ws/goravel/framework", "Ship issue 123"))+
			"\nerror: paseo workspace create failed (exit 1): "+
			"Cannot connect to daemon at 127.0.0.1:6767: connect ECONNREFUSED: exit status 1",
	).Once()

	// No run expectation: a create failure must abort before `paseo run`.
	dispatcher := newOpencodeDispatcher(testModels())
	err := dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/framework",
		Agent:  constants.AgentShip,
		Prompt: "do it",
		Title:  "Ship issue 123",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "paseo workspace create failed (exit 1)")
	// Both parts: paseo's reason, collapsed onto the one line the log promises.
	require.Contains(t, err.Error(), ": Cannot connect to daemon at 127.0.0.1:6767: connect ECONNREFUSED: ")
	require.NotContains(t, err.Error(), "\n")
	// The underlying wait error is wrapped, so a caller can still inspect it.
	require.ErrorIs(t, err, exitErr)

	processMock.AssertExpectations(t)
	createResult.AssertExpectations(t)
	logMock.AssertExpectations(t)
}

func TestOpencodeDispatcherWorkspaceCreateFailureNoStderr(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	processMock := factory.Process()
	createResult := mocksprocess.NewResult(t)
	// A process that never started reports its reason only through Error().
	exitErr := errors.New(`exec: "paseo": executable file not found in $PATH`)

	expectWorkspaceCreate(processMock, "/tmp/ws/goravel/framework", "Ship issue 123", createResult)
	createResult.EXPECT().Failed().Return(true).Once()
	createResult.EXPECT().ExitCode().Return(1).Once()
	createResult.EXPECT().ErrorOutput().Return("").Once()
	createResult.EXPECT().Error().Return(exitErr).Once()

	dispatcher := newOpencodeDispatcher(testModels())
	err := dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/framework",
		Agent:  constants.AgentShip,
		Prompt: "do it",
		Title:  "Ship issue 123",
	})
	require.EqualError(t, err, `paseo workspace create failed (exit 1): exec: "paseo": executable file not found in $PATH`)
	require.ErrorIs(t, err, exitErr)

	processMock.AssertExpectations(t)
	createResult.AssertExpectations(t)
}

func TestOpencodeDispatcherWorkspaceCreateBadJSON(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	processMock := factory.Process()
	createResult := mocksprocess.NewResult(t)

	expectWorkspaceCreate(processMock, "/tmp/ws/goravel/framework", "Ship issue 123", createResult)
	createResult.EXPECT().Failed().Return(false).Once()
	createResult.EXPECT().Output().Return("not json").Once()

	// Unreadable output aborts instead of falling back to an untitled run.
	dispatcher := newOpencodeDispatcher(testModels())
	err := dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/framework",
		Agent:  constants.AgentShip,
		Prompt: "do it",
		Title:  "Ship issue 123",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode paseo workspace create output")

	processMock.AssertExpectations(t)
	createResult.AssertExpectations(t)
}

func TestOpencodeDispatcherWorkspaceCreateNoID(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	processMock := factory.Process()
	createResult := mocksprocess.NewResult(t)

	expectWorkspaceCreate(processMock, "/tmp/ws/goravel/framework", "Ship issue 123", createResult)
	createResult.EXPECT().Failed().Return(false).Once()
	createResult.EXPECT().Output().Return(`{"name":"x"}`).Once()

	// A JSON reply without an id aborts for the same reason.
	dispatcher := newOpencodeDispatcher(testModels())
	err := dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/framework",
		Agent:  constants.AgentShip,
		Prompt: "do it",
		Title:  "Ship issue 123",
	})
	require.EqualError(t, err, "paseo workspace create returned no workspace id")

	processMock.AssertExpectations(t)
	createResult.AssertExpectations(t)
}

func TestLogDispatch(t *testing.T) {
	create := []string{"paseo", "workspace", "create", "--isolation", "local",
		"--path", "/tmp/ws/goravel/framework", "--title", "Ship issue 123", "--json"}
	run := []string{"paseo", "run", "--provider", "opencode",
		"--workspace", workspaceID, "--no-wait", "--", "do it"}
	untitled := []string{"paseo", "run", "--provider", "opencode", "--no-wait", "--", "do it"}

	// The whole entry is compared, not fragments: the contract is one line per
	// command, in execution order, and an exact match also catches a duplicated
	// or reordered command line that a substring check would let through.
	directory := "\ndirectory: /tmp/ws/goravel/framework"

	tests := []struct {
		name     string
		reason   error
		commands [][]string
		want     string
	}{
		{
			name:     "titled dispatch logs both commands in one entry",
			commands: [][]string{create, run},
			want: directory +
				"\ncommand: " + shellCommand(create) +
				"\ncommand: " + shellCommand(run),
		},
		{
			name:     "untitled dispatch logs only the run",
			commands: [][]string{nil, untitled},
			want:     directory + "\ncommand: " + shellCommand(untitled),
		},
		{
			name:     "failed create logs the command and the reason",
			reason:   errors.New("paseo workspace create failed (exit 1): daemon down"),
			commands: [][]string{create},
			want: directory +
				"\ncommand: " + shellCommand(create) +
				"\nerror: paseo workspace create failed (exit 1): daemon down",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// mock.Factory installs the fake application, and the log facade
			// resolves through MakeLog, so the generated log mock records the
			// single Debug entry a dispatch is allowed.
			factory := mock.Factory()
			logMock := mockslog.NewLog(t)
			factory.App().EXPECT().MakeLog().Return(logMock).Once()

			logMock.EXPECT().Debug("Running paseo:", test.want).Once()

			logDispatch("/tmp/ws/goravel/framework", test.reason, test.commands...)

			logMock.AssertExpectations(t)
		})
	}
}

func TestOpencodeDispatcherDefaults(t *testing.T) {
	models := testModels()
	dispatcher := newOpencodeDispatcher(models)
	require.Equal(t, paseoBin, dispatcher.bin)
	require.Equal(t, "opencode", dispatcher.provider)
	require.Equal(t, models, dispatcher.models)
}

func TestOpencodeDispatcherRunUnknownMode(t *testing.T) {
	// No process expectation: an unknown mode fails before the CLI is touched.
	mock.Factory().Process()

	dispatcher := newOpencodeDispatcher(map[string]string{
		constants.AgentShip: "opencode-go/deepseek-v4.1-flash#high",
	})
	err := dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/goravel",
		Agent:  constants.AgentReview,
		Prompt: "do it",
	})
	require.EqualError(t, err, `no model configured for mode "review"`)
}

func TestOpencodeDispatcherRunEmptyModels(t *testing.T) {
	mock.Factory().Process()

	dispatcher := newOpencodeDispatcher(nil)
	err := dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/goravel",
		Agent:  constants.AgentShip,
		Prompt: "do it",
	})
	require.EqualError(t, err, `no model configured for mode "ship"`)
}

func TestOpencodeDispatcherRunWithoutThinking(t *testing.T) {
	// A value with no `#` produces no `--thinking` flag. The empty title also
	// takes the untitled path: no title → no workspace call, and the strict mock
	// has no create expectation, so an accidental create fails the test.
	factory := mock.Factory()
	factory.Log()
	processMock := factory.Process()
	result := mocksprocess.NewResult(t)

	args := dispatcherArgs(constants.AgentReview, "opencode-go/deepseek-v4.1-flash", "", "", "")

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path("/tmp/ws/goravel/goravel").Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
	result.EXPECT().Failed().Return(false).Once()

	dispatcher := newOpencodeDispatcher(map[string]string{
		constants.AgentReview: "opencode-go/deepseek-v4.1-flash",
	})
	require.NoError(t, dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/goravel",
		Agent:  constants.AgentReview,
		Prompt: "do it",
	}))

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestModelFor(t *testing.T) {
	tests := []struct {
		name         string
		entry        string
		wantModel    string
		wantThinking string
		wantErr      string
	}{
		{name: "model and thinking", entry: "opencode-go/deepseek-v4.1-flash#high", wantModel: "opencode-go/deepseek-v4.1-flash", wantThinking: "high"},
		{name: "model only", entry: "opencode-go/deepseek-v4.1-flash", wantModel: "opencode-go/deepseek-v4.1-flash"},
		{name: "surrounding spaces", entry: " opencode-go/deepseek-v4.1-flash # high ", wantModel: "opencode-go/deepseek-v4.1-flash", wantThinking: "high"},
		{name: "empty thinking after hash", entry: "opencode-go/deepseek-v4.1-flash#", wantModel: "opencode-go/deepseek-v4.1-flash"},
		{name: "missing model before hash", entry: "#high", wantErr: `no model configured for mode "review"`},
		{name: "blank entry", entry: "   ", wantErr: `no model configured for mode "review"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := newOpencodeDispatcher(map[string]string{constants.AgentReview: test.entry})
			model, thinking, err := dispatcher.modelFor(constants.AgentReview)
			if test.wantErr != "" {
				require.EqualError(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantModel, model)
			require.Equal(t, test.wantThinking, thinking)
		})
	}
}

func TestAgentModels(t *testing.T) {
	models := testModels()

	tests := []struct {
		name    string
		value   any
		want    map[string]string
		wantErr string
	}{
		{name: "map of strings", value: models, want: models},
		{name: "nil value", value: nil, wantErr: "agent: agent.models must map every agent to a <model>[#<thinking>] entry"},
		{name: "not a map[string]string", value: "opencode-go/deepseek-v4.1-flash#high", wantErr: "agent: agent.models must map every agent to a <model>[#<thinking>] entry"},
		{
			name:    "map of any",
			value:   map[string]any{constants.AgentShip: "opencode-go/deepseek-v4.1-flash#high", constants.AgentReview: "opencode-go/deepseek-v4.1-flash#high"},
			wantErr: "agent: agent.models must map every agent to a <model>[#<thinking>] entry",
		},
		{
			name:    "agent without model",
			value:   map[string]string{constants.AgentShip: "opencode-go/deepseek-v4.1-flash#high", constants.AgentAnalyze: "opencode-go/deepseek-v4.1-flash#high"},
			wantErr: `agent: agent.models has no model for the "review" agent`,
		},
		{
			name:    "blank agent model",
			value:   map[string]string{constants.AgentShip: "opencode-go/deepseek-v4.1-flash#high", constants.AgentAnalyze: "opencode-go/deepseek-v4.1-flash#high", constants.AgentReview: "  "},
			wantErr: `agent: agent.models has no model for the "review" agent`,
		},
		{
			name:    "entry without model",
			value:   map[string]string{constants.AgentShip: "opencode-go/deepseek-v4.1-flash#high", constants.AgentAnalyze: "opencode-go/deepseek-v4.1-flash#high", constants.AgentReview: "#max"},
			wantErr: `agent: agent.models has no model for the "review" agent`,
		},
		{
			name:    "analyze without model",
			value:   map[string]string{constants.AgentShip: "opencode-go/deepseek-v4.1-flash#high", constants.AgentReview: "opencode-go/deepseek-v4.1-flash#high"},
			wantErr: `agent: agent.models has no model for the "analyze" agent`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := agentModels(test.value)
			if test.wantErr != "" {
				require.EqualError(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}
