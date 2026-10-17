package goraveler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	testifymock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	mockshttp "github.com/goravel/framework/mocks/http/client"
	"github.com/goravel/framework/testing/mock"
)

const (
	relayEventsBaseURL = "http://relay.test/webhooks/events"

	mentionOnlyEnvelope = `{
	"data": [
		{
			"id": 1,
			"delivery_id": "d-1",
			"event": "issue_comment",
			"action": "created",
			"repository": "goravel/goravel",
			"sender": "bowen",
			"payload": "{\"number\":123,\"comment\":{\"id\":24680,\"body\":\"@goraveler review\"}}"
		}
	],
	"total": 1,
	"page": 1,
	"limit": 50
}`
)

var relayQueryParams = map[string]string{
	"platform": "github",
	"page":     "1",
	"limit":    "50",
}

type fakeRouter struct {
	agent string
	err   error
	got   []string
}

func (f *fakeRouter) AgentFor(_ context.Context, instruction string) (string, error) {
	f.got = append(f.got, instruction)
	return f.agent, f.err
}

type dispatchCall struct {
	repoDir string
	agent   string
	prompt  string
}

type fakeDispatcher struct {
	err   error
	calls []dispatchCall
}

func (f *fakeDispatcher) Run(_ context.Context, req dispatchRequest) error {
	f.calls = append(f.calls, dispatchCall{repoDir: req.dir, agent: req.agent, prompt: req.prompt})
	return f.err
}

type fakeRetry struct {
	exceeded map[string]bool
	resets   []string
}

func (f *fakeRetry) RecordFailure(key string) bool {
	return f.exceeded[key]
}

func (f *fakeRetry) Reset(key string) {
	f.resets = append(f.resets, key)
}

type fakeHandleStore struct {
	handled map[string]bool
	marked  []string
}

func (f *fakeHandleStore) Has(key string) bool {
	return f.handled[key]
}

func (f *fakeHandleStore) Mark(key string) {
	if f.handled == nil {
		f.handled = map[string]bool{}
	}
	f.handled[key] = true
	f.marked = append(f.marked, key)
}

// testConfig groups the relay-facing settings used to build a test Service.
type testConfig struct {
	baseURL        string
	apiToken       string
	workspace      string
	allowedSenders map[string]struct{}
}

// newTestService wires a Service from explicit dependencies, mirroring NewService
// without the configuration lookup.
func newTestService(router agentRouter, dispatcher dispatcher, retry retryTracker, handles handleStore, checkouts checkouter, cfg testConfig) *Service {
	return &Service{
		router:         router,
		dispatcher:     dispatcher,
		checkouts:      checkouts,
		retry:          retry,
		handles:        handles,
		apiToken:       cfg.apiToken,
		workspaceRoot:  resolveWorkspace(cfg.workspace),
		eventsURL:      strings.TrimRight(cfg.baseURL, "/"),
		allowedSenders: cfg.allowedSenders,
	}
}

// relayConfig builds the test relay configuration.
func relayConfig(apiToken, workspace string) testConfig {
	return testConfig{baseURL: relayEventsBaseURL, apiToken: apiToken, workspace: workspace, allowedSenders: parseSenders("bowen")}
}

// relayConfigSenders is relayConfig with a sender allowlist.
func relayConfigSenders(apiToken, workspace, senders string) testConfig {
	cfg := relayConfig(apiToken, workspace)
	cfg.allowedSenders = parseSenders(senders)

	return cfg
}

// checkoutCall records one Ensure invocation.
type checkoutCall struct{ dir, repository string }

// fakeCheckouter records Ensure calls and returns a canned error.
type fakeCheckouter struct {
	err   error
	calls []checkoutCall
}

func (f *fakeCheckouter) Ensure(_ context.Context, dir, repository string) error {
	f.calls = append(f.calls, checkoutCall{dir: dir, repository: repository})
	return f.err
}

// testService builds a Service with a fresh fake handle store.
func testService(router agentRouter, dispatcher dispatcher, retry retryTracker) *Service {
	return newTestService(router, dispatcher, retry, &fakeHandleStore{}, &fakeCheckouter{}, relayConfig("token", "/tmp/ws"))
}

// deleteRecorder collects delete URLs across the poller's worker pool.
type deleteRecorder struct {
	mu   sync.Mutex
	urls []string
}

func (r *deleteRecorder) add(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.urls = append(r.urls, url)
}

func (r *deleteRecorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls...)
}

