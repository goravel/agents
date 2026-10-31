package features

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// projectLookupTimeout is the timeout the service applies to its one
// `paseo project ls`; pinned on purpose so a drift is a deliberate change here.
const projectLookupTimeout = 30 * time.Second

// scrubbedEnv returns the `-i PATH=... HOME=...` arguments the service runs
// every paseo subprocess with, behind the `env` command name.
func scrubbedEnv() []any {
	return []any{"-i", "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
}

const (
	// shipIssuePage is one issue_comment on an issue in goravel/goravel whose
	// instruction names the goravel/framework target repository.
	shipIssuePage = `{"data":[
		{"id":1,"delivery_id":"d-1","event":"issue_comment","action":"created",
		 "repository":"goravel/goravel","sender":"bowen",
		 "payload":"{\"number\":123,\"issue\":{\"number\":123,\"html_url\":\"https://github.com/goravel/goravel/issues/123\",\"body\":\"Tracking issue\"},\"comment\":{\"id\":24680,\"html_url\":\"https://github.com/goravel/goravel/issues/123#issuecomment-24680\",\"body\":\"@goravel-agent ship this issue in goravel/framework\"}}"}
	],"total":1,"page":1,"limit":50}`
)

var (
	// reviewPullRequestPage is one issue_comment on a pull request in
	// goravel/framework.
	reviewPullRequestPage = pullRequestPage("@goravel-agent review")

	// analyzePullRequestPage is the same pull request, asking for the analyze
	// agent.
	analyzePullRequestPage = pullRequestPage("@goravel-agent analyze this pull request")
)

// pullRequestPage is one issue_comment on pull request 42 in goravel/framework
// carrying body. It is a pull request so the prompt builder emits the PR: line.
func pullRequestPage(body string) string {
	// body is JSON inside the payload string, which is JSON inside the page, so
	// it takes one round of escaping per level.
	escaper := strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
		"\n", "\\n",
		"\r", "\\r",
		"\t", "\\t",
	)
	escaped := escaper.Replace(escaper.Replace(body))

	return `{"data":[
		{"id":1,"delivery_id":"d-1","event":"issue_comment","action":"created",
		 "repository":"goravel/framework","sender":"bowen",
		 "payload":"{\"number\":42,\"issue\":{\"number\":42,\"html_url\":\"https://github.com/goravel/framework/pull/42\",\"pull_request\":{\"html_url\":\"https://github.com/goravel/framework/pull/42\"},\"body\":\"A change\"},\"comment\":{\"id\":98765,\"html_url\":\"https://github.com/goravel/framework/pull/42#issuecomment-98765\",\"body\":\"` + escaped + `\"}}"}
	],"total":1,"page":1,"limit":50}`
}

