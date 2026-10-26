package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

	"goravel/app/constants"
	mocksagent "goravel/app/mocks/services/agent"
	"goravel/app/services/agent/dispatch"
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
			"payload": "{\"number\":123,\"comment\":{\"id\":24680,\"body\":\"@goravel-agent review\"}}"
		}
	],
	"total": 1,
	"page": 1,
	"limit": 10
}`

	// zeroNumberEnvelope is a mention whose payload carries no issue or pull
	// request number, so the run has no fallback title.
	zeroNumberEnvelope = `{"data":[{"id":1,"delivery_id":"d-1","sender":"bowen","event":"issue_comment","action":"created","repository":"goravel/goravel","payload":"{\"comment\":{\"id\":24680,\"body\":\"@goravel-agent review\"}}"}],"total":1,"page":1,"limit":10}`
)

// relayQueryParams pins the relay request query. The limit is the literal wire
// value the service sends, pinned on purpose: it must not be derived from
// eventLimit, or the page size would be asserted against itself and a drift like
// 50 -> 10 would go unnoticed. Changing the relay page size must be a deliberate
// change to this pin too.
var relayQueryParams = map[string]string{
	"platform": "github",
	"page":     "1",
	"limit":    "10",
}

// checkoutTime pins the injected clock so the per-run checkout name is stable.
var checkoutTime = time.Date(2026, time.October, 18, 14, 32, 5, 0, time.UTC)

// testConfig groups the relay-facing settings used to build a test Service.
type testConfig struct {
	baseURL        string
	apiToken       string
	workspace      string
	mention        string
	allowedSenders map[string]struct{}
	now            func() time.Time
}

// newTestService wires a Service from explicit dependencies, mirroring NewService
// without the configuration lookup.
func newTestService(router AgentRouter, dispatcher Dispatcher, retry RetryTracker, handles HandleStore, checkouts Checkouter, reactor Reactor, cfg testConfig) *Service {
	return &Service{
		router:         router,
		dispatcher:     dispatcher,
		checkouts:      checkouts,
		retry:          retry,
		handles:        handles,
		reactor:        reactor,
		mention:        cfg.mention,
		apiToken:       cfg.apiToken,
		workspaceRoot:  resolveWorkspace(cfg.workspace),
		eventsURL:      strings.TrimRight(cfg.baseURL, "/"),
		allowedSenders: cfg.allowedSenders,
		now:            cfg.now,
	}
}

// relayConfig builds the test relay configuration.
func relayConfig(apiToken, workspace string) testConfig {
	return testConfig{
		baseURL:        relayEventsBaseURL,
		apiToken:       apiToken,
		workspace:      workspace,
		mention:        DefaultMention,
		allowedSenders: parseSenders("bowen"),
		now:            func() time.Time { return checkoutTime },
	}
}

// relayConfigSenders is relayConfig with a sender allowlist.
func relayConfigSenders(apiToken, workspace, senders string) testConfig {
	cfg := relayConfig(apiToken, workspace)
	cfg.allowedSenders = parseSenders(senders)

	return cfg
}

// testService builds a Service with a fresh handle store and checkouter, which
// are returned so the caller can set their expectations.
func testService(t *testing.T, router AgentRouter, dispatcher Dispatcher, retry RetryTracker) (*Service, *mocksagent.HandleStore, *mocksagent.Checkouter) {
	handles := mocksagent.NewHandleStore(t)
	checkouts := mocksagent.NewCheckouter(t)
	service := newTestService(router, dispatcher, retry, handles, checkouts, mocksagent.NewReactor(t), relayConfig("token", "/tmp/ws"))

	return service, handles, checkouts
}

// expectReaction returns a reactor preset to expect exactly one successful eyes
// reaction on the given comment.
func expectReaction(t *testing.T, repository, collection string, commentID int64) *mocksagent.Reactor {
	t.Helper()

	return expectReactionError(t, repository, collection, commentID, nil)
}

// expectReactionError is expectReaction for a reaction that fails with err. It
// also pins that the caller bounds the call with reactTimeout, so a hung GitHub
// can never stall the poll loop.
func expectReactionError(t *testing.T, repository, collection string, commentID int64, err error) *mocksagent.Reactor {
	t.Helper()

	reactor := mocksagent.NewReactor(t)
	reactor.EXPECT().
		React(testifymock.MatchedBy(func(ctx context.Context) bool {
			deadline, ok := ctx.Deadline()

			return ok && time.Until(deadline) <= reactTimeout
		}), repository, collection, commentID).
		Return(err).
		Once()

	return reactor
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
		{"id":1,"delivery_id":"d-1","sender":"bowen","event":"issue_comment","action":"created","repository":"goravel/goravel","payload":"{\"number\":123,\"comment\":{\"id\":24680,\"body\":\"@goravel-agent review\"}}"},
		{"id":2,"delivery_id":"d-2","sender":"bowen","event":"issues","action":"opened","repository":"goravel/framework","payload":"{\"number\":5,\"issue\":{\"number\":5,\"body\":\"nothing to see\"}}"}
	],"total":2,"page":1,"limit":10}`

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

	router := mocksagent.NewAgentRouter(t)
	// The router sees the source repository and the untrusted instruction, not
	// the routed target.
	router.EXPECT().Route(testifymock.Anything, testifymock.MatchedBy(func(p string) bool {
		return strings.Contains(p, "Repository: goravel/goravel") &&
			strings.Contains(p, "Instruction: review")
	})).Return(constants.AgentReview, "goravel/goravel", "Review issue 123", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().
		Run(testifymock.Anything, testifymock.MatchedBy(func(req dispatch.Request) bool {
			return req.Dir == runDir("/tmp/ws", "goravel/goravel", checkoutTime) &&
				req.Agent == constants.AgentReview &&
				req.Title == "Review issue 123" &&
				!strings.Contains(req.Prompt, "Repository:") &&
				strings.Contains(req.Prompt, "Instruction: review")
		})).
		Return(nil).
		Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().
		Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").
		Return(nil).
		Once()

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
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

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().RecordFailure("d-1").Return(false).Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().
		Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").
		Return(errors.New("clone failed")).
		Once()

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	// A checkout failure keeps the event for a retry and never dispatches.
	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
	handles.AssertNotCalled(t, "Mark", testifymock.Anything)
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

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).
		Return(errors.New("paseo failed")).
		Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().RecordFailure("d-1").Return(false).Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").Return(nil).Once()

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
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

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return("", "", "", errors.New("router exploded")).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().RecordFailure("d-1").Return(false).Once()
	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	checkouts := mocksagent.NewCheckouter(t)

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
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

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).
		Return(errors.New("paseo failed")).
		Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().RecordFailure("d-1").Return(true).Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	// A gave-up event is marked so a failed acknowledgement cannot replay it.
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").Return(nil).Once()

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
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

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	checkouts := mocksagent.NewCheckouter(t)
	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").Return(nil).Once()

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

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	checkouts := mocksagent.NewCheckouter(t)
	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").Return(nil).Once()

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
	body := `{"data":[` + strings.Join(events, ",") + `],"total":` + strconv.Itoa(count) + `,"page":1,"limit":10}`

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
	service, _, _ := testService(t, mocksagent.NewAgentRouter(t), mocksagent.NewDispatcher(t), mocksagent.NewRetryTracker(t))

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

	router := mocksagent.NewAgentRouter(t)
	dispatcher := mocksagent.NewDispatcher(t)

	service, _, _ := testService(t, router, dispatcher, mocksagent.NewRetryTracker(t))
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

	router := mocksagent.NewAgentRouter(t)
	dispatcher := mocksagent.NewDispatcher(t)

	service, _, _ := testService(t, router, dispatcher, mocksagent.NewRetryTracker(t))
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

	router := mocksagent.NewAgentRouter(t)
	dispatcher := mocksagent.NewDispatcher(t)

	service, _, _ := testService(t, router, dispatcher, mocksagent.NewRetryTracker(t))
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

	router := mocksagent.NewAgentRouter(t)
	dispatcher := mocksagent.NewDispatcher(t)

	service, _, _ := testService(t, router, dispatcher, mocksagent.NewRetryTracker(t))
	service.Poll(context.Background())

	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
	httpMock.AssertNotCalled(t, "Delete", testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}

