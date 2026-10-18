package agent

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	testifymock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/goravel/framework/contracts/process"
	mocksai "github.com/goravel/framework/mocks/ai"
	mocksprocess "github.com/goravel/framework/mocks/process"
	"github.com/goravel/framework/testing/mock"

	"goravel/app/services/agent/dispatch"
)

func TestParseReply(t *testing.T) {
	tests := []struct {
		name         string
		reply        string
		wantAgent    string
		wantRepo     string
		wantRejected string
		wantErr      bool
	}{
		{name: "review", reply: "review", wantAgent: agentReview},
		{name: "ship with spaces", reply: "  SHIP ", wantAgent: agentShip},
		{name: "implement with trailing punctuation", reply: "implement.", wantAgent: agentImplement},
		{name: "review with backticks", reply: "`review`", wantAgent: agentReview},
		{name: "review in bold markdown", reply: "**review**", wantAgent: agentReview},
		{name: "ship in parentheses", reply: "(ship)", wantAgent: agentShip},
		{name: "implement with trailing colon", reply: "implement:", wantAgent: agentImplement},
		{name: "ship with quotes", reply: `"ship"`, wantAgent: agentShip},
		{name: "implement with newline", reply: "implement\n", wantAgent: agentImplement},
		{name: "review with carriage return", reply: "review\r\n", wantAgent: agentReview},
		{
			name:      "agent and repository",
			reply:     "implement goravel/framework",
			wantAgent: agentImplement,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "multiple spaces between tokens",
			reply:     "review   goravel/framework",
			wantAgent: agentReview,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "decorated repository",
			reply:     "review 'goravel/framework'",
			wantAgent: agentReview,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "dotted repository name",
			reply:     "ship goravel/repo.github.io",
			wantAgent: agentShip,
			wantRepo:  "goravel/repo.github.io",
		},
		{
			name:      "dotted name",
			reply:     "ship goravel/.github",
			wantAgent: agentShip,
			wantRepo:  "goravel/.github",
		},
		{
			name:      "hyphen and underscore in names",
			reply:     "ship my-org/my_repo",
			wantAgent: agentShip,
			wantRepo:  "my-org/my_repo",
		},
		{
			// A token that is not a bare owner/name is dropped so the caller
			// falls back to the source repository instead of cloning a bogus
			// target. The dropped token is reported so Route can log it.
			name:         "non owner/name token falls back to source",
			reply:        "ship ../etc",
			wantAgent:    agentShip,
			wantRejected: "../etc",
		},
		{
			name:         "bare word token falls back to source",
			reply:        "ship this issue in the goravel repo",
			wantAgent:    agentShip,
			wantRejected: "this",
		},
		{
			name:      "repository with trailing .git",
			reply:     "ship goravel/framework.git",
			wantAgent: agentShip,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "repository as https URL",
			reply:     "ship https://github.com/goravel/framework",
			wantAgent: agentShip,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "repository as https URL with .git",
			reply:     "ship https://github.com/goravel/framework.git",
			wantAgent: agentShip,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "repository as ssh URL",
			reply:     "ship git@github.com:goravel/framework.git",
			wantAgent: agentShip,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "https URL with trailing slash after .git",
			reply:     "ship https://github.com/goravel/framework.git/",
			wantAgent: agentShip,
			wantRepo:  "goravel/framework",
		},
		{
			name:      "repository with .git and trailing slash",
			reply:     "ship goravel/framework.git/",
			wantAgent: agentShip,
			wantRepo:  "goravel/framework",
		},
		{
			// Only the spellings above are reduced to a bare owner/name; other
			// URL shapes are deliberately dropped so the run falls back to the
			// source repository rather than cloning a bogus target.
			name:         "uppercase scheme",
			reply:        "ship HTTPS://github.com/goravel/framework",
			wantAgent:    agentShip,
			wantRejected: "HTTPS://github.com/goravel/framework",
		},
		{
			name:         "www host",
			reply:        "ship www.github.com/goravel/framework",
			wantAgent:    agentShip,
			wantRejected: "www.github.com/goravel/framework",
		},
		{
			name:         "ssh scheme URL",
			reply:        "ship ssh://git@github.com/goravel/framework",
			wantAgent:    agentShip,
			wantRejected: "ssh://git@github.com/goravel/framework",
		},
		{
			name:         "scheme-less host",
			reply:        "ship github.com/goravel/framework",
			wantAgent:    agentShip,
			wantRejected: "github.com/goravel/framework",
		},
		{
			name:         "URL with a path",
			reply:        "ship https://github.com/goravel/framework/tree/main",
			wantAgent:    agentShip,
			wantRejected: "https://github.com/goravel/framework/tree/main",
		},
		{
			// A dots-only name is not a plausible repository, so it is dropped
			// rather than handed to checkout as a bogus clone target.
			name:         "dots-only name falls back to source",
			reply:        "ship uwu/../..",
			wantAgent:    agentShip,
			wantRejected: "uwu/../..",
		},
		{
			name:         "dots-only token falls back to source",
			reply:        "ship uwu/..",
			wantAgent:    agentShip,
			wantRejected: "uwu/..",
		},
		{
			name:         "empty repository token",
			reply:        "implement .",
			wantAgent:    agentImplement,
			wantRejected: ".",
		},
		{name: "empty", reply: "", wantErr: true},
		{name: "unrecognised", reply: "nonsense", wantErr: true},
		{name: "removed reply token", reply: "plan", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, repository, rejected, err := parseReply(test.reply)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantAgent, agent)
			require.Equal(t, test.wantRepo, repository)
			require.Equal(t, test.wantRejected, rejected)
		})
	}
}

