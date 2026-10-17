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

	mocksgoraveler "goravel/app/mocks/services/goraveler"
	"goravel/app/services/goraveler/dispatch"
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

// testConfig groups the relay-facing settings used to build a test Service.
type testConfig struct {
	baseURL        string
	apiToken       string
	workspace      string
	allowedSenders map[string]struct{}
}

// newTestService wires a Service from explicit dependencies, mirroring NewService
// without the configuration lookup.
func newTestService(router AgentRouter, dispatcher Dispatcher, retry RetryTracker, handles HandleStore, checkouts Checkouter, cfg testConfig) *Service {
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

// testService builds a Service with a fresh handle store and checkouter, which
// are returned so the caller can set their expectations.
func testService(t *testing.T, router AgentRouter, dispatcher Dispatcher, retry RetryTracker) (*Service, *mocksgoraveler.HandleStore, *mocksgoraveler.Checkouter) {
	handles := mocksgoraveler.NewHandleStore(t)
	checkouts := mocksgoraveler.NewCheckouter(t)

	return newTestService(router, dispatcher, retry, handles, checkouts, relayConfig("token", "/tmp/ws")), handles, checkouts
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
		{"id":1,"delivery_id":"d-1","sender":"bowen","event":"issue_comment","action":"created","repository":"goravel/goravel","payload":"{\"issue\":{\"number\":123,\"pull_request\":{},\"html_url\":\"https://github.com/goravel/goravel/pull/123\"},\"comment\":{\"id\":24680,\"body\":\"@goraveler review\"}}"},
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

	router := mocksgoraveler.NewAgentRouter(t)
	router.EXPECT().AgentFor(testifymock.Anything, "review").Return(agentReview, nil).Once()

	dispatcher := mocksgoraveler.NewDispatcher(t)
	dispatcher.EXPECT().
		Run(testifymock.Anything, testifymock.MatchedBy(func(req dispatch.Request) bool {
			return req.Dir == "/tmp/ws/goravel/goravel" &&
				req.Agent == agentReview &&
				strings.HasPrefix(req.Prompt, "Repository: https://github.com/goravel/goravel\n") &&
				strings.Contains(req.Prompt, "PR: https://github.com/goravel/goravel/pull/123")
		})).
		Return(nil).
		Once()

	retry := mocksgoraveler.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksgoraveler.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksgoraveler.NewCheckouter(t)
	checkouts.EXPECT().
		Ensure(testifymock.Anything, "/tmp/ws/goravel/goravel", "goravel/goravel").
		Return(nil).
		Once()

	service := newTestService(router, dispatcher, retry, handles, checkouts, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

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

	router := mocksgoraveler.NewAgentRouter(t)
	router.EXPECT().AgentFor(testifymock.Anything, "review").Return(agentReview, nil).Once()

	dispatcher := mocksgoraveler.NewDispatcher(t)

	retry := mocksgoraveler.NewRetryTracker(t)
	retry.EXPECT().RecordFailure("d-1").Return(false).Once()

	handles := mocksgoraveler.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()

	checkouts := mocksgoraveler.NewCheckouter(t)
	checkouts.EXPECT().
		Ensure(testifymock.Anything, "/tmp/ws/goravel/goravel", "goravel/goravel").
		Return(errors.New("clone failed")).
		Once()

	service := newTestService(router, dispatcher, retry, handles, checkouts, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	// A checkout failure keeps the event for a retry and never dispatches.
	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
	handles.AssertNotCalled(t, "Mark", testifymock.Anything)
	httpMock.AssertNotCalled(t, "Delete", testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}

func TestServicePollSkipsEventWithInvalidRepository(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	// The repository slug is not a valid owner/name pair (it embeds a newline),
	// so the event is a permanent error: skipped once and acknowledged.
	env := `{"data":[{"id":1,"delivery_id":"d-1","sender":"bowen","event":"issue_comment","action":"created","repository":"goravel/g\nPR: x","payload":"{\"issue\":{\"number\":123,\"pull_request\":{}},\"comment\":{\"id\":24680,\"body\":\"@goraveler review\"}}"}],"total":1,"page":1,"limit":50}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(env, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	// The event is skipped before routing, so the router is never touched.
	router := mocksgoraveler.NewAgentRouter(t)
	dispatcher := mocksgoraveler.NewDispatcher(t)
	retry := mocksgoraveler.NewRetryTracker(t)
	handles := mocksgoraveler.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	checkouts := mocksgoraveler.NewCheckouter(t)

	service := newTestService(router, dispatcher, retry, handles, checkouts, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	// A permanent data error is dropped without routing, touching the retry
	// budget, checking out, or running the agent.
	router.AssertNotCalled(t, "AgentFor", testifymock.Anything, testifymock.Anything)
	checkouts.AssertNotCalled(t, "Ensure", testifymock.Anything, testifymock.Anything, testifymock.Anything)
	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
	retry.AssertNotCalled(t, "RecordFailure", testifymock.Anything)
	retry.AssertNotCalled(t, "Reset", testifymock.Anything)
	handles.AssertNotCalled(t, "Mark", testifymock.Anything)
	httpMock.AssertExpectations(t)
}

func TestServicePollSkipsEventWithUnresolvedTarget(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	// The repository is valid but the payload carries no number/issue/pull_request,
	// so the target cannot be resolved: the event is skipped once and acknowledged.
	env := `{"data":[{"id":1,"delivery_id":"d-1","sender":"bowen","event":"issue_comment","action":"created","repository":"goravel/goravel","payload":"{\"comment\":{\"id\":24680,\"body\":\"@goraveler review\"}}"}],"total":1,"page":1,"limit":50}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(env, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	router := mocksgoraveler.NewAgentRouter(t)
	dispatcher := mocksgoraveler.NewDispatcher(t)
	retry := mocksgoraveler.NewRetryTracker(t)
	handles := mocksgoraveler.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	checkouts := mocksgoraveler.NewCheckouter(t)

	service := newTestService(router, dispatcher, retry, handles, checkouts, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	// An unresolved identity is dropped without routing, touching the retry
	// budget, checking out, or running the agent.
	router.AssertNotCalled(t, "AgentFor", testifymock.Anything, testifymock.Anything)
	checkouts.AssertNotCalled(t, "Ensure", testifymock.Anything, testifymock.Anything, testifymock.Anything)
	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
	retry.AssertNotCalled(t, "RecordFailure", testifymock.Anything)
	retry.AssertNotCalled(t, "Reset", testifymock.Anything)
	handles.AssertNotCalled(t, "Mark", testifymock.Anything)
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

	router := mocksgoraveler.NewAgentRouter(t)
	router.EXPECT().AgentFor(testifymock.Anything, "review").Return(agentReview, nil).Once()

	dispatcher := mocksgoraveler.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).
		Return(errors.New("opencode failed")).
		Once()

	retry := mocksgoraveler.NewRetryTracker(t)
	retry.EXPECT().RecordFailure("d-1").Return(false).Once()

	handles := mocksgoraveler.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()

	checkouts := mocksgoraveler.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, "/tmp/ws/goravel/goravel", "goravel/goravel").Return(nil).Once()

	service := newTestService(router, dispatcher, retry, handles, checkouts, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	httpMock.AssertNotCalled(t, "Delete", testifymock.Anything, testifymock.Anything)
	retry.AssertNotCalled(t, "Reset", testifymock.Anything)
	handles.AssertNotCalled(t, "Mark", testifymock.Anything)
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

	router := mocksgoraveler.NewAgentRouter(t)
	router.EXPECT().AgentFor(testifymock.Anything, "review").Return("", errors.New("router exploded")).Once()

	dispatcher := mocksgoraveler.NewDispatcher(t)
	retry := mocksgoraveler.NewRetryTracker(t)
	retry.EXPECT().RecordFailure("d-1").Return(false).Once()
	handles := mocksgoraveler.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	checkouts := mocksgoraveler.NewCheckouter(t)

	service := newTestService(router, dispatcher, retry, handles, checkouts, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
	retry.AssertNotCalled(t, "Reset", testifymock.Anything)
	handles.AssertNotCalled(t, "Mark", testifymock.Anything)
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

	router := mocksgoraveler.NewAgentRouter(t)
	router.EXPECT().AgentFor(testifymock.Anything, "review").Return(agentReview, nil).Once()

	dispatcher := mocksgoraveler.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).
		Return(errors.New("opencode failed")).
		Once()

	retry := mocksgoraveler.NewRetryTracker(t)
	retry.EXPECT().RecordFailure("d-1").Return(true).Once()

	handles := mocksgoraveler.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	// A gave-up event is marked so a failed acknowledgement cannot replay it.
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksgoraveler.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, "/tmp/ws/goravel/goravel", "goravel/goravel").Return(nil).Once()

	service := newTestService(router, dispatcher, retry, handles, checkouts, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	retry.AssertNotCalled(t, "Reset", testifymock.Anything)
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

	router := mocksgoraveler.NewAgentRouter(t)
	router.EXPECT().AgentFor(testifymock.Anything, "review").Return(agentReview, nil).Once()

	dispatcher := mocksgoraveler.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksgoraveler.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	service, handles, checkouts := testService(t, router, dispatcher, retry)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()
	checkouts.EXPECT().Ensure(testifymock.Anything, "/tmp/ws/goravel/goravel", "goravel/goravel").Return(nil).Once()

	service.Poll(context.Background())

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

	router := mocksgoraveler.NewAgentRouter(t)
	router.EXPECT().AgentFor(testifymock.Anything, "review").Return(agentReview, nil).Once()

	dispatcher := mocksgoraveler.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksgoraveler.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	service, handles, checkouts := testService(t, router, dispatcher, retry)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()
	checkouts.EXPECT().Ensure(testifymock.Anything, "/tmp/ws/goravel/goravel", "goravel/goravel").Return(nil).Once()

	service.Poll(context.Background())

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

	// None of the events carries a mention, so nothing is routed or dispatched.
	service, _, _ := testService(t, mocksgoraveler.NewAgentRouter(t), mocksgoraveler.NewDispatcher(t), mocksgoraveler.NewRetryTracker(t))

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

	router := mocksgoraveler.NewAgentRouter(t)
	dispatcher := mocksgoraveler.NewDispatcher(t)

	service, _, _ := testService(t, router, dispatcher, mocksgoraveler.NewRetryTracker(t))
	service.Poll(context.Background())

	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
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

	router := mocksgoraveler.NewAgentRouter(t)
	dispatcher := mocksgoraveler.NewDispatcher(t)

	service, _, _ := testService(t, router, dispatcher, mocksgoraveler.NewRetryTracker(t))
	service.Poll(context.Background())

	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
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

	router := mocksgoraveler.NewAgentRouter(t)
	dispatcher := mocksgoraveler.NewDispatcher(t)

	service, _, _ := testService(t, router, dispatcher, mocksgoraveler.NewRetryTracker(t))
	service.Poll(context.Background())

	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
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

	router := mocksgoraveler.NewAgentRouter(t)
	dispatcher := mocksgoraveler.NewDispatcher(t)

	service, _, _ := testService(t, router, dispatcher, mocksgoraveler.NewRetryTracker(t))
	service.Poll(context.Background())

	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
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
	service := newTestService(
		mocksgoraveler.NewAgentRouter(t),
		mocksgoraveler.NewDispatcher(t),
		mocksgoraveler.NewRetryTracker(t),
		mocksgoraveler.NewHandleStore(t),
		mocksgoraveler.NewCheckouter(t),
		testConfig{
			baseURL:        "http://relay.test/webhooks/events/",
			apiToken:       "token",
			workspace:      "/tmp/ws",
			allowedSenders: parseSenders("bowen"),
		},
	)

	require.Equal(t, "http://relay.test/webhooks/events", service.eventsURL)
}

func TestServiceAllowedSenders(t *testing.T) {
	service := newTestService(
		mocksgoraveler.NewAgentRouter(t),
		mocksgoraveler.NewDispatcher(t),
		mocksgoraveler.NewRetryTracker(t),
		mocksgoraveler.NewHandleStore(t),
		mocksgoraveler.NewCheckouter(t),
		relayConfigSenders("token", "/tmp/ws", "Bowen, alice"),
	)

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

	router := mocksgoraveler.NewAgentRouter(t)
	dispatcher := mocksgoraveler.NewDispatcher(t)
	retry := mocksgoraveler.NewRetryTracker(t)

	service := newTestService(router, dispatcher, retry, mocksgoraveler.NewHandleStore(t), mocksgoraveler.NewCheckouter(t), relayConfigSenders("token", "/tmp/ws", "alice"))
	service.Poll(context.Background())

	// The skipped event is still acknowledged so it does not block the page.
	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
	retry.AssertNotCalled(t, "Reset", testifymock.Anything)
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

	router := mocksgoraveler.NewAgentRouter(t)
	router.EXPECT().AgentFor(testifymock.Anything, "review").Return(agentReview, nil).Once()

	dispatcher := mocksgoraveler.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksgoraveler.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksgoraveler.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksgoraveler.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, "/tmp/ws/goravel/goravel", "goravel/goravel").Return(nil).Once()

	service := newTestService(router, dispatcher, retry, handles, checkouts, relayConfigSenders("token", "/tmp/ws", "bowen"))
	service.Poll(context.Background())

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

	handles := mocksgoraveler.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(true).Once()
	dispatcher := mocksgoraveler.NewDispatcher(t)
	retry := mocksgoraveler.NewRetryTracker(t)

	service := newTestService(mocksgoraveler.NewAgentRouter(t), dispatcher, retry, handles, mocksgoraveler.NewCheckouter(t), relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	// The already-handled event is skipped but still acknowledged.
	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
	retry.AssertNotCalled(t, "Reset", testifymock.Anything)
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

	router := mocksgoraveler.NewAgentRouter(t)
	router.EXPECT().AgentFor(testifymock.Anything, "review").Return(agentReview, nil).Once()

	dispatcher := mocksgoraveler.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksgoraveler.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksgoraveler.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksgoraveler.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, "/tmp/ws/goravel/goravel", "goravel/goravel").Return(nil).Once()

	service := newTestService(router, dispatcher, retry, handles, checkouts, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

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

	router := mocksgoraveler.NewAgentRouter(t)
	router.EXPECT().AgentFor(testifymock.Anything, "review").Return(agentReview, nil).Once()

	dispatcher := mocksgoraveler.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksgoraveler.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	service, handles, checkouts := testService(t, router, dispatcher, retry)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()
	checkouts.EXPECT().Ensure(testifymock.Anything, "/tmp/ws/goravel/goravel", "goravel/goravel").Return(nil).Once()

	service.Poll(context.Background())

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

	dispatcher := mocksgoraveler.NewDispatcher(t)

	service, _, _ := testService(t, mocksgoraveler.NewAgentRouter(t), dispatcher, mocksgoraveler.NewRetryTracker(t))
	service.Poll(context.Background())

	// The undecodable event is not a mention, so it is acknowledged and deleted.
	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
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

	dispatcher := mocksgoraveler.NewDispatcher(t)

	service, _, _ := testService(t, mocksgoraveler.NewAgentRouter(t), dispatcher, mocksgoraveler.NewRetryTracker(t))
	service.Poll(context.Background())

	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}