func TestNewServiceReadsConfig(t *testing.T) {
	factory := mock.Factory()
	configMock := factory.Config()
	httpMock := factory.Http()

	configMock.EXPECT().GetString("agent.base_url").Return("http://relay.test/webhooks/events").Once()
	configMock.EXPECT().GetString("agent.api_token").Return("secret").Once()
	configMock.EXPECT().GetString("agent.workspace").Return("/srv/code").Once()
	configMock.EXPECT().GetString("agent.allowed_senders").Return("bowen, alice").Once()
	models := map[string]string{
		constants.AgentShip:      "opencode-go/deepseek-v4.1-flash#high",
		constants.AgentImplement: "opencode-go/deepseek-v4.1-flash#high",
		constants.AgentReview:    "opencode-go/deepseek-v4.1-flash#high",
	}
	configMock.EXPECT().Get("agent.models").Return(models).Once()
	configMock.EXPECT().GetString("agent.mention").Return("@bot").Once()
	httpMock.EXPECT().HttpClient().Return(&http.Client{}).Once()
	configMock.EXPECT().GetString("agent.github_token").Return("gh-token").Once()

	service, err := NewService()
	require.NoError(t, err)
	require.NotNil(t, service)

	require.Equal(t, "http://relay.test/webhooks/events", service.eventsURL)
	require.Equal(t, "secret", service.apiToken)
	require.Equal(t, "/srv/code", service.workspaceRoot)
	require.Equal(t, "@bot", service.mention)
	require.Equal(t, models, service.dispatcher.(*opencodeDispatcher).models)
	require.IsType(t, &cacheHandleStore{}, service.handles)
	require.IsType(t, &gitCheckouter{}, service.checkouts)
	require.NotNil(t, service.now)
	require.IsType(t, &githubReactor{}, service.reactor)
	require.NotNil(t, service.reactor.(*githubReactor).client)
	require.True(t, service.allowed("bowen"))
	require.True(t, service.allowed("alice"))
	require.False(t, service.allowed("mallory"))

	configMock.AssertExpectations(t)
}

