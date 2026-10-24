//go:generate go tool mockery

package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"goravel/app/facades"
	"goravel/app/services/agent/dispatch"
)

const (
	// maxAttempts is the number of consecutive failures before an event is dropped.
	maxAttempts = 3
	// apiTokenHeader is the header carrying the relay token.
	apiTokenHeader = "X-Api-Token"
	// deleteConcurrency bounds the number of concurrent acknowledgement requests.
	deleteConcurrency = 5
	// reactTimeout bounds the best-effort eyes reaction so an unresponsive GitHub
	// cannot stall the poll loop before the dispatch and the acknowledgement.
	reactTimeout = 10 * time.Second
)

// Service polls the relay for @goravel-agent mentions and dispatches agents.
//
// The router, dispatcher, checkouter, retry tracker, handle store and reactor
// are injected because their policies (agent choice, CLI invocation, checkout,
// retry cap, dedupe, acknowledgement) are the interesting parts to substitute in
// tests. The HTTP client and logger are resolved from the global facades on
// purpose: they are framework singletons and the app facades are the supported
// way to reach them.
type Service struct {
	router     AgentRouter
	dispatcher Dispatcher
	checkouts  Checkouter
	retry      RetryTracker
	handles    HandleStore

	// reactor acknowledges a mention comment with an eyes reaction.
	reactor Reactor

	// mention is the handle that triggers an agent run (AGENT_MENTION).
	mention string

	apiToken string
	// workspaceRoot is the symlink-resolved workspace root.
	workspaceRoot string
	eventsURL     string

	// allowedSenders restricts which GitHub senders may trigger an agent run
	// (AGENT_ALLOWED_SENDERS). It is validated non-empty in NewService.
	allowedSenders map[string]struct{}

	// now returns the current time; injected so the per-run checkout name is
	// deterministic in tests.
	now func() time.Time
}

// NewService wires a Service from application configuration. It fails when
// AGENT_ALLOWED_SENDERS is empty, since running with no allowlist would let
// any GitHub sender trigger agent runs.
func NewService() (*Service, error) {
	config := facades.Config()

	senders := parseSenders(config.GetString("agent.allowed_senders"))
	if len(senders) == 0 {
		return nil, errors.New("agent: AGENT_ALLOWED_SENDERS must not be empty")
	}

	models, err := agentModels(config.Get("agent.models"))
	if err != nil {
		return nil, err
	}

	// The empty base URL keeps go-github's default (https://api.github.com/).
	reactor, err := newGitHubReactor(
		config.GetString("agent.github_token"),
		facades.Http().HttpClient(),
		"",
	)
	if err != nil {
		return nil, err
	}

	return &Service{
		router:         newAIRouter(),
		dispatcher:     newOpencodeDispatcher(models),
		checkouts:      newGitCheckouter(),
		retry:          newCacheRetryTracker(),
		handles:        newCacheHandleStore(),
		reactor:        reactor,
		mention:        normalizeMention(config.GetString("agent.mention")),
		apiToken:       config.GetString("agent.api_token"),
		workspaceRoot:  resolveWorkspace(config.GetString("agent.workspace")),
		eventsURL:      strings.TrimRight(config.GetString("agent.base_url"), "/"),
		allowedSenders: senders,
		now:            time.Now,
	}, nil
}

// allowed reports whether the sender may trigger an agent run. Comparison is
// case-insensitive; the allowlist is validated non-empty in NewService.
func (s *Service) allowed(sender string) bool {
	_, ok := s.allowedSenders[strings.ToLower(sender)]

	return ok
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
		facades.Log().Errorf("agent: fetch events: %v", err)
		return
	}
	if response.Failed() {
		facades.Log().Errorf("agent: fetch events: status %d", response.Status())
		// Drain the body so the connection is released.
		_, _ = response.Body()
		return
	}

	body, err := response.Body()
	if err != nil {
		facades.Log().Errorf("agent: read events response: %v", err)
		return
	}

	env, err := parseEnvelope([]byte(body))
	if err != nil {
		facades.Log().Errorf("agent: parse events envelope: %v", err)
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
			facades.Log().Errorf("agent: decode event payload %d: %v", ev.ID, err)
		}

		instruction, ok := ev.instruction(s.mention)
		if !ok {
			toDelete = append(toDelete, ev.ID)
			continue
		}

		if !s.allowed(ev.Sender) {
			facades.Log().Infof("agent: skip event %d: sender %q is not allowed", ev.ID, ev.Sender)
			toDelete = append(toDelete, ev.ID)
			continue
		}

		key := retryKey(ev)
		if s.handles.Has(key) {
			facades.Log().Infof("agent: skip event %d: already handled", ev.ID)
			toDelete = append(toDelete, ev.ID)
			continue
		}

		// The reaction is best-effort and network-bound, so give it its own short
		// deadline: a hung GitHub must never delay dispatch or the acknowledgement.
		reactCtx, cancel := context.WithTimeout(ctx, reactTimeout)
		s.react(reactCtx, ev)
		cancel()

		if err := s.handle(ctx, ev, instruction); err != nil {
			facades.Log().Errorf("agent: handle event %d: %v", ev.ID, err)
			if !s.retry.RecordFailure(key) {
				continue
			}
			facades.Log().Errorf("agent: giving up on event %d after %d attempts", ev.ID, maxAttempts)
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

// handle routes the instruction to an agent, resolves the target repository,
// and runs the agent against its checkout.
func (s *Service) handle(ctx context.Context, ev *event, instruction string) error {
	// The router sees the source repository; the agent sees the target.
	agent, repository, err := s.router.Route(ctx, ev.prompt(instruction, ev.Repository))
	if err != nil {
		return err
	}
	if repository == "" {
		repository = ev.Repository
	}

	dir := runDir(s.workspaceRoot, repository, s.now())
	if err := s.checkouts.Ensure(ctx, dir, repository); err != nil {
		return err
	}

	return s.dispatcher.Run(ctx, dispatch.Request{
		Dir:    dir,
		Agent:  agent,
		Prompt: ev.agentPrompt(agent, instruction),
	})
}

// react adds the eyes reaction to the triggering comment so the sender sees the
// mention was picked up. It is best-effort: a failure is logged and the run
// continues, and a body-only mention (no comment) is left alone. The caller
// bounds the call with reactTimeout so a hung request cannot stall the loop.
func (s *Service) react(ctx context.Context, ev *event) {
	collection, commentID, ok := ev.reaction()
	if !ok {
		return
	}

	if err := s.reactor.React(ctx, ev.Repository, collection, commentID); err != nil {
		facades.Log().Errorf("agent: react to event %d: %v", ev.ID, err)
	}
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
		facades.Log().Errorf("agent: delete event %d: %v", id, err)
		return
	}

	// The framework only reads and closes the body inside Body()/Bind()/Json().
	if _, err := res.Body(); err != nil {
		facades.Log().Errorf("agent: read delete response %d: %v", id, err)
	}

	if res.Failed() {
		facades.Log().Errorf("agent: delete event %d: status %d", id, res.Status())
	}
}
