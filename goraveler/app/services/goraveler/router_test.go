package goraveler

import (
	"context"
	"errors"
	"testing"

	testifymock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	mocksai "github.com/goravel/framework/mocks/ai"
	"github.com/goravel/framework/testing/mock"
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