// pollObservation is what one poll cycle observed at the process boundary: the
// run directory the paseo workspace was created for, the paseo project it was
// created in, the paseo run started inside it, and the acknowledged event ids.
type pollObservation struct {
	runDir         string
	project        string
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

// pollEnv is everything one poll cycle runs against: the relay, the mocked
// process boundary and the project folder the run directories are created in.
type pollEnv struct {
	relay      *relay
	process    *mocksprocess.Process
	projectDir string
	lastError  func() string
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

// setupPoll installs a relay serving page, the relay configuration and the
// mocked process boundary. Only the process boundary (the project lookup and the
// dispatcher's two paseo calls — workspace create, then run) is mocked; the
// relay, the live AI router and every other layer are real. The routed agent and
// repository are the live router's choice, so they are captured rather than
// constrained here and asserted by each test.
func (s *AgentPollTestSuite) setupPoll(page string) pollEnv {
	s.requireAIProvider()

	r := &relay{events: page}
	srv := httptest.NewServer(r.handler())
	s.T().Cleanup(srv.Close)

	s.Require().True(facades.Cache().Flush())

	// The project lookup points paseo at this directory, so the real
	// prepareRunDir stays inside the test's temp dir.
	projectDir, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)

	originalConfig := facades.Config().Get("agent")
	facades.Config().Add("agent", map[string]any{
		"base_url":        srv.URL + "/webhooks/events",
		"api_token":       "test-token",
		"project_name":    "tmp",
		"allowed_senders": "bowen",
		"models": map[string]string{
			constants.AgentShip:      "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentImplement: "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentReview:    "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentAnalyze:   "opencode-go/deepseek-v4.1-flash#high",
		},
	})
	s.T().Cleanup(func() { facades.Config().Add("agent", originalConfig) })

	// The project lookup and the dispatcher's two paseo calls are the only
	// external processes: mock them. Captures happen in the Run callbacks, not
	// the argument matchers: testify probes every same-method expectation by
	// running its matchers against each call (findExpectedCall -> Arguments.Diff),
	// so a matcher with a side effect would also fire for the other Run call. The
	// callbacks only run on a real match, and the matchers stay pure predicates.
	processMock := mocksprocess.NewProcess(s.T())
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

	// Capture the poller's own error output so a failure (for example a stale AI
	// key) is reported instead of a bare mock-expectation failure. MakeLog caches
	// the shared log singleton, so the instance map must be evicted after
	// rebinding for facades.Log() to pick up the recorder.
	originalLog := facades.App().MakeLog()
	recorder := &errorRecorder{Log: originalLog}
	facades.App().Instance(binding.Log, recorder)
	facades.App().Fresh(binding.Log)
	s.T().Cleanup(func() {
		facades.App().Instance(binding.Log, originalLog)
		facades.App().Fresh(binding.Log)
	})

	return pollEnv{
		relay:      r,
		process:    processMock,
		projectDir: projectDir,
		lastError:  recorder.lastError,
	}
}

// pollOnce runs one poll-events cycle against a single-event relay page and
// returns what it observed.
func (s *AgentPollTestSuite) pollOnce(page string) pollObservation {
	env := s.setupPoll(page)

	observation := pollObservation{}

	lookup := mocksprocess.NewResult(s.T())
	env.process.EXPECT().WithContext(testifymock.Anything).Return(env.process).Once()
	env.process.EXPECT().Timeout(projectLookupTimeout).Return(env.process).Once()
	env.process.EXPECT().Quietly().Return(env.process).Once()
	env.process.EXPECT().
		Run("env", append(scrubbedEnv(), "paseo", "project", "ls", "--json")...).
		Return(lookup).
		Once()
	lookup.EXPECT().Failed().Return(false).Once()
	lookup.EXPECT().
		Output().
		Return(`[{"projectId":"prj_tmp","name":"tmp","kind":"non_git","path":"` + env.projectDir + `"}]`).
		Once()

	// The dispatcher creates the run workspace first and runs the agent inside
	// it, so the create expectations are armed before the run ones.
	createResultMock := mocksprocess.NewResult(s.T())

	env.process.EXPECT().WithContext(testifymock.Anything).Return(env.process).Once()
	env.process.EXPECT().
		Path(testifymock.MatchedBy(func(p string) bool {
			return strings.HasPrefix(p, env.projectDir)
		})).
		Return(env.process).
		Once()
	env.process.EXPECT().Quietly().Return(env.process).Once()
	env.process.EXPECT().
		Run("env", "-i",
			"PATH="+os.Getenv("PATH"),
			"HOME="+os.Getenv("HOME"),
			"paseo", "workspace", "create",
			"--isolation", "local",
			"--path", testifymock.MatchedBy(func(p string) bool { return strings.HasPrefix(p, env.projectDir) }),
			"--project", "prj_tmp",
			"--title", testifymock.MatchedBy(func(title string) bool { return title != "" }),
			"--json").
		Run(func(_ string, args ...string) {
			// args = [-i, PATH…, HOME…, paseo, workspace, create, --isolation,
			//         local, --path, <dir>, --project, <id>, --title, <title>,
			//         --json].
			observation.runDir = args[9]
			observation.project = args[11]
			observation.workspaceTitle = args[13]
			s.Require().DirExists(observation.runDir)
			s.Require().Equal(env.projectDir, filepath.Dir(observation.runDir))
		}).
		Return(createResultMock).
		Once()
	createResultMock.EXPECT().Failed().Return(false).Once()
	createResultMock.EXPECT().
		Output().
		Return(`{"workspaceId":"wks_0123456789abcdef","project":"tmp","name":"probe","isolation":"local","cwd":"/tmp"}`).
		Once()

	env.process.EXPECT().WithContext(testifymock.Anything).Return(env.process).Once()
	env.process.EXPECT().
		Path(testifymock.MatchedBy(func(p string) bool {
			return strings.HasPrefix(p, env.projectDir)
		})).
		Run(func(p string) { observation.workspaceDir = p }).
		Return(env.process).
		Once()
	env.process.EXPECT().DisableBuffering().Return(env.process).Once()
	env.process.EXPECT().Quietly().Return(env.process).Once()
	runResultMock := mocksprocess.NewResult(s.T())
	runResultMock.EXPECT().Failed().Return(false).Once()
	env.process.EXPECT().
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
				// The prompt omits the Repository line: the run directory
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
		Return(runResultMock).
		Once()

	s.Require().NoError(facades.Artisan().Call("poll-events"))

	observation.acknowledged = env.relay.acknowledged()
	observation.lastError = env.lastError()

	return observation
}

// TestPollShipsIssueMention covers a `@goravel-agent ship this issue in
// goravel/framework` comment on an issue in goravel/goravel. The router is live,
// so this asserts the poller's observable outcome of the router's decision: the
// ship agent and a run directory for the routed goravel/framework repository
// (not the comment's own repository), plus the issue and comment links and the
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
	s.Require().True(strings.HasPrefix(filepath.Base(got.runDir), "goravel-framework-"))
	s.Require().Equal("prj_tmp", got.project)
	s.Require().Equal("wks_0123456789abcdef", got.workspaceID)
	s.Require().Equal(got.title, got.workspaceTitle, "workspace title must match the run title")
	s.Require().Equal(got.runDir, got.workspaceDir, "workspace must point at the per-run directory")
	s.Require().Contains(got.prompt, "Issue: https://github.com/goravel/goravel/issues/123")
	s.Require().Contains(got.prompt, "comment: https://github.com/goravel/goravel/issues/123#issuecomment-24680")
	s.Require().Contains(got.prompt, "Instruction: ship this issue in goravel/framework")
	s.Require().NotContains(got.prompt, "Repository:")
	s.Require().NotContains(got.prompt, "PR:")
	s.Require().NotContains(got.prompt, "comment the review result to the pr")
}

