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

	"goravel/app/constants"
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
	// cloneTimeout is the git clone timeout the checkouter applies.
	cloneTimeout = 5 * time.Minute

	// shipIssuePage is one issue_comment on an issue in goravel/goravel whose
	// instruction names the goravel/framework target repository.
	shipIssuePage = `{"data":[
		{"id":1,"delivery_id":"d-1","event":"issue_comment","action":"created",
		 "repository":"goravel/goravel","sender":"bowen",
		 "payload":"{\"number\":123,\"issue\":{\"number\":123,\"html_url\":\"https://github.com/goravel/goravel/issues/123\",\"body\":\"Tracking issue\"},\"comment\":{\"id\":24680,\"html_url\":\"https://github.com/goravel/goravel/issues/123#issuecomment-24680\",\"body\":\"@goravel-agent ship this issue in goravel/framework\"}}"}
	],"total":1,"page":1,"limit":50}`

	// reviewPullRequestPage is one issue_comment on a pull request in
	// goravel/framework.
	reviewPullRequestPage = `{"data":[
		{"id":1,"delivery_id":"d-1","event":"issue_comment","action":"created",
		 "repository":"goravel/framework","sender":"bowen",
		 "payload":"{\"number\":42,\"issue\":{\"number\":42,\"html_url\":\"https://github.com/goravel/framework/pull/42\",\"pull_request\":{\"html_url\":\"https://github.com/goravel/framework/pull/42\"},\"body\":\"A change\"},\"comment\":{\"id\":98765,\"html_url\":\"https://github.com/goravel/framework/pull/42#issuecomment-98765\",\"body\":\"@goravel-agent review\"}}"}
	],"total":1,"page":1,"limit":50}`
)

// pollObservation is what one poll cycle observed at the process boundary: the
// routed checkout it cloned, the paseo workspace it created and the paseo run it
// started inside it, and the acknowledged event ids.
type pollObservation struct {
	cloneRemote    string
	cloneDir       string
	workspaceID    string
	workspaceDir   string
	workspaceTitle string
	mode           string
	model          string
	thinking       string
	title          string
	prompt         string
	acknowledged   []string
	lastError      string
}

type AgentPollTestSuite struct {
	suite.Suite
	tests.TestCase
}

func TestAgentPollTestSuite(t *testing.T) {
	suite.Run(t, new(AgentPollTestSuite))
}

// requireAIProvider skips the test when no AI provider is configured, since
// routing always goes through facades.AI.
func (s *AgentPollTestSuite) requireAIProvider() {
	provider := facades.Config().GetString("ai.default")
	if provider == "" {
		s.T().Skip("set AI_PROVIDER and DEEPSEEK_API_KEY to run this test")
	}
	if facades.Config().GetString("ai.providers."+provider+".key") == "" {
		s.T().Skipf("set an API key for the %q AI provider to run this test", provider)
	}
}