func TestNewServiceRejectsEmptySenders(t *testing.T) {
	factory := mock.Factory()
	configMock := factory.Config()

	configMock.EXPECT().GetString("agent.allowed_senders").Return("  ,  ").Once()

	service, err := NewService()
	require.Nil(t, service)
	require.EqualError(t, err, "agent: AGENT_ALLOWED_SENDERS must not be empty")

	configMock.AssertExpectations(t)
}

func TestNewServiceRejectsMissingModels(t *testing.T) {
	factory := mock.Factory()
	configMock := factory.Config()

	configMock.EXPECT().GetString("agent.allowed_senders").Return("bowen").Once()
	configMock.EXPECT().Get("agent.models").Return(nil).Once()

	// NewService returns before touching the HTTP client, so no HTTP mock is
	// registered.
	service, err := NewService()
	require.Nil(t, service)
	require.EqualError(t, err, "agent: agent.models must map every agent to a <model>[#<thinking>] entry")

	configMock.AssertExpectations(t)
}

func TestNewServiceRejectsAgentWithoutModel(t *testing.T) {
	factory := mock.Factory()
	configMock := factory.Config()

	configMock.EXPECT().GetString("agent.allowed_senders").Return("bowen").Once()
	configMock.EXPECT().Get("agent.models").Return(map[string]string{
		constants.AgentShip:      "opencode-go/deepseek-v4.1-flash#high",
		constants.AgentImplement: "opencode-go/deepseek-v4.1-flash#high",
	}).Once()

	service, err := NewService()
	require.Nil(t, service)
	require.EqualError(t, err, `agent: agent.models has no model for the "review" agent`)

	configMock.AssertExpectations(t)
}

