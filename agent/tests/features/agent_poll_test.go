package features

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	testifymock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/goravel/framework/contracts/binding"
	contractsfoundation "github.com/goravel/framework/contracts/foundation"
	contractslog "github.com/goravel/framework/contracts/log"
	mocksprocess "github.com/goravel/framework/mocks/process"
	"github.com/goravel/framework/process"

	"goravel/app/facades"
	"goravel/tests"
)

// errorRecorder wraps the real logger and remembers the last error it logged,
// so a dispatch failure reports the poller's own error instead of an opaque
// mock expectation failure.
type errorRecorder struct {
	contractslog.Log

	mu      sync.Mutex
	lastErr string
}

// Errorf records the formatted error before delegating to the real logger.
func (r *errorRecorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	r.lastErr = fmt.Sprintf(format, args...)
	r.mu.Unlock()

	r.Log.Errorf(format, args...)
}

// lastError returns the last error passed to Errorf, or "" when none was logged.
func (r *errorRecorder) lastError() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.lastErr
}

// relay is an in-process stand-in for the relay events API.
type relay struct {
	mu      sync.Mutex
	events  string
	deleted []string
}

func (r *relay) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /webhooks/events", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Api-Token") != "test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.mu.Lock()
		body := r.events
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("DELETE /webhooks/events/{id}", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Api-Token") != "test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.mu.Lock()
		r.deleted = append(r.deleted, req.PathValue("id"))
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (r *relay) acknowledged() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.deleted...)
}

const (
	// The opencode run timeout the dispatcher applies.
	opencodeTimeout = 30 * time.Minute

	// mentionPage is one GitHub issue comment containing the @goravel-agent
	// mention.
	mentionPage = `{"data":[
		{"id":1,"delivery_id":"d-1","event":"issue_comment","action":"created",
		 "repository":"goravel/goravel","sender":"bowen",
		 "payload":"{\"number\":123,\"comment\":{\"id\":24680,\"body\":\"@goravel-agent review\"}}"}
	],"total":1,"page":1,"limit":50}`

	// expectedPrompt is the exact instruction the poller builds for mentionPage;
	// it does not depend on which agent the router picks.
	expectedPrompt = "Repository: goravel/goravel\n" +
		"Event: issue_comment\n" +
		"Comment ID: 24680\n" +
		"Number: #123\n\n" +
		"Instruction:\n" +
		"review"
)

type AgentPollTestSuite struct {
	suite.Suite
	tests.TestCase
}

func TestAgentPollTestSuite(t *testing.T) {
	suite.Run(t, new(AgentPollTestSuite))
}

// TestPollDispatchesMention covers the happy path: receiving a `@goravel-agent
// review` event routes an agent, runs opencode against the checkout and
// acknowledges the event.
func (s *AgentPollTestSuite) TestPollDispatchesMention() {
	// Routing always goes through facades.AI, so require a configured provider.
	provider := facades.Config().GetString("ai.default")
	if provider == "" {
		s.T().Skip("set AI_PROVIDER and OPENAI_API_KEY to run this test")
	}
	if facades.Config().GetString("ai.providers."+provider+".key") == "" {
		s.T().Skipf("set an API key for the %q AI provider to run this test", provider)
	}

	// Relay returning the mention event.
	r := &relay{events: mentionPage}
	srv := httptest.NewServer(r.handler())
	s.T().Cleanup(srv.Close)

	// Fresh workspace with a pre-created checkout so the real git checkouter
	// short-circuits on Stat, plus the relay configuration.
	s.Require().True(facades.Cache().Flush())

	resolved, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	repoDir := filepath.Join(resolved, "goravel", "goravel")
	s.Require().NoError(os.MkdirAll(repoDir, 0o755))

	originalConfig := facades.Config().Get("agent")
	facades.Config().Add("agent", map[string]any{
		"base_url":        srv.URL + "/webhooks/events",
		"api_token":       "test-token",
		"workspace":       resolved,
		"allowed_senders": "bowen",
	})
	s.T().Cleanup(func() { facades.Config().Add("agent", originalConfig) })

	// The dispatcher's opencode run is the only external process: mock it. The
	// agent name is chosen by the live AI, so accept any supported agent.
	processMock := mocksprocess.NewProcess(s.T())
	resultMock := mocksprocess.NewResult(s.T())

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path(repoDir).Return(processMock).Once()
	processMock.EXPECT().Timeout(opencodeTimeout).Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().OnOutput(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().
		Run("env", "-i",
			"PATH="+os.Getenv("PATH"),
			"HOME="+os.Getenv("HOME"),
			"opencode", "run", "--agent",
			testifymock.MatchedBy(func(agent string) bool {
				return agent == "review" || agent == "plan" || agent == "implement"
			}),
			"--", expectedPrompt).
		Return(resultMock).
		Once()
	resultMock.EXPECT().Failed().Return(false).Once()

	facades.App().Instance(binding.Process, processMock)
	s.T().Cleanup(func() {
		// Restore the framework's non-shared factory (process.ServiceProvider
		// registers Process with Bind, so every resolve returns a fresh
		// instance) rather than a shared instance, then evict the cached mock.
		facades.App().Bind(binding.Process, func(contractsfoundation.Application) (any, error) {
			return process.New(), nil
		})
		facades.App().Fresh(binding.Process)
	})

	// Capture the poller's own error output so a routing failure (for example a
	// stale AI key) is reported instead of a bare mock-expectation failure.
	// MakeLog caches the shared log singleton, so the instance map must be
	// evicted after rebinding for facades.Log() to pick up the recorder.
	originalLog := facades.App().MakeLog()
	recorder := &errorRecorder{Log: originalLog}
	facades.App().Instance(binding.Log, recorder)
	facades.App().Fresh(binding.Log)
	s.T().Cleanup(func() {
		facades.App().Instance(binding.Log, originalLog)
		facades.App().Fresh(binding.Log)
	})

	s.Require().NoError(facades.Artisan().Call("poll-events"))

	// The mention was dispatched and the event acknowledged through the relay.
	s.Require().Equal([]string{"1"}, r.acknowledged(),
		"poll-events did not acknowledge the event; last error: %s", recorder.lastError())
}
