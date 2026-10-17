package goraveler

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"goravel/app/facades"
)

const (
	// maxAttempts is the number of consecutive failures before an event is dropped.
	maxAttempts = 3
	// apiTokenHeader is the header carrying the relay token.
	apiTokenHeader = "X-Api-Token"
	// deleteConcurrency bounds the number of concurrent acknowledgement requests.
	deleteConcurrency = 5
)

// agentRouter chooses which agent should handle an instruction.
type agentRouter interface {
	AgentFor(ctx context.Context, instruction string) (string, error)
}

// dispatcher runs an agent for a dispatch request.
type dispatcher interface {
	Run(ctx context.Context, req dispatchRequest) error
}

// checkouter ensures a repository is checked out under the workspace.
type checkouter interface {
	// Ensure clones the repository into dir when it does not already exist.
	Ensure(ctx context.Context, dir, repository string) error
}

// retryTracker counts consecutive failures per key.
type retryTracker interface {
	// RecordFailure records a failed attempt and reports whether the cap is reached.
	RecordFailure(key string) bool
	// Reset clears the failure counter for a key.
	Reset(key string)
}

// handleStore records events that have been handled so a failed acknowledgement
// does not replay the agent run on the next tick.
type handleStore interface {
	// Has reports whether the key was already recorded.
	Has(key string) bool
	// Mark records the key.
	Mark(key string)
}

// Service polls the relay for @goraveler mentions and dispatches agents.
//
// The router, dispatcher, checkouter, retry tracker and handle store are
// injected because their policies (agent choice, CLI invocation, checkout,
// retry cap, dedupe) are the interesting parts to substitute in tests. The HTTP
// client and logger are resolved from the global facades on purpose: they are
// framework singletons and the app facades are the supported way to reach them.
type Service struct {
	router     agentRouter
	dispatcher dispatcher
	checkouts  checkouter
	retry      retryTracker
	handles    handleStore

	apiToken string
	// workspaceRoot is the symlink-resolved workspace root.
	workspaceRoot string
	eventsURL     string

	// allowedSenders restricts which GitHub senders may trigger an agent run
	// (GORAVELER_ALLOWED_SENDERS). It is validated non-empty in NewService.
	allowedSenders map[string]struct{}
}

// NewService wires a Service from application configuration. It fails when
// GORAVELER_ALLOWED_SENDERS is empty, since running with no allowlist would let
// any GitHub sender trigger agent runs.
func NewService() (*Service, error) {
	config := facades.Config()

	senders := parseSenders(config.GetString("goraveler.allowed_senders"))
	if len(senders) == 0 {
		return nil, errors.New("goraveler: GORAVELER_ALLOWED_SENDERS must not be empty")
	}

	return &Service{
		router:         newAIRouter(),
		dispatcher:     newOpencodeDispatcher(),
		checkouts:      newGitCheckouter(),
		retry:          newCacheRetryTracker(),
		handles:        newCacheHandleStore(),
		apiToken:       config.GetString("goraveler.api_token"),
		workspaceRoot:  resolveWorkspace(config.GetString("goraveler.workspace")),
		eventsURL:      strings.TrimRight(config.GetString("goraveler.base_url"), "/"),
		allowedSenders: senders,
	}, nil
}

// allowed reports whether the sender may trigger an agent run. Comparison is
// case-insensitive; the allowlist is validated non-empty in NewService.
func (s *Service) allowed(sender string) bool {
	_, ok := s.allowedSenders[strings.ToLower(sender)]

	return ok
}

// parseSenders splits a comma-separated allowlist into lowercased sender keys,
// ignoring blank entries.
func parseSenders(raw string) map[string]struct{} {
	senders := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			senders[part] = struct{}{}
		}
	}

	return senders
}