// pollOnce runs one poll-events cycle against a single-event relay page. Only
// the process boundary (the checkouter's git clone and the dispatcher's two
// paseo calls — workspace create, then run) is mocked; the relay, the live AI
// router and every other layer are real. The routed agent and repository are the
// live router's choice, so they are captured rather than constrained here and
// asserted by each test.
func (s *AgentPollTestSuite) pollOnce(page string) pollObservation {
	s.requireAIProvider()

	// Relay returning the event.
	r := &relay{events: page}
	srv := httptest.NewServer(r.handler())
	s.T().Cleanup(srv.Close)

	// Fresh, empty workspace plus the relay configuration. Each run checks out
	// into its own timestamped directory, so the checkout never pre-exists and
	// the real git checkouter clones before the paseo run.
	s.Require().True(facades.Cache().Flush())

	resolved, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)

	originalConfig := facades.Config().Get("agent")
	facades.Config().Add("agent", map[string]any{
		"base_url":        srv.URL + "/webhooks/events",
		"api_token":       "test-token",
		"workspace":       resolved,
		"allowed_senders": "bowen",
		"models": map[string]string{
			constants.AgentShip:      "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentImplement: "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentReview:    "opencode-go/deepseek-v4.1-flash#high",
		},
	})
	s.T().Cleanup(func() { facades.Config().Add("agent", originalConfig) })

	observation := pollObservation{}

	// The checkouter's git clone and the dispatcher's two paseo calls are the
	// only external processes: mock both. Ensure claims the per-run directory
	// before invoking git, so the clone callback only asserts it exists. The
	// routed repository and agent are captured here and asserted per test.
	//
	// Captures happen in the Run callbacks, not the argument matchers: testify
	// probes every same-method expectation by running its matchers against each
	// call (findExpectedCall -> Arguments.Diff), so a matcher with a side effect
	// would also fire for the other Run call. The callbacks only run on a real
	// match, and the matchers stay pure predicates.
	processMock := mocksprocess.NewProcess(s.T())
	cloneResultMock := mocksprocess.NewResult(s.T())
	resultMock := mocksprocess.NewResult(s.T())

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Timeout(cloneTimeout).Return(processMock).Once()
	processMock.EXPECT().
		Run("git", "clone", "--depth", "1",
			testifymock.MatchedBy(func(remote string) bool {
				return strings.HasPrefix(remote, "git@github.com:")
			}),
			testifymock.Anything).
		Run(func(_ string, args ...string) {
			// args = [clone, --depth, 1, <remote>, <dir>].
			observation.cloneRemote = args[len(args)-2]
			observation.cloneDir = args[len(args)-1]
			s.Require().DirExists(observation.cloneDir)
			s.Require().True(strings.HasPrefix(observation.cloneDir, resolved))
		}).
		Return(cloneResultMock).
		Once()
	cloneResultMock.EXPECT().Failed().Return(false).Once()

	// The dispatcher creates the run workspace first and runs the agent inside
	// it, so the create expectations are armed before the run ones.
	createResultMock := mocksprocess.NewResult(s.T())

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().
		Path(testifymock.MatchedBy(func(p string) bool {
			return strings.HasPrefix(p, resolved)
		})).
		Return(processMock).
		Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().
		Run("env", "-i",
			"PATH="+os.Getenv("PATH"),
			"HOME="+os.Getenv("HOME"),
			"paseo", "workspace", "create",
			"--isolation", "local",
			"--path", testifymock.MatchedBy(func(p string) bool { return strings.HasPrefix(p, resolved) }),
			"--title", testifymock.MatchedBy(func(title string) bool { return title != "" }),
			"--json").
		Run(func(_ string, args ...string) {
			// args = [-i, PATH…, HOME…, paseo, workspace, create, --isolation,
			//         local, --path, <dir>, --title, <title>, --json].
			observation.workspaceDir = args[9]
			observation.workspaceTitle = args[11]
		}).
		Return(createResultMock).
		Once()
	createResultMock.EXPECT().Failed().Return(false).Once()
	createResultMock.EXPECT().
		Output().
		Return(`{"workspaceId":"wks_0123456789abcdef","project":"goravel-framework-20260101-000000","name":"probe","isolation":"local","cwd":"/tmp"}`).
		Once()

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().
		Path(testifymock.MatchedBy(func(p string) bool {
			return strings.HasPrefix(p, resolved)
		})).
		Return(processMock).
		Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().
		Run("env", "-i",
			"PATH="+os.Getenv("PATH"),
			"HOME="+os.Getenv("HOME"),
			"paseo", "run",
			"--provider", "opencode",
			"--mode", testifymock.Anything,
			"--model", testifymock.Anything,
			"--thinking", testifymock.Anything,
			"--title", testifymock.MatchedBy(func(title string) bool { return title != "" }),
			"--workspace", "wks_0123456789abcdef",
			"--no-wait", "--",
			testifymock.MatchedBy(func(prompt string) bool {
				// The prompt omits the Repository line: the checkout directory
				// implies the repository.
				return !strings.HasPrefix(prompt, "Repository: ")
			})).
		Run(func(_ string, args ...string) {
			// args = [-i, PATH…, HOME…, paseo, run, --provider, opencode,
			//         --mode, <mode>, --model, <model>, --thinking, <thinking>,
			//         --title, <title>, --workspace, <id>, --no-wait, --,
			//         <prompt>].
			observation.mode = args[8]
			observation.model = args[10]
			observation.thinking = args[12]
			observation.title = args[14]
			observation.workspaceID = args[16]
			observation.prompt = args[19]
		}).
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

	observation.acknowledged = r.acknowledged()
	observation.lastError = recorder.lastError()

	return observation
}