func TestServiceBaseURLTrimsTrailingSlash(t *testing.T) {
	service := newTestService(
		mocksagent.NewAgentRouter(t),
		mocksagent.NewDispatcher(t),
		mocksagent.NewRetryTracker(t),
		mocksagent.NewHandleStore(t),
		mocksagent.NewCheckouter(t),
		mocksagent.NewReactor(t),
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
		mocksagent.NewAgentRouter(t),
		mocksagent.NewDispatcher(t),
		mocksagent.NewRetryTracker(t),
		mocksagent.NewHandleStore(t),
		mocksagent.NewCheckouter(t),
		mocksagent.NewReactor(t),
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

	router := mocksagent.NewAgentRouter(t)
	dispatcher := mocksagent.NewDispatcher(t)
	retry := mocksagent.NewRetryTracker(t)

	service := newTestService(router, dispatcher, retry, mocksagent.NewHandleStore(t), mocksagent.NewCheckouter(t), mocksagent.NewReactor(t), relayConfigSenders("token", "/tmp/ws", "alice"))
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

	envelope := `{"data":[{"id":1,"delivery_id":"d-1","sender":"bowen","event":"issue_comment","action":"created","repository":"goravel/goravel","payload":"{\"number\":123,\"comment\":{\"id\":24680,\"body\":\"@goravel-agent review\"}}"}],"total":1,"page":1,"limit":10}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(envelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").Return(nil).Once()

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfigSenders("token", "/tmp/ws", "bowen"))
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

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(true).Once()
	dispatcher := mocksagent.NewDispatcher(t)
	retry := mocksagent.NewRetryTracker(t)

	service := newTestService(mocksagent.NewAgentRouter(t), dispatcher, retry, handles, mocksagent.NewCheckouter(t), mocksagent.NewReactor(t), relayConfig("token", "/tmp/ws"))
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

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").Return(nil).Once()

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
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

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	checkouts := mocksagent.NewCheckouter(t)
	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").Return(nil).Once()

	service.Poll(context.Background())

	httpMock.AssertExpectations(t)
}

func TestServicePollDeletesEventWithInvalidPayload(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	env := `{"data":[{"id":1,"delivery_id":"d-1","event":"issue_comment","action":"created","repository":"goravel/goravel","payload":"not json"}],"total":1,"page":1,"limit":10}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(env, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)

	service, _, _ := testService(t, mocksagent.NewAgentRouter(t), dispatcher, mocksagent.NewRetryTracker(t))
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
	env := `{"data":[{"id":1,"delivery_id":"d-1","event":"issues","action":"labeled","repository":"goravel/goravel","payload":"{\"number\":123,\"issue\":{\"number\":123,\"body\":\"@goravel-agent review\"}}"}],"total":1,"page":1,"limit":10}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(env, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)

	service, _, _ := testService(t, mocksagent.NewAgentRouter(t), dispatcher, mocksagent.NewRetryTracker(t))
	service.Poll(context.Background())

	dispatcher.AssertNotCalled(t, "Run", testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}

func TestServicePollUsesRoutedRepository(t *testing.T) {
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

	// The router sends the event's goravel/goravel mention to goravel/framework.
	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).
		Return(constants.AgentImplement, "goravel/framework", "", nil).
		Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().
		Run(testifymock.Anything, testifymock.MatchedBy(func(req dispatch.Request) bool {
			return req.Dir == runDir("/tmp/ws", "goravel/framework", checkoutTime) &&
				req.Agent == constants.AgentImplement &&
				req.Title == "Issue #123" &&
				!strings.Contains(req.Prompt, "Repository:")
		})).
		Return(nil).
		Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().
		Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/framework", checkoutTime), "goravel/framework").
		Return(nil).
		Once()

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	httpMock.AssertExpectations(t)
}

// TestServicePollLeavesTitleEmptyWithoutTarget pins that an event whose payload
// carries no issue or pull request number has no fallback title: the empty title
// reaches the dispatcher, which then omits --title so paseo names the run.
func TestServicePollLeavesTitleEmptyWithoutTarget(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(zeroNumberEnvelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	// The router returns no title and the payload has no number to fall back to.
	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).
		Return(constants.AgentReview, "goravel/goravel", "", nil).
		Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().
		Run(testifymock.Anything, testifymock.MatchedBy(func(req dispatch.Request) bool {
			return req.Dir == runDir("/tmp/ws", "goravel/goravel", checkoutTime) &&
				req.Title == ""
		})).
		Return(nil).
		Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().
		Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").
		Return(nil).
		Once()

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	httpMock.AssertExpectations(t)
}

func TestServicePollFallsBackToSourceRepository(t *testing.T) {
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

	// An empty routed repository means "use the source repository".
	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).
		Return(constants.AgentReview, "", "", nil).
		Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().
		Run(testifymock.Anything, testifymock.MatchedBy(func(req dispatch.Request) bool {
			return req.Dir == runDir("/tmp/ws", "goravel/goravel", checkoutTime) &&
				req.Agent == constants.AgentReview &&
				!strings.Contains(req.Prompt, "Repository:")
		})).
		Return(nil).
		Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().
		Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").
		Return(nil).
		Once()

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	httpMock.AssertExpectations(t)
}

func TestServicePollUsesConfiguredMention(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	// Only the configured handle triggers a run; the built-in default does not.
	envelope := `{"data":[
		{"id":1,"delivery_id":"d-1","sender":"bowen","event":"issue_comment","action":"created","repository":"goravel/goravel","payload":"{\"number\":123,\"comment\":{\"id\":24680,\"body\":\"@bot review\"}}"},
		{"id":2,"delivery_id":"d-2","sender":"bowen","event":"issue_comment","action":"created","repository":"goravel/framework","payload":"{\"number\":5,\"comment\":{\"id\":13579,\"body\":\"@goravel-agent review\"}}"}
	],"total":2,"page":1,"limit":10}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(envelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Times(2)
	deleteResp.EXPECT().Failed().Return(false).Times(2)

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(3)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(3)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/2", nil).Return(deleteResp, nil).Once()

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").Return(nil).Once()

	cfg := relayConfig("token", "/tmp/ws")
	cfg.mention = "@bot"

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, cfg)
	service.Poll(context.Background())

	// Both events are acknowledged: the mention was handled and the non-mention
	// drained.
	httpMock.AssertExpectations(t)
}

// TestServicePollReactsToMentionedComment is the end-to-end assertion for the
// conversation-comment reaction: an accepted issue_comment mention gets the eyes
// reaction and still dispatches the agent run.
func TestServicePollReactsToMentionedComment(t *testing.T) {
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

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").Return(nil).Once()

	reactor := expectReaction(t, "goravel/goravel", reactionCollectionIssue, 24680)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	reactor.AssertNumberOfCalls(t, "React", 1)
	httpMock.AssertExpectations(t)
}

// TestServicePollReactsToReviewComment is the end-to-end assertion for the inline
// review comment reaction: the comment is addressed through the pull request
// collection, not the issue collection.
func TestServicePollReactsToReviewComment(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	envelope := `{"data":[{"id":1,"delivery_id":"d-1","sender":"bowen","event":"pull_request_review_comment","action":"created","repository":"goravel/framework","payload":"{\"pull_request\":{\"number\":123},\"comment\":{\"id\":98765,\"body\":\"@goravel-agent review\"}}"}],"total":1,"page":1,"limit":10}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(envelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/framework", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().
		Run(testifymock.Anything, testifymock.MatchedBy(func(req dispatch.Request) bool {
			return req.Dir == runDir("/tmp/ws", "goravel/framework", checkoutTime) &&
				req.Agent == constants.AgentReview &&
				req.Title == "PR #123"
		})).
		Return(nil).
		Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/framework", checkoutTime), "goravel/framework").Return(nil).Once()

	reactor := expectReaction(t, "goravel/framework", reactionCollectionReview, 98765)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	reactor.AssertNumberOfCalls(t, "React", 1)
	httpMock.AssertExpectations(t)
}

// TestServicePollContinuesWhenReactionFails pins that a failed reaction is only
// logged: the run still dispatches, the event is marked handled, and it is
// deleted.
func TestServicePollContinuesWhenReactionFails(t *testing.T) {
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

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/goravel", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksagent.NewRetryTracker(t)
	// A reaction failure must not consume a retry attempt.
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/goravel", checkoutTime), "goravel/goravel").Return(nil).Once()

	reactor := expectReactionError(t, "goravel/goravel", reactionCollectionIssue, 24680, errors.New("gh down"))

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	retry.AssertNotCalled(t, "RecordFailure", testifymock.Anything)
	handles.AssertNumberOfCalls(t, "Mark", 1)
	httpMock.AssertExpectations(t)
}

// TestServicePollDoesNotReactToBodyMention pins that a mention in an issue body
// has no comment to react to, so no reaction is sent while the run still
// dispatches.
func TestServicePollDoesNotReactToBodyMention(t *testing.T) {
	factory := mock.Factory()
	factory.Log()
	httpMock := factory.Http()
	getResp := mockshttp.NewResponse(t)
	deleteResp := mockshttp.NewResponse(t)

	envelope := `{"data":[{"id":1,"delivery_id":"d-1","sender":"bowen","event":"issues","action":"opened","repository":"goravel/framework","payload":"{\"number\":5,\"issue\":{\"number\":5,\"body\":\"@goravel-agent review\"}}"}],"total":1,"page":1,"limit":10}`

	getResp.EXPECT().Failed().Return(false).Once()
	getResp.EXPECT().Body().Return(envelope, nil).Once()
	deleteResp.EXPECT().Body().Return("", nil).Once()
	deleteResp.EXPECT().Failed().Return(false).Once()

	httpMock.EXPECT().WithContext(testifymock.Anything).Return(httpMock).Times(2)
	httpMock.EXPECT().WithQueryParameters(relayQueryParams).Return(httpMock).Once()
	httpMock.EXPECT().WithHeader(apiTokenHeader, "token").Return(httpMock).Times(2)
	httpMock.EXPECT().Get(relayEventsBaseURL).Return(getResp, nil).Once()
	httpMock.EXPECT().Delete("http://relay.test/webhooks/events/1", nil).Return(deleteResp, nil).Once()

	router := mocksagent.NewAgentRouter(t)
	router.EXPECT().Route(testifymock.Anything, testifymock.Anything).Return(constants.AgentReview, "goravel/framework", "", nil).Once()

	dispatcher := mocksagent.NewDispatcher(t)
	dispatcher.EXPECT().Run(testifymock.Anything, testifymock.Anything).Return(nil).Once()

	retry := mocksagent.NewRetryTracker(t)
	retry.EXPECT().Reset("d-1").Once()

	handles := mocksagent.NewHandleStore(t)
	handles.EXPECT().Has("d-1").Return(false).Once()
	handles.EXPECT().Mark("d-1").Once()

	checkouts := mocksagent.NewCheckouter(t)
	checkouts.EXPECT().Ensure(testifymock.Anything, runDir("/tmp/ws", "goravel/framework", checkoutTime), "goravel/framework").Return(nil).Once()

	reactor := mocksagent.NewReactor(t)

	service := newTestService(router, dispatcher, retry, handles, checkouts, reactor, relayConfig("token", "/tmp/ws"))
	service.Poll(context.Background())

	reactor.AssertNotCalled(t, "React", testifymock.Anything, testifymock.Anything, testifymock.Anything, testifymock.Anything)
	httpMock.AssertExpectations(t)
}