// Poll fetches one page of events, handles every mention, and deletes the
// events it has drained.
func (s *Service) Poll(ctx context.Context) {
	response, err := facades.Http().
		WithContext(ctx).
		WithQueryParameters(map[string]string{
			"platform": platformGitHub,
			"page":     strconv.Itoa(eventPage),
			"limit":    strconv.Itoa(eventLimit),
		}).
		WithHeader(apiTokenHeader, s.apiToken).
		Get(s.eventsURL)
	if err != nil {
		facades.Log().Errorf("goraveler: fetch events: %v", err)
		return
	}
	if response.Failed() {
		facades.Log().Errorf("goraveler: fetch events: status %d", response.Status())
		// Drain the body so the connection is released.
		_, _ = response.Body()
		return
	}

	body, err := response.Body()
	if err != nil {
		facades.Log().Errorf("goraveler: read events response: %v", err)
		return
	}

	env, err := parseEnvelope([]byte(body))
	if err != nil {
		facades.Log().Errorf("goraveler: parse events envelope: %v", err)
		return
	}

	toDelete := make([]uint64, 0, len(env.Data))
	for i := range env.Data {
		ev := &env.Data[i]

		if !ev.actionable() {
			toDelete = append(toDelete, ev.ID)
			continue
		}

		if _, err := ev.payload(); err != nil {
			facades.Log().Errorf("goraveler: decode event payload %d: %v", ev.ID, err)
		}

		instruction, ok := ev.instruction(mentionHandle)
		if !ok {
			toDelete = append(toDelete, ev.ID)
			continue
		}

		if !s.allowed(ev.Sender) {
			facades.Log().Infof("goraveler: skip event %d: sender %q is not allowed", ev.ID, ev.Sender)
			toDelete = append(toDelete, ev.ID)
			continue
		}

		key := retryKey(ev)
		if s.handles.Has(key) {
			facades.Log().Infof("goraveler: skip event %d: already handled", ev.ID)
			toDelete = append(toDelete, ev.ID)
			continue
		}

		if err := s.handle(ctx, ev, instruction); err != nil {
			facades.Log().Errorf("goraveler: handle event %d: %v", ev.ID, err)
			if !s.retry.RecordFailure(key) {
				continue
			}
			facades.Log().Errorf("goraveler: giving up on event %d after %d attempts", ev.ID, maxAttempts)
			// Remember the give-up so a failed acknowledgement cannot replay it.
			s.handles.Mark(key)
		} else {
			s.retry.Reset(key)
			// Remember the success so a failed acknowledgement does not replay it.
			s.handles.Mark(key)
		}

		toDelete = append(toDelete, ev.ID)
	}

	s.deleteEvents(ctx, toDelete)
}

// retryKey identifies an event for retry accounting, preferring the relay's
// delivery id and falling back to the event id when it is absent.
func retryKey(ev *event) string {
	if ev.DeliveryID != "" {
		return ev.DeliveryID
	}

	return strconv.FormatUint(ev.ID, 10)
}

// handle routes the instruction to an agent and runs it against the target repo.
func (s *Service) handle(ctx context.Context, ev *event, instruction string) error {
	agent, err := s.router.AgentFor(ctx, instruction)
	if err != nil {
		return err
	}

	dir := repoDirIn(s.workspaceRoot, ev.Repository)
	if err := s.checkouts.Ensure(ctx, dir, ev.Repository); err != nil {
		return err
	}

	return s.dispatcher.Run(ctx, dispatchRequest{
		dir:    dir,
		agent:  agent,
		prompt: ev.prompt(instruction),
	})
}

// deleteEvents acknowledges the drained events with a bounded worker pool so a
// full page does not serialize fifty requests.
func (s *Service) deleteEvents(ctx context.Context, ids []uint64) {
	if len(ids) == 0 {
		return
	}

	sem := make(chan struct{}, deleteConcurrency)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}

		go func(id uint64) {
			defer wg.Done()
			defer func() { <-sem }()

			s.deleteEvent(ctx, id)
		}(id)
	}
	wg.Wait()
}

// deleteEvent acknowledges an event so it is not fetched again.
func (s *Service) deleteEvent(ctx context.Context, id uint64) {
	url := s.eventsURL + "/" + strconv.FormatUint(id, 10)

	res, err := facades.Http().
		WithContext(ctx).
		WithHeader(apiTokenHeader, s.apiToken).
		Delete(url, nil)
	if err != nil {
		facades.Log().Errorf("goraveler: delete event %d: %v", id, err)
		return
	}

	// The framework only reads and closes the body inside Body()/Bind()/Json().
	if _, err := res.Body(); err != nil {
		facades.Log().Errorf("goraveler: read delete response %d: %v", id, err)
	}

	if res.Failed() {
		facades.Log().Errorf("goraveler: delete event %d: status %d", id, res.Status())
	}
}