// TestPollShipsIssueMention covers a `@goravel-agent ship this issue in
// goravel/framework` comment on an issue in goravel/goravel. The router is live,
// so this asserts the poller's observable outcome of the router's decision: the
// ship agent and a checkout of the routed goravel/framework repository (not the
// comment's own repository), plus the issue and comment links and the
// instruction.
func (s *AgentPollTestSuite) TestPollShipsIssueMention() {
	got := s.pollOnce(shipIssuePage)

	s.Require().Empty(got.lastError)
	s.Require().Equal([]string{"1"}, got.acknowledged,
		"poll-events did not acknowledge the event; last error: %s", got.lastError)
	s.Require().Equal("ship", got.mode,
		"live router did not choose the ship agent (context: %s)", got.lastError)
	s.Require().Equal("opencode-go/deepseek-v4.1-flash", got.model)
	s.Require().Equal("high", got.thinking)
	s.Require().NotEmpty(got.title)
	s.Require().NotContains(got.title, "\n")
	s.Require().LessOrEqual(len([]rune(got.title)), 60)
	s.Require().Equal("git@github.com:goravel/framework.git", got.cloneRemote)
	s.Require().True(strings.HasPrefix(filepath.Base(got.cloneDir), "goravel-framework-"))
	s.Require().Equal("wks_0123456789abcdef", got.workspaceID)
	s.Require().Equal(got.title, got.workspaceTitle, "workspace title must match the run title")
	s.Require().Equal(got.cloneDir, got.workspaceDir, "workspace must point at the per-run checkout")
	s.Require().Contains(got.prompt, "Issue: https://github.com/goravel/goravel/issues/123")
	s.Require().Contains(got.prompt, "comment: https://github.com/goravel/goravel/issues/123#issuecomment-24680")
	s.Require().Contains(got.prompt, "Instruction: ship this issue in goravel/framework")
	s.Require().NotContains(got.prompt, "Repository:")
	s.Require().NotContains(got.prompt, "PR:")
	s.Require().NotContains(got.prompt, "comment the review result to the pr")
}

// TestPollReviewsPullRequest covers a `@goravel-agent review` comment on a pull
// request in goravel/framework. The router is live, so this asserts the poller's
// observable outcome: the review agent and a checkout of goravel/framework, plus
// the PR and comment links and the instruction.
func (s *AgentPollTestSuite) TestPollReviewsPullRequest() {
	got := s.pollOnce(reviewPullRequestPage)

	s.Require().Empty(got.lastError)
	s.Require().Equal([]string{"1"}, got.acknowledged,
		"poll-events did not acknowledge the event; last error: %s", got.lastError)
	s.Require().Equal("review", got.mode,
		"live router did not choose the review agent (context: %s)", got.lastError)
	s.Require().Equal("opencode-go/deepseek-v4.1-flash", got.model)
	s.Require().Equal("high", got.thinking)
	s.Require().NotEmpty(got.title)
	s.Require().NotContains(got.title, "\n")
	s.Require().LessOrEqual(len([]rune(got.title)), 60)
	s.Require().Equal("git@github.com:goravel/framework.git", got.cloneRemote)
	s.Require().True(strings.HasPrefix(filepath.Base(got.cloneDir), "goravel-framework-"))
	s.Require().Equal("wks_0123456789abcdef", got.workspaceID)
	s.Require().Equal(got.title, got.workspaceTitle, "workspace title must match the run title")
	s.Require().Equal(got.cloneDir, got.workspaceDir, "workspace must point at the per-run checkout")
	s.Require().Contains(got.prompt, "PR: https://github.com/goravel/framework/pull/42")
	s.Require().Contains(got.prompt, "comment: https://github.com/goravel/framework/pull/42#issuecomment-98765")
	s.Require().Contains(got.prompt, "Instruction: review\ncomment the review result to the pr")
	s.Require().NotContains(got.prompt, "Repository:")
	s.Require().NotContains(got.prompt, "Issue:")
}

// TestAgentModelsConfigCoversRoutableAgents checks the shipped agent.models map.
func (s *AgentPollTestSuite) TestAgentModelsConfigCoversRoutableAgents() {
	models, ok := facades.Config().Get("agent.models").(map[string]string)
	s.Require().True(ok, "config/agent.go must register agent.models as map[string]string")

	for _, mode := range []string{constants.AgentShip, constants.AgentImplement, constants.AgentReview} {
		entry := strings.TrimSpace(models[mode])
		s.Require().NotEmpty(entry, "agent.models has no entry for %q", mode)
		model, _, _ := strings.Cut(entry, "#")
		s.Require().NotEmpty(strings.TrimSpace(model), "agent.models entry for %q has no model", mode)
	}
}
