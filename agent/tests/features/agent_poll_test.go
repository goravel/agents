package features

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

	// cloneTimeout is the git clone timeout the checkouter applies.
	cloneTimeout = 5 * time.Minute

	// mentionPage is one GitHub issue comment containing the @goravel-agent
	// mention.
	mentionPage = `{"data":[
		{"id":1,"delivery_id":"d-1","event":"issue_comment","action":"created",
		 "repository":"goravel/goravel","sender":"bowen",
		 "payload":"{\"number\":123,\"pull_request\":{\"number\":123,\"html_url\":\"https://github.com/goravel/goravel/pull/123\"},\"comment\":{\"id\":24680,\"html_url\":\"https://github.com/goravel/goravel/pull/123#issuecomment-24680\",\"body\":\"@goravel-agent review\"}}"}
	],"total":1,"page":1,"limit":50}`
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
		s.T().Skip("set AI_PROVIDER and DEEPSEEK_API_KEY to run this test")
	}
	if facades.Config().GetString("ai.providers."+provider+".key") == "" {
		s.T().Skipf("set an API key for the %q AI provider to run this test", provider)
	}

	// Relay returning the mention event.
	r := &relay{events: mentionPage}
	srv := httptest.NewServer(r.handler())
	s.T().Cleanup(srv.Close)

	// Fresh, empty workspace plus the relay configuration. Each run checks out
	// into its own timestamped directory, so the checkout never pre-exists and
	// the real git checkouter clones before the opencode run.
	s.Require().True(facades.Cache().Flush())

	resolved, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)

	originalConfig := facades.Config().Get("agent")
	facades.Config().Add("agent", map[string]any{
		"base_url":        srv.URL + "/webhooks/events",
		"api_token":       "test-token",
		"workspace":       resolved,
		"allowed_senders": "bowen",
	})
	s.T().Cleanup(func() { facades.Config().Add("agent", originalConfig) })

	// The checkouter's git clone and the dispatcher's opencode run are the only
	// external processes: mock both. The clone callback needs no work (Ensure
	// creates and renames its own staging directory), and the agent name and
	// target repository are chosen by the live AI, so accept any supported agent
	// and any checkout under the workspace and only pin the parts of the prompt
	// the poller controls itself.
	processMock := mocksprocess.NewProcess(s.T())
	cloneResultMock := mocksprocess.NewResult(s.T())
	resultMock := mocksprocess.NewResult(s.T())

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Timeout(cloneTimeout).Return(processMock).Once()
	processMock.EXPECT().
		Run("git", "clone", "--depth", "1",
			testifymock.MatchedBy(func(remote string) bool {
				return strings.HasPrefix(remote, "https://github.com/goravel/")
			}),
			testifymock.Anything).
		Return(cloneResultMock).
		Once()
	cloneResultMock.EXPECT().Failed().Return(false).Once()

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().
		Path(testifymock.MatchedBy(func(p string) bool {
			return strings.HasPrefix(p, resolved)
		})).
		Return(processMock).
		Once()
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
				return agent == "review" || agent == "ship" || agent == "implement"
			}),
			"--",
			testifymock.MatchedBy(func(prompt string) bool {
				// Pin the parts the poller owns (the links from the payload and
				// the instruction) while leaving the routed repository value to
				// the live AI. The agent prompt omits the Repository line: the
				// checkout's working directory implies the repository.
				return !strings.HasPrefix(prompt, "Repository: ") &&
					strings.Contains(prompt, "comment: https://github.com/goravel/goravel/pull/123#issuecomment-24680") &&
					strings.Contains(prompt, "Instruction: review")
			})).
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
