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

func TestParseRoute(t *testing.T) {
	tests := []struct {
		name      string
		reply     string
		wantAgent string
		wantRepo  string
		wantErr   bool
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
			// There is no pattern gate: an escaping repository is passed
			// through, with only surrounding decoration trimmed. The path
			// safety net is checkoutName at checkout time.
			name:      "escaping repository is not validated",
			reply:     "ship ../etc",
			wantAgent: agentShip,
			wantRepo:  "/etc",
		},
		{
			name:      "empty repository token",
			reply:     "implement .",
			wantAgent: agentImplement,
		},
		{name: "empty", reply: "", wantErr: true},
		{name: "unrecognised", reply: "nonsense", wantErr: true},
		{name: "removed reply token", reply: "plan", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, repository, err := parseRoute(test.reply)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantAgent, agent)
			require.Equal(t, test.wantRepo, repository)
		})
	}
}

func TestRouterAgentInstructions(t *testing.T) {
	// The prompt is pinned exactly: the reply vocabulary must stay the agent
	// names parseAgent accepts, followed by the repository to work on.
	require.Equal(t,
		"Decide how to handle a GitHub mention. The Repository line and links are the source; "+
			"reply with exactly two space-separated tokens: one agent from: "+
			"implement (implements a plan that is already provided, reviews the changes, and opens a pull request), "+
			"review (reviews an existing GitHub pull request and reports findings), "+
			"ship (plans the task first, then implements and drives it through review to a pull request); "+
			"then the repository to work on as owner/name. Do not add any other text.",
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