func TestServicePollDispatchesMentionsAndDeletes(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	body := `{"data":[
		{"id":1,"delivery_id":"d-1","sender":"bowen","event":"issue_comment","action":"created","repository":"goravel/goravel","payload":"{\"number\":123,\"comment\":{\"id\":24680,\"body\":\"@goraveler review\"}}"},
		{"id":2,"delivery_id":"d-2","sender":"bowen","event":"issues","action":"opened","repository":"goravel/framework","payload":"{\"number\":5,\"issue\":{\"number\":5,\"body\":\"nothing to see\"}}"}
	],"total":2,"page":1,"limit":50}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(body, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Times(2)
	deleteResp.EXPECT().Failed().Return(false).Times(2)

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(3)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(3)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/2", nil).Return(deleteResp, nil).Once()

	router := &fakeRouter{agent: agentReview}
	dispatcher := &fakeDispatcher{}
	retry := &fakeRetry{exceeded: map[string]bool{}}
	checkouts := &fakeCheckouter{}

	service := newTestService(router, dispatcher, retry, &fakeHandleStore{}, checkouts, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	require.Equal(t, []string{"review"}, router.got)
	require.Len(t, dispatcher.calls, 1)
	require.Equal(t, agentReview, dispatcher.calls[0].agent)
	require.Equal(t, "/tmp/ws/goravel/goravel", dispatcher.calls[0].repoDir)
	require.Contains(t, dispatcher.calls[0].prompt, "Repository: goravel/goravel")

	require.Equal(t, []checkoutCall{{dir: "/tmp/ws/goravel/goravel", repository: "goravel/goravel"}}, checkouts.calls)

	require.Equal(t, []string{"d-1"}, retry.resets)

	httpMock.AssertExpectations(t)
}

func TestServicePollCloneError(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(mentionOnlyEnvelope, nil).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Once()
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Once()
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()

	dispatcher := &fakeDispatcher{}
	retry := &fakeRetry{exceeded: map[string]bool{"d-1": false}}
	handles := &fakeHandleStore{handled: map[string]bool{}}
	checkouts := &fakeCheckouter{err: errors.New("clone failed")}

	service := newTestService(&fakeRouter{agent: agentReview}, dispatcher, retry, handles, checkouts, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	// A checkout failure keeps the event for a retry and never dispatches.
	require.Empty(t, dispatcher.calls)
	require.Empty(t, handles.marked)
	require.Equal(t, []checkoutCall{{dir: "/tmp/ws/goravel/goravel", repository: "goravel/goravel"}}, checkouts.calls)
	httpMock.AssertNotCalled(t, "Delete", testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}

func TestServicePollKeepsFailedEventWhenRetryNotExceeded(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(mentionOnlyEnvelope, nil).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Once()
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Once()
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()

	router := &fakeRouter{agent: agentReview}
	dispatcher := &fakeDispatcher{err: errors.New("opencode failed")}
	retry := &fakeRetry{exceeded: map[string]bool{"d-1": false}}
	handles := &fakeHandleStore{handled: map[string]bool{}}

	service := newTestService(router, dispatcher, retry, handles, &fakeCheckouter{}, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	httpMock.AssertNotCalled(t, "Delete", testifymock.Anything, testifymock.Anything)
	require.Empty(t, retry.resets)
	require.Empty(t, handles.marked)
	httpMock.AssertExpectations(t)
}

func TestServicePollKeepsEventWhenRoutingFails(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(mentionOnlyEnvelope, nil).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Once()
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Once()
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()

	router := &fakeRouter{err: errors.New("router exploded")}
	dispatcher := &fakeDispatcher{}
	retry := &fakeRetry{exceeded: map[string]bool{"d-1": false}}
	handles := &fakeHandleStore{handled: map[string]bool{}}

	service := newTestService(router, dispatcher, retry, handles, &fakeCheckouter{}, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	require.Empty(t, dispatcher.calls)
	require.Empty(t, retry.resets)
	require.Empty(t, handles.marked)
	httpMock.AssertNotCalled(t, "Delete", testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}

func TestServicePollDeletesWhenRetryExceeded(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(mentionOnlyEnvelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	router := &fakeRouter{agent: agentReview}
	dispatcher := &fakeDispatcher{err: errors.New("opencode failed")}
	retry := &fakeRetry{exceeded: map[string]bool{"d-1": true}}
	handles := &fakeHandleStore{handled: map[string]bool{}}

	service := newTestService(router, dispatcher, retry, handles, &fakeCheckouter{}, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	require.Empty(t, retry.resets)
	// A gave-up event is marked so a failed acknowledgement cannot replay it.
	require.Equal(t, []string{"d-1"}, handles.marked)
	httpMock.AssertExpectations(t)
}

func TestServicePollHandlesDeleteError(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(mentionOnlyEnvelope, nil).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).
		Return(nil, errors.New("connection reset")).Once()

	router := &fakeRouter{agent: agentReview}
	dispatcher := &fakeDispatcher{}
	retry := &fakeRetry{exceeded: map[string]bool{}}

	service := testService(router, dispatcher, retry)
	service.Poll(context.Background())

	require.Equal(t, []string{"d-1"}, retry.resets)
	httpMock.AssertExpectations(t)
}

func TestServicePollHandlesDeleteFailureStatus(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(mentionOnlyEnvelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(true).Once()
	deleteResp.EXPECT().Status().Return(500).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	router := &fakeRouter{agent: agentReview}
	dispatcher := &fakeDispatcher{}
	retry := &fakeRetry{exceeded: map[string]bool{}}

	service := testService(router, dispatcher, retry)
	service.Poll(context.Background())

	require.Equal(t, []string{"d-1"}, retry.resets)
	httpMock.AssertExpectations(t)
}

func TestServicePollDeletesFullPageThroughPool(t *testing.T) {
	const count = deleteConcurrency + 3

	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	events := make([]string, 0, count)
	want := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		events = append(events, fmt.Sprintf(
			`{"id":%d,"delivery_id":"d-%d","sender":"bowen","event":"issues","action":"opened","repository":"goravel/framework","payload":"{\"number\":%d,\"issue\":{\"number\":%d,\"body\":\"no mention\"}}"}`,
			i, i, i, i,
		))
		want = append(want, fmt.Sprintf("http://relay.test/webhooks/events/%d", i))
	}
	body := `{"data":[` + strings.Join(events, ",") + `],"total":` + strconv.Itoa(count) + `,"page":1,"limit":50}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(body, nil).Once()

	released := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }
	t.Cleanup(release)
	arrived := make(chan struct{}, count)
	var inFlight, peak int64

	// Gate each delete inside deleteEvent (Body runs while the pool slot is
	// held), so the in-flight count reflects the pool's concurrency.
	deleteResp.EXPECT().Body().
		Run(func() {
			current := atomic.AddInt64(&inFlight, 1)
			for {
				observed := atomic.LoadInt64(&peak)
				if current <= observed || atomic.CompareAndSwapInt64(&peak, observed, current) {
					break
				}
			}
			arrived <- struct{}{}
			<-released
			atomic.AddInt64(&inFlight, -1)
		}).
		Return("", nil).
		Times(count)
	deleteResp.EXPECT().Failed().Return(false).Times(count)

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(count + 1)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(count + 1)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	for _, url := range want {
		httpMock.EXPECT().Delete(url, nil).Return(deleteResp, nil).Once()
	}

	service := testService(&fakeRouter{}, &fakeDispatcher{}, &fakeRetry{})

	done := make(chan struct{})
	go func() {
		service.Poll(context.Background())
		close(done)
	}()

	// Wait until the pool is saturated: exactly deleteConcurrency deletes hold
	// the gate at once. A sequential pool would never fill it (deadlock); an
	// unbounded pool would exceed it.
	for i := 0; i < deleteConcurrency; i++ {
		select {
		case <-arrived:
		case <-time.After(30 * time.Second):
			release()
			<-done
			t.Fatalf("only %d deletes reached the pool (want %d)", i, deleteConcurrency)
		}
	}

	// Sample the in-flight peak over a short window; an unbounded pool would
	// push it above the limit here even if the extra goroutines are slow to start.
	for i := 0; i < 50; i++ {
		assert.LessOrEqual(t, atomic.LoadInt64(&peak), int64(deleteConcurrency))
		time.Sleep(2 * time.Millisecond)
	}
	assert.Equal(t, int64(deleteConcurrency), atomic.LoadInt64(&peak))

	release()
	<-done

	httpMock.AssertExpectations(t)
}