// TestPollReviewsPullRequest covers a `@goravel-agent review` comment on a pull
// request in goravel/framework. The router is live, so this asserts the poller's
// observable outcome: the review agent and a run directory for goravel/framework,
// plus the PR and comment links and the instruction.
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
	s.Require().True(strings.HasPrefix(filepath.Base(got.runDir), "goravel-framework-"))
	s.Require().Equal("prj_tmp", got.project)
	s.Require().Equal("wks_0123456789abcdef", got.workspaceID)
	s.Require().Equal(got.title, got.workspaceTitle, "workspace title must match the run title")
	s.Require().Equal(got.runDir, got.workspaceDir, "workspace must point at the per-run directory")
	s.Require().Contains(got.prompt, "PR: https://github.com/goravel/framework/pull/42")
	s.Require().Contains(got.prompt, "comment: https://github.com/goravel/framework/pull/42#issuecomment-98765")
	s.Require().Contains(got.prompt, "Instruction: review\ncomment the review result to the pr")
	s.Require().NotContains(got.prompt, "Repository:")
	s.Require().NotContains(got.prompt, "Issue:")
}

// TestPollAnalyzesPullRequest covers a `@goravel-agent analyze this pull request`
// comment on a pull request in goravel/framework. The router is live, so this
// asserts the poller's observable outcome: paseo runs --mode analyze against a
// run directory for goravel/framework, with the PR link the analyze prompt needs
// and the same post-to-PR line the review agent gets.
func (s *AgentPollTestSuite) TestPollAnalyzesPullRequest() {
	got := s.pollOnce(analyzePullRequestPage)

	s.Require().Empty(got.lastError)
	s.Require().Equal([]string{"1"}, got.acknowledged,
		"poll-events did not acknowledge the event; last error: %s", got.lastError)
	s.Require().Equal("analyze", got.mode,
		"live router did not choose the analyze agent (context: %s)", got.lastError)
	s.Require().Equal("opencode-go/deepseek-v4.1-flash", got.model)
	s.Require().Equal("high", got.thinking)
	s.Require().NotEmpty(got.title)
	s.Require().Equal(got.title, got.workspaceTitle, "workspace title must match the run title")
	s.Require().Equal("prj_tmp", got.project)
	s.Require().Equal(got.runDir, got.workspaceDir, "workspace must point at the per-run directory")
	s.Require().Contains(got.prompt, "PR: https://github.com/goravel/framework/pull/42")
	s.Require().Contains(got.prompt, "comment: https://github.com/goravel/framework/pull/42#issuecomment-98765")
	s.Require().Contains(got.prompt, "Instruction: analyze this pull request\ncomment the review result to the pr")
	s.Require().Equal(1, strings.Count(got.prompt, "comment the review result to the pr"),
		"the post-to-PR line must appear once, not once per agent branch")
	s.Require().NotContains(got.prompt, "Repository:")
	s.Require().NotContains(got.prompt, "Issue:")
}

