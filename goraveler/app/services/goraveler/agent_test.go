package goraveler

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

	"goravel/app/services/goraveler/dispatch"
)

func TestParseAgent(t *testing.T) {
	tests := []struct {
		name    string
		reply   string
		want    string
		wantErr bool
	}{
		{name: "review", reply: "review", want: agentReview},
		{name: "plan with spaces", reply: "  PLAN ", want: agentPlan},
		{name: "implement with trailing punctuation", reply: "implement.", want: agentImplement},
		{name: "review with backticks", reply: "`review`", want: agentReview},
		{name: "review in bold markdown", reply: "**review**", want: agentReview},
		{name: "plan in parentheses", reply: "(plan)", want: agentPlan},
		{name: "implement with trailing colon", reply: "implement:", want: agentImplement},
		{name: "plan with quotes", reply: `"plan"`, want: agentPlan},
		{name: "implement with newline", reply: "implement\n", want: agentImplement},
		{name: "review with carriage return", reply: "review\r\n", want: agentReview},
		{name: "empty", reply: "", wantErr: true},
		{name: "unrecognised", reply: "nonsense", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseAgent(test.reply)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestRouterAgentInstructions(t *testing.T) {
	prompt := routerAgent{}.Instructions()

	require.NotEmpty(t, prompt)
	require.Contains(t, prompt, agentReview)
	require.Contains(t, prompt, agentPlan)
	require.Contains(t, prompt, agentImplement)
}

func TestAIRouterAgentFor(t *testing.T) {
	factory := mock.Factory()
	aiMock := factory.AI()
	conversation := mocksai.NewConversation(t)
	response := mocksai.NewAgentResponse(t)

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(conversation, nil).Once()
	conversation.EXPECT().Prompt("review this change").Return(response, nil).Once()
	response.EXPECT().Text().Return("review").Once()

	router := newAIRouter()
	agent, err := router.AgentFor(context.Background(), "review this change")
	require.NoError(t, err)
	require.Equal(t, agentReview, agent)
	aiMock.AssertExpectations(t)
}

func TestAIRouterAgentForEmptyInstruction(t *testing.T) {
	router := newAIRouter()

	_, err := router.AgentFor(context.Background(), "   ")
	require.Error(t, err)
}

func TestAIRouterAgentForAIError(t *testing.T) {
	factory := mock.Factory()
	aiMock := factory.AI()

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(nil, errors.New("boom")).Once()

	router := newAIRouter()
	_, err := router.AgentFor(context.Background(), "do something")
	require.Error(t, err)
	aiMock.AssertExpectations(t)
}

func TestAIRouterAgentForPromptError(t *testing.T) {
	factory := mock.Factory()
	aiMock := factory.AI()
	conversation := mocksai.NewConversation(t)

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(conversation, nil).Once()
	conversation.EXPECT().Prompt("do something").Return(nil, errors.New("provider down")).Once()

	router := newAIRouter()
	_, err := router.AgentFor(context.Background(), "do something")
	require.Error(t, err)
	require.Contains(t, err.Error(), "route instruction")
	aiMock.AssertExpectations(t)
}

func TestAIRouterAgentForUnrecognisedReply(t *testing.T) {
	factory := mock.Factory()
	aiMock := factory.AI()
	conversation := mocksai.NewConversation(t)
	response := mocksai.NewAgentResponse(t)

	aiMock.EXPECT().WithContext(testifymock.Anything).Return(aiMock).Once()
	aiMock.EXPECT().Agent(routerAgent{}).Return(conversation, nil).Once()
	conversation.EXPECT().Prompt("do something").Return(response, nil).Once()
	response.EXPECT().Text().Return("nonsense").Once()

	router := newAIRouter()
	_, err := router.AgentFor(context.Background(), "do something")
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

	args := append(scrubbedEnvArgs(), "opencode", "run", "--agent", "plan", "--", "do it")

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
		Agent:  "plan",
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
