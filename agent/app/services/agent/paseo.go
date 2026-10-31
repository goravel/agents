package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goravel/app/facades"
)

const (
	// paseoTimeout bounds every paseo CLI call, so the package has one deadline
	// for the CLI.
	paseoTimeout = 30 * time.Second
	// paseoBin is the CLI that dispatches runs.
	paseoBin = "paseo"
	// workspaceIsolation is the workspace kind paseo creates for a run: the
	// per-run directory already exists, so the workspace must point at it rather
	// than mint a new worktree.
	workspaceIsolation = "local"
)

// Project is the paseo project every run workspace is created in: the id
// `paseo workspace create --project` needs, and the folder its run directories
// live in.
type Project struct {
	ID   string
	Path string
}

// paseoError marks a paseo CLI failure that replaying an event cannot fix: a
// failed project lookup or workspace create. Poll stops on it without consuming
// the event's retry budget and without acknowledging the page, because the same
// call fails again for every event.
type paseoError struct{ err error }

func (e paseoError) Error() string { return e.err.Error() }

func (e paseoError) Unwrap() error { return e.err }

// Paseo runs every paseo CLI call and remembers the project runs are created in:
// the first call that needs it pays one `paseo project ls`, later calls reuse the
// answer.
type Paseo struct {
	bin  string
	name string

	mu       sync.Mutex
	resolved bool
	project  Project
}

// newPaseo returns a Paseo that runs bin and resolves the project named name.
func newPaseo(bin, name string) *Paseo {
	return &Paseo{bin: bin, name: name}
}

// sharedPaseos holds the process's clients. `schedule:run` runs poll-events
// in-process on every tick, so a Paseo owned by the tick's service would pay the
// lookup again on each one.
var (
	sharedPaseosMu sync.Mutex
	sharedPaseos   = map[string]*Paseo{}
)

// sharedPaseo returns the one Paseo for bin and name in this process, so every
// in-process tick reuses the same cache.
func sharedPaseo(bin, name string) *Paseo {
	sharedPaseosMu.Lock()
	defer sharedPaseosMu.Unlock()

	key := bin + "\x00" + name
	if paseo, ok := sharedPaseos[key]; ok {
		return paseo
	}

	paseo := newPaseo(bin, name)
	sharedPaseos[key] = paseo

	return paseo
}

// Project returns the project named by AGENT_PROJECT_NAME, resolving it on the
// first call. A failure is not cached and comes back as a paseoError, so the next
// call retries.
func (p *Paseo) Project(ctx context.Context) (Project, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.resolved {
		return p.project, nil
	}

	project, err := p.resolveProject(ctx)
	if err != nil {
		return Project{}, paseoError{err: err}
	}

	p.project, p.resolved = project, true

	return project, nil
}

