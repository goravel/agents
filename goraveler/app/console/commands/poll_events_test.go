package commands

import (
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
	configMock.EXPECT().GetString("goraveler.base_url").Return("http://relay.test/webhooks/events").Once()
	configMock.EXPECT().GetString("goraveler.api_token").Return("token").Once()
	configMock.EXPECT().GetString("goraveler.workspace").Return("/tmp/ws").Once()
	configMock.EXPECT().GetString("goraveler.allowed_senders").Return("bowen").Once()

	httpMock := factory.Http()
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