// TestAgentModelsConfigCoversRoutableAgents checks the shipped agent.models map.
func (s *AgentPollTestSuite) TestAgentModelsConfigCoversRoutableAgents() {
	models, ok := facades.Config().Get("agent.models").(map[string]string)
	s.Require().True(ok, "config/agent.go must register agent.models as map[string]string")

	for _, mode := range []string{constants.AgentShip, constants.AgentImplement, constants.AgentReview, constants.AgentAnalyze} {
		entry := strings.TrimSpace(models[mode])
		s.Require().NotEmpty(entry, "agent.models has no entry for %q", mode)
		model, _, _ := strings.Cut(entry, "#")
		s.Require().NotEmpty(strings.TrimSpace(model), "agent.models entry for %q has no model", mode)
	}
}

// TestPollRejectsBlankProjectName pins that a blank AGENT_PROJECT_NAME stops the
// cycle before it polls: there is nothing to create the workspace in.
func (s *AgentPollTestSuite) TestPollRejectsBlankProjectName() {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	s.T().Cleanup(srv.Close)

	originalConfig := facades.Config().Get("agent")
	facades.Config().Add("agent", map[string]any{
		"base_url":        srv.URL + "/webhooks/events",
		"api_token":       "test-token",
		"project_name":    "",
		"allowed_senders": "bowen",
		"models": map[string]string{
			constants.AgentShip:      "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentImplement: "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentReview:    "opencode-go/deepseek-v4.1-flash#high",
			constants.AgentAnalyze:   "opencode-go/deepseek-v4.1-flash#high",
		},
	})
	s.T().Cleanup(func() { facades.Config().Add("agent", originalConfig) })

	err := facades.Artisan().Call("poll-events")

	s.Require().ErrorContains(err, "agent: AGENT_PROJECT_NAME must name a paseo project")
	s.Require().False(called.Load(), "the relay must not be called")
}

// TestPollStopsOnUnknownProject pins the no-fallback rule end to end: an unknown
// project name fails the cycle, acknowledges nothing, and creates neither a
// workspace nor a run.
func (s *AgentPollTestSuite) TestPollStopsOnUnknownProject() {
	env := s.setupPoll(reviewPullRequestPage)

	lookup := mocksprocess.NewResult(s.T())
	env.process.EXPECT().WithContext(testifymock.Anything).Return(env.process).Once()
	env.process.EXPECT().Timeout(projectLookupTimeout).Return(env.process).Once()
	env.process.EXPECT().Quietly().Return(env.process).Once()
	env.process.EXPECT().
		Run("env", append(scrubbedEnv(), "paseo", "project", "ls", "--json")...).
		Return(lookup).
		Once()
	lookup.EXPECT().Failed().Return(false).Once()
	lookup.EXPECT().Output().Return(`[]`).Once()

	err := facades.Artisan().Call("poll-events")

	s.Require().ErrorContains(err, `agent: AGENT_PROJECT_NAME="tmp": no paseo project with that name`)
	s.Require().Empty(env.relay.acknowledged(), "the mention stays queued for the next tick")
	// Neither `paseo workspace create` nor `paseo run` ever started.
	env.process.AssertNotCalled(s.T(), "Path", testifymock.Anything)
	env.process.AssertNotCalled(s.T(), "DisableBuffering")
}