func TestRouterAgentInstructions(t *testing.T) {
	// The prompt is pinned exactly: the reply vocabulary must stay the agent
	// names parseAgent accepts, followed by the repository to work on.
	require.Equal(t,
		"Decide how to handle a GitHub mention. "+
			"Reply with exactly two space-separated tokens: one agent from: "+
			"implement (implements a plan that is already provided, reviews the changes, and opens a pull request), "+
			"review (reviews an existing GitHub pull request and reports findings), "+
			"ship (plans the task first, then implements and drives it through review to a pull request); "+
			"then the repository to work on. "+
			"The Instruction may name a repository, and when it does that repository is the one to work on, "+
			"even when it differs from the Repository line. "+
			"Only when the Instruction names no repository, or names something that is not a bare owner/name, do you use the Repository line. "+
			"For example, with Repository: owner/source and Instruction: ship this issue in owner/target, reply: ship owner/target. "+
			"Reply with only the agent and the repository as a bare owner/name (no scheme, no URL, no trailing .git, no markdown); "+
			"never repeat the instruction text. "+
			"Do not add any other text.",
		routerAgent{}.Instructions())
}

func TestAIRouterRoute(t *testing.T) {
	factory := mock.Factory()
	aiMock := factory.AI()
	conversation := mocksai.NewConversation(t)
	response := mocksai.NewAgentResponse(t)

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(conversation, nil).Once()
	conversation.EXPECT().Prompt("review this change").Return(response, nil).Once()
	response.EXPECT().Text().Return("review goravel/framework").Once()

	router := newAIRouter()
	agent, repository, err := router.Route(context.Background(), "review this change")
	require.NoError(t, err)
	require.Equal(t, agentReview, agent)
	require.Equal(t, "goravel/framework", repository)
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
	response.EXPECT().Text().Return("review this issue").Once()

	router := newAIRouter()
	agent, repository, err := router.Route(context.Background(), "review this change")
	require.NoError(t, err)
	require.Equal(t, agentReview, agent)
	require.Empty(t, repository)
	aiMock.AssertExpectations(t)
}

func TestAIRouterRouteEmptyPrompt(t *testing.T) {
	router := newAIRouter()

	_, _, err := router.Route(context.Background(), "   ")
	require.Error(t, err)
}

func TestAIRouterRouteAIError(t *testing.T) {
	factory := mock.Factory()
	aiMock := factory.AI()

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(nil, errors.New("boom")).Once()

	router := newAIRouter()
	_, _, err := router.Route(context.Background(), "do something")
	require.Error(t, err)
	aiMock.AssertExpectations(t)
}