func TestServicePollStopsOnFetchError(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Once()
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Once()
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(nil, errors.New("network down")).Once()

	dispatcher := &fakeDispatcher{}
	service := testService(&fakeRouter{agent: agentReview}, dispatcher, &fakeRetry{})
	service.Poll(context.Background())

	require.Empty(t, dispatcher.calls)
	httpMock.AssertNotCalled(t, "Delete", testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}

func TestServicePollStopsOnFailedStatus(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(true).Once()
	getResp.EXPECT().Status().Return(401).Once()
	getResp.EXPECT().Body().Return("", nil).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Once()
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Once()
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()

	dispatcher := &fakeDispatcher{}
	service := testService(&fakeRouter{agent: agentReview}, dispatcher, &fakeRetry{})
	service.Poll(context.Background())

	require.Empty(t, dispatcher.calls)
	httpMock.AssertNotCalled(t, "Delete", testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}

func TestServicePollStopsOnBodyError(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return("", errors.New("read failed")).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Once()
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Once()
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()

	dispatcher := &fakeDispatcher{}
	service := testService(&fakeRouter{agent: agentReview}, dispatcher, &fakeRetry{})
	service.Poll(context.Background())

	require.Empty(t, dispatcher.calls)
	httpMock.AssertNotCalled(t, "Delete", testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}

func TestServicePollStopsOnInvalidEnvelope(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return("not json", nil).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Once()
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Once()
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()

	dispatcher := &fakeDispatcher{}
	service := testService(&fakeRouter{agent: agentReview}, dispatcher, &fakeRetry{})
	service.Poll(context.Background())

	require.Empty(t, dispatcher.calls)
	httpMock.AssertNotCalled(t, "Delete", testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}

func TestNewServiceReadsConfig(t *testing.T) {
	factory := mock.Factory()
	configMock := factory.Config()

	configMock.EXPECT().GetString("goraveler.base_url").Return("http://relay.test/webhooks/events").Once()
	configMock.EXPECT().GetString("goraveler.api_token").Return("secret").Once()
	configMock.EXPECT().GetString("goraveler.workspace").Return("/srv/code").Once()
	configMock.EXPECT().GetString("goraveler.allowed_senders").Return("bowen, alice").Once()

	service, err := NewService()
	require.NoError(t, err)
	require.NotNil(t, service)

	require.Equal(t, "http://relay.test/webhooks/events", service.eventsURL)
	require.Equal(t, "secret", service.apiToken)
	require.Equal(t, "/srv/code", service.workspaceRoot)
	require.IsType(t, &cacheHandleStore{}, service.handles)
	require.IsType(t, &gitCheckouter{}, service.checkouts)
	require.True(t, service.allowed("bowen"))
	require.True(t, service.allowed("alice"))
	require.False(t, service.allowed("mallory"))

	configMock.AssertExpectations(t)
}

func TestNewServiceRejectsEmptySenders(t *testing.T) {
	factory := mock.Factory()
	configMock := factory.Config()

	configMock.EXPECT().GetString("goraveler.allowed_senders").Return("  ,  ").Once()

	service, err := NewService()
	require.Nil(t, service)
	require.EqualError(t, err, "goraveler: GORAVELER_ALLOWED_SENDERS must not be empty")

	configMock.AssertExpectations(t)
}

func TestServiceBaseURLTrimsTrailingSlash(t *testing.T) {
	service := newTestService(&fakeRouter{}, &fakeDispatcher{}, &fakeRetry{}, &fakeHandleStore{}, &fakeCheckouter{}, testConfig{
		baseURL:        "http://relay.test/webhooks/events/",
		apiToken:       "token",
		workspace:      "/tmp/ws",
		allowedSenders: parseSenders("bowen"),
	})
	require.Equal(t, "http://relay.test/webhooks/events", service.eventsURL)
}

func TestServiceAllowedSenders(t *testing.T) {
	service := newTestService(&fakeRouter{}, &fakeDispatcher{}, &fakeRetry{}, &fakeHandleStore{}, &fakeCheckouter{}, relayConfigSenders("token", "/tmp/ws", "Bowen, alice"))

	require.True(t, service.allowed("bowen"))
	require.True(t, service.allowed("BOWEN"))
	require.True(t, service.allowed("Alice"))
	require.False(t, service.allowed("mallory"))
}

func TestServicePollSkipsDisallowedSender(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(mentionOnlyEnvelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	router := &fakeRouter{agent: agentReview}
	dispatcher := &fakeDispatcher{}
	retry := &fakeRetry{exceeded: map[string]bool{}}

	service := newTestService(router, dispatcher, retry, &fakeHandleStore{}, &fakeCheckouter{}, relayConfigSenders("token", "/tmp/ws", "alice"))
	service.Poll(context.Background())

	// The skipped event is still acknowledged so it does not block the page.
	require.Empty(t, dispatcher.calls)
	require.Empty(t, retry.resets)
	httpMock.AssertExpectations(t)
}

func TestServicePollDispatchesAllowedSender(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	envelope := `{"data":[{"id":1,"delivery_id":"d-1","sender":"bowen","event":"issue_comment","action":"created","repository":"goravel/goravel","payload":"{\"number\":123,\"comment\":{\"id\":24680,\"body\":\"@goraveler review\"}}"}],"total":1,"page":1,"limit":50}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(envelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	router := &fakeRouter{agent: agentReview}
	dispatcher := &fakeDispatcher{}
	retry := &fakeRetry{exceeded: map[string]bool{}}

	service := newTestService(router, dispatcher, retry, &fakeHandleStore{}, &fakeCheckouter{}, relayConfigSenders("token", "/tmp/ws", "bowen"))
	service.Poll(context.Background())

	require.Len(t, dispatcher.calls, 1)
	require.Equal(t, []string{"d-1"}, retry.resets)
	httpMock.AssertExpectations(t)
}

func TestServicePollSkipsAlreadyHandledEvent(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(mentionOnlyEnvelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	handles := &fakeHandleStore{handled: map[string]bool{"d-1": true}}
	dispatcher := &fakeDispatcher{}
	retry := &fakeRetry{exceeded: map[string]bool{}}

	service := newTestService(&fakeRouter{agent: agentReview}, dispatcher, retry, handles, &fakeCheckouter{}, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	// The already-handled event is skipped but still acknowledged.
	require.Empty(t, dispatcher.calls)
	require.Empty(t, retry.resets)
	httpMock.AssertExpectations(t)
}

func TestServicePollMarksHandledOnSuccess(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(mentionOnlyEnvelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	handles := &fakeHandleStore{handled: map[string]bool{}}
	retry := &fakeRetry{exceeded: map[string]bool{}}

	service := newTestService(&fakeRouter{agent: agentReview}, &fakeDispatcher{}, retry, handles, &fakeCheckouter{}, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	require.Equal(t, []string{"d-1"}, handles.marked)
	require.Equal(t, []string{"d-1"}, retry.resets)
	httpMock.AssertExpectations(t)
}

func TestServicePollHandlesDeleteBodyError(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(mentionOnlyEnvelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", errors.New("read failed")).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	retry := &fakeRetry{exceeded: map[string]bool{}}

	service := testService(&fakeRouter{agent: agentReview}, &fakeDispatcher{}, retry)
	service.Poll(context.Background())

	require.Equal(t, []string{"d-1"}, retry.resets)
	httpMock.AssertExpectations(t)
}

func TestServicePollDeletesEventWithInvalidPayload(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	env := `{"data":[{"id":1,"delivery_id":"d-1","event":"issue_comment","action":"created","repository":"goravel/goravel","payload":"not json"}],"total":1,"page":1,"limit":50}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(env, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	dispatcher := &fakeDispatcher{}
	service := testService(&fakeRouter{agent: agentReview}, dispatcher, &fakeRetry{exceeded: map[string]bool{}})
	service.Poll(context.Background())

	// The undecodable event is not a mention, so it is acknowledged and deleted.
	require.Empty(t, dispatcher.calls)
	httpMock.AssertExpectations(t)
}

func TestServicePollSkipsNonActionableAction(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	// A mention in the issue body repeated by a `labeled` action must not dispatch.
	env := `{"data":[{"id":1,"delivery_id":"d-1","event":"issues","action":"labeled","repository":"goravel/goravel","payload":"{\"number\":123,\"issue\":{\"number\":123,\"body\":\"@goraveler review\"}}"}],"total":1,"page":1,"limit":50}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(env, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	dispatcher := &fakeDispatcher{}
	service := testService(&fakeRouter{agent: agentReview}, dispatcher, &fakeRetry{exceeded: map[string]bool{}})
	service.Poll(context.Background())

	require.Empty(t, dispatcher.calls)
	httpMock.AssertExpectations(t)
}

func TestRetryKey(t *testing.T) {
	require.Equal(t, "d-1", retryKey(&event{ID: 7, DeliveryID: "d-1"}))
	require.Equal(t, "7", retryKey(&event{ID: 7}))
}
