package commands

import (
	"net/http"
	"testing"

	testifymock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	mockshttp "github.com/goravel/framework/mocks/http/client"
	"github.com/goravel/framework/testing/mock"
)

func TestPollEventsHandle(t *testing.T) {
	factory := mock.Factory()
	factory.Log()

	configMock := factory.Config()
	configMock.EXPECT().GetString("agent.base_url").Return("http://relay.test/webhooks/events").Once()
	configMock.EXPECT().GetString("agent.api_token").Return("token").Once()
	configMock.EXPECT().GetString("agent.workspace").Return("/tmp/ws").Once()
	configMock.EXPECT().GetString("agent.allowed_senders").Return("bowen").Once()
	configMock.EXPECT().Get("agent.models").Return(map[string]string{
		"ship":      "opencode-go/deepseek-v4.1-flash#high",
		"implement": "opencode-go/deepseek-v4.1-flash#high",
		"review":    "opencode-go/deepseek-v4.1-flash#high",
	}).Once()
	configMock.EXPECT().GetString("agent.mention").Return("@goravel-agent").Once()
	configMock.EXPECT().GetString("agent.github_token").Return("").Once()

	httpMock := factory.Http()
	httpMock.EXPECT().HttpClient().Return(&http.Client{}).Once()

	getResp := mockshttp.NewResponse(t)
	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(`{"data":[],"total":0,"page":1,"limit":50}`, nil).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Once()
	httpMock.EXPECT().WithQueryParameters(testifymock.Anything).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader("X-Api-Token", "token").Return(httpMock).Once()
	httpMock.EXPECT().Get("http://relay.test/webhooks/events").Return(getResp, nil).Once()

	command := &PollEvents{}
	require.NoError(t, command.Handle(nil))

	configMock.AssertExpectations(t)
	httpMock.AssertExpectations(t)
}

func TestPollEventsHandleRejectsEmptySenders(t *testing.T) {
	factory := mock.Factory()
	factory.Log()

	configMock := factory.Config()
	configMock.EXPECT().GetString("agent.allowed_senders").Return("").Once()

	// NewService returns before any HTTP call when the allowlist is empty, so no
	// HTTP mock is registered: touching the client would fail this test.
	command := &PollEvents{}
	require.EqualError(t, command.Handle(nil), "agent: AGENT_ALLOWED_SENDERS must not be empty")

	configMock.AssertExpectations(t)
}