func TestAIRouterRoutePromptError(t *testing.T) {
	factory := mock.Factory()
	aiMock := factory.AI()
	conversation := mocksai.NewConversation(t)

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(conversation, nil).Once()
	conversation.EXPECT().Prompt("do something").Return(nil, errors.New("provider down")).Once()

	router := newAIRouter()
	_, _, err := router.Route(context.Background(), "do something")
	require.Error(t, err)
	require.Contains(t, err.Error(), "route prompt")
	aiMock.AssertExpectations(t)
}

func TestAIRouterRouteUnrecognisedReply(t *testing.T) {
	factory := mock.Factory()
	aiMock := factory.AI()
	conversation := mocksai.NewConversation(t)
	response := mocksai.NewAgentResponse(t)

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(conversation, nil).Once()
	conversation.EXPECT().Prompt("do something").Return(response, nil).Once()
	response.EXPECT().Text().Return("nonsense").Once()

	router := newAIRouter()
	_, _, err := router.Route(context.Background(), "do something")
	require.Error(t, err)
	aiMock.AssertExpectations(t)
}

// scrubbedEnvArgs returns the `env -i PATH=... HOME=...` prefix the dispatcher
// uses to strip secrets from the agent subprocess.
func scrubbedEnvArgs() []any {
	return []any{"env", "-i", "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
}

func TestOpencodeDispatcherRun(t *testing.T) {
	processMock := mock.Factory().Process()
	result := mocksprocess.NewResult(t)

	args := append(scrubbedEnvArgs(), "opencode", "run", "--agent", "review", "--", "do it")

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path("/tmp/ws/goravel/goravel").Return(processMock).Once()
	processMock.EXPECT().Timeout(opencodeTimeout).Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().OnOutput(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
	result.EXPECT().Failed().Return(false).Once()

	dispatcher := newOpencodeDispatcher()
	require.NoError(t, dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/goravel",
		Agent:  "review",
		Prompt: "do it",
	}))

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestOpencodeDispatcherRunFailure(t *testing.T) {
	processMock := mock.Factory().Process()
	result := mocksprocess.NewResult(t)

	args := append(scrubbedEnvArgs(), "opencode", "run", "--agent", "ship", "--", "do it")

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path("/tmp/ws/goravel/framework").Return(processMock).Once()
	processMock.EXPECT().Timeout(opencodeTimeout).Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	// The dispatcher captures stderr via OnOutput; simulate a line arriving.
	processMock.EXPECT().OnOutput(testifymock.Anything).
		Run(func(handler process.OnOutputFunc) {
			handler(process.OutputTypeStdout, []byte("ignored"))
			handler(process.OutputTypeStderr, []byte("boom"))
		}).
		Return(processMock).
		Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
	result.EXPECT().Failed().Return(true).Once()
	result.EXPECT().ExitCode().Return(2).Once()

	dispatcher := newOpencodeDispatcher()
	err := dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/framework",
		Agent:  "ship",
		Prompt: "do it",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `"boom"`)

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestOpencodeDispatcherRunStartError(t *testing.T) {
	processMock := mock.Factory().Process()
	result := mocksprocess.NewResult(t)

	args := append(scrubbedEnvArgs(), "opencode", "run", "--agent", "review", "--", "do it")

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path("/tmp/ws/goravel/goravel").Return(processMock).Once()
	processMock.EXPECT().Timeout(opencodeTimeout).Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	// No stderr captured, so the start error is reported instead.
	processMock.EXPECT().OnOutput(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
	result.EXPECT().Failed().Return(true).Once()
	result.EXPECT().ExitCode().Return(-1).Once()
	result.EXPECT().Error().Return(errors.New("start failed")).Once()

	dispatcher := newOpencodeDispatcher()
	err := dispatcher.Run(context.Background(), dispatch.Request{
		Dir:    "/tmp/ws/goravel/goravel",
		Agent:  "review",
		Prompt: "do it",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "start failed")

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestOpencodeDispatcherDefaults(t *testing.T) {
	dispatcher := newOpencodeDispatcher()
	require.Equal(t, opencodeBin, dispatcher.bin)
	require.Equal(t, 30*time.Minute, dispatcher.deadline)
}