// resolveProject resolves the paseo project named by AGENT_PROJECT_NAME with
// `paseo project ls --json` ([{projectId,name,kind,path}]). `-q` is not used: it
// prints ids only and cannot answer a name lookup. It never falls back — a blank
// or unknown name, a failing paseo, unreadable output, or a project without an id
// or path is an error.
func (p *Paseo) resolveProject(ctx context.Context) (Project, error) {
	if p.name == "" {
		return Project{}, errors.New("agent: AGENT_PROJECT_NAME must name a paseo project")
	}

	// Buffering must stay on: the id and path are read from stdout.
	result := facades.Process().
		WithContext(ctx).
		Timeout(paseoTimeout).
		Quietly().
		Run("env", append(scrubbedEnv(), p.bin, "project", "ls", "--json")...)

	if result.Failed() {
		return Project{}, fmt.Errorf(
			"agent: AGENT_PROJECT_NAME=%q: `paseo project ls --json` failed (exit %d): %s",
			p.name, result.ExitCode(), strings.Join(strings.Fields(result.ErrorOutput()), " "))
	}

	var projects []struct {
		ProjectID string `json:"projectId"`
		Name      string `json:"name"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal([]byte(result.Output()), &projects); err != nil {
		return Project{}, fmt.Errorf("agent: AGENT_PROJECT_NAME=%q: decode `paseo project ls --json`: %w", p.name, err)
	}

	for _, project := range projects {
		if project.Name != p.name {
			continue
		}
		if project.ProjectID == "" || project.Path == "" {
			return Project{}, fmt.Errorf("agent: AGENT_PROJECT_NAME=%q: that paseo project has no id or path", p.name)
		}

		return Project{ID: project.ProjectID, Path: project.Path}, nil
	}

	return Project{}, fmt.Errorf("agent: AGENT_PROJECT_NAME=%q: no paseo project with that name", p.name)
}

// CreateWorkspace creates the paseo workspace a run will live in, titled with the
// run title so the session name in `paseo workspace ls` matches the run in
// `paseo ls`. It returns the workspace id and the command it attempted — the
// command comes back even on failure, so the caller can log what did not work.
// The JSON branch is read for the id: with --json paseo prints only
// {"workspaceId":"wks_…","project":…,"name":…,"isolation":"local","cwd":…}. A
// failure is a paseoError: every event of the page would hit it again.
func (p *Paseo) CreateWorkspace(ctx context.Context, dir, projectID, title string) (string, []string, error) {
	if projectID == "" {
		return "", nil, paseoError{err: errors.New("agent: a paseo project is required to create a run workspace")}
	}

	command := []string{
		p.bin, "workspace", "create",
		"--isolation", workspaceIsolation,
		"--path", dir,
		"--project", projectID,
		"--title", title,
		"--json",
	}

	// Buffering must stay on for this call: DisableBuffering makes Result.Output
	// empty, and the id is read from stdout. Quietly alone only stops the copy to
	// the terminal.
	result := facades.Process().
		WithContext(ctx).
		Timeout(paseoTimeout).
		Path(dir).
		Quietly().
		Run("env", append(scrubbedEnv(), command...)...)

	if result.Failed() {
		// paseo explains itself on stderr, while Result.Error is only the wait
		// error (`exit status 1`), so the message needs both: the stderr collapsed
		// to the one line the log promises, and the wait error wrapped so a caller
		// can still match it. Without stderr (a process that never started) the
		// wrapped error is the whole reason.
		reason := strings.Join(strings.Fields(result.ErrorOutput()), " ")
		if reason != "" {
			return "", command, paseoError{err: fmt.Errorf("paseo workspace create failed (exit %d): %s: %w", result.ExitCode(), reason, result.Error())}
		}

		return "", command, paseoError{err: fmt.Errorf("paseo workspace create failed (exit %d): %w", result.ExitCode(), result.Error())}
	}

	var created struct {
		WorkspaceID string `json:"workspaceId"`
	}
	if err := json.Unmarshal([]byte(result.Output()), &created); err != nil {
		return "", command, paseoError{err: fmt.Errorf("decode paseo workspace create output: %w", err)}
	}
	if created.WorkspaceID == "" {
		return "", command, paseoError{err: errors.New("paseo workspace create returned no workspace id")}
	}

	return created.WorkspaceID, command, nil
}

// RunRequest is one `paseo run` invocation: the directory it runs in, the
// provider and mode, the model and thinking level, the run title, the workspace
// it runs in, and the prompt.
type RunRequest struct {
	Dir         string
	Provider    string
	Mode        string
	Model       string
	Thinking    string
	Title       string
	WorkspaceID string
	Prompt      string
}

// Run executes `paseo run --provider <provider> --mode <mode> --model <model>
// [--thinking <level>] --title <title> --workspace <id> --no-wait -- <prompt>` in
// the request's directory with a scrubbed environment, and returns the command it
// attempted. A failure is an ordinary event failure, not a paseoError: replaying
// the event may well succeed.
func (p *Paseo) Run(ctx context.Context, request RunRequest) ([]string, error) {
	command := []string{
		p.bin, "run",
		"--provider", request.Provider,
		"--mode", request.Mode,
		"--model", request.Model,
	}
	if request.Thinking != "" {
		command = append(command, "--thinking", request.Thinking)
	}
	// Always set: the workspace and the run must share one title, and the service
	// guarantees a non-blank one.
	command = append(command, "--title", request.Title, "--workspace", request.WorkspaceID)
	// The run's setup subagent clones through `--env`, because paseo starts it
	// with a scrubbed environment; the depth stays unset, so it clones fully.
	command = append(command, "--env", "GITHUB_CLONE_PREFIX="+gitCloneURL)
	// `--` keeps the prompt from being read as a flag.
	command = append(command, "--no-wait", "--", request.Prompt)

	result := facades.Process().
		WithContext(ctx).
		Timeout(paseoTimeout).
		Path(request.Dir).
		DisableBuffering().
		Quietly().
		Run("env", append(scrubbedEnv(), command...)...)

	if result.Failed() {
		return command, fmt.Errorf("paseo run failed (exit %d): %w", result.ExitCode(), result.Error())
	}

	return command, nil
}

// runTimeLayout timestamps each run directory. Milliseconds keep two mentions
// handled in the same second from sharing one directory.
const runTimeLayout = "20060102-150405.000"

// runDir resolves the per-run directory for a repository under the resolved
// paseo project folder. Each run gets its own uniquely named directory
// (<root>/<owner>-<repo>-<timestamp>) so two runs never share one. It starts
// empty: the run's `setup` subagent clones the repository and creates the
// worktree the run then works inside.
func runDir(root, repository string, now time.Time) string {
	return filepath.Join(root, runDirName(repository, now))
}

// runDirName flattens owner/name into a filesystem-safe basename and appends the
// run timestamp. Flattening also removes any path separators, so a hostile
// repository value cannot escape the project folder.
func runDirName(repository string, now time.Time) string {
	var b strings.Builder
	for _, r := range repository {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}

	return strings.Trim(b.String(), "-") + "-" + now.UTC().Format(runTimeLayout)
}

// prepareRunDir creates the directory the paseo workspace points at. paseo
// records a local workspace for a path it does not create, and `paseo run`
// starts the agent with that path as its working directory, so the directory
// must exist first. No clone happens here.
func prepareRunDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create run directory %s: %w", dir, err)
	}

	return nil
}

// scrubbedEnv is the `env -i PATH=... HOME=...` prefix that strips secrets from
// every paseo subprocess. Commands are built without it and carry it only when
// they are executed, so the command that reaches the log is exactly what an
// operator would paste.
func scrubbedEnv() []string {
	return []string{"-i", "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
}

// logDispatch writes the one Debug entry for a dispatch: the directory, then one
// `command:` line per paseo command in the order they ran, quoted by
// shellCommand so the whole block pastes into a shell unchanged. A nil command
// is skipped, so a failed create logs just itself. reason explains why a command
// never produced its successor: it is only set by a failed workspace create,
// whose run command was never built.
func logDispatch(dir string, reason error, commands ...[]string) {
	var b strings.Builder
	b.WriteString("\ndirectory: ")
	b.WriteString(dir)
	for _, command := range commands {
		if command == nil {
			continue
		}
		b.WriteString("\ncommand: ")
		b.WriteString(shellCommand(command))
	}
	if reason != nil {
		b.WriteString("\nerror: ")
		b.WriteString(reason.Error())
	}

	facades.Log().Debug("Running paseo:", b.String())
}
