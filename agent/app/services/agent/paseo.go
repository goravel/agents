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

// projectLookupTimeout bounds the one `paseo project ls` a process pays for.
const projectLookupTimeout = 30 * time.Second

// Project is the paseo project every run workspace is created in: the id
// `paseo workspace create --project` needs, and the folder its run directories
// live in.
type Project struct {
	ID   string
	Path string
}

// projectError marks a paseo project failure. The poll cycle stops on it without
// consuming the event's retry budget, because replaying the event cannot fix a
// configuration problem.
type projectError struct{ err error }

func (e projectError) Error() string { return e.err.Error() }

func (e projectError) Unwrap() error { return e.err }

// projectResolver resolves AGENT_PROJECT_NAME once per process. The first
// mention that needs a workspace pays one `paseo project ls`; later mentions
// reuse the answer. A failure is not cached, so the next mention retries.
type projectResolver struct {
	bin  string
	name string

	mu       sync.Mutex
	resolved bool
	project  Project
}

func newProjectResolver(bin, name string) *projectResolver {
	return &projectResolver{bin: bin, name: name}
}

// sharedProjectResolvers holds the process's resolvers. `schedule:run` runs
// poll-events in-process on every tick, so a resolver owned by the tick's service
// would pay the lookup again on each one.
var (
	sharedProjectResolversMu sync.Mutex
	sharedProjectResolvers   = map[string]*projectResolver{}
)

// sharedProjectResolver returns the one resolver for bin and name in this
// process, so every in-process tick reuses the same cache.
func sharedProjectResolver(bin, name string) *projectResolver {
	sharedProjectResolversMu.Lock()
	defer sharedProjectResolversMu.Unlock()

	key := bin + "\x00" + name
	if resolver, ok := sharedProjectResolvers[key]; ok {
		return resolver
	}

	resolver := newProjectResolver(bin, name)
	sharedProjectResolvers[key] = resolver

	return resolver
}

// Project returns the resolved project, or a projectError naming
// AGENT_PROJECT_NAME and what was tried.
func (r *projectResolver) Project(ctx context.Context) (Project, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.resolved {
		return r.project, nil
	}

	project, err := resolveProject(ctx, r.bin, r.name)
	if err != nil {
		return Project{}, projectError{err: err}
	}

	r.project, r.resolved = project, true

	return project, nil
}

// resolveProject resolves the paseo project named by AGENT_PROJECT_NAME with
// `paseo project ls --json` ([{projectId,name,kind,path}]). `-q` is not used: it
// prints ids only and cannot answer a name lookup. It never falls back — a blank
// or unknown name, a failing paseo, unreadable output, or a project without a
// path is an error.
func resolveProject(ctx context.Context, bin, name string) (Project, error) {
	if name == "" {
		return Project{}, errors.New("agent: AGENT_PROJECT_NAME must name a paseo project")
	}

	// Buffering must stay on: the id and path are read from stdout.
	result := facades.Process().
		WithContext(ctx).
		Timeout(projectLookupTimeout).
		Quietly().
		Run("env", append(scrubbedEnv(), bin, "project", "ls", "--json")...)

	if result.Failed() {
		return Project{}, fmt.Errorf(
			"agent: AGENT_PROJECT_NAME=%q: `paseo project ls --json` failed (exit %d): %s",
			name, result.ExitCode(), strings.Join(strings.Fields(result.ErrorOutput()), " "))
	}

	var projects []struct {
		ProjectID string `json:"projectId"`
		Name      string `json:"name"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal([]byte(result.Output()), &projects); err != nil {
		return Project{}, fmt.Errorf("agent: AGENT_PROJECT_NAME=%q: decode `paseo project ls --json`: %w", name, err)
	}

	for _, project := range projects {
		if project.Name != name {
			continue
		}
		if project.ProjectID == "" || project.Path == "" {
			return Project{}, fmt.Errorf("agent: AGENT_PROJECT_NAME=%q: that paseo project has no id or path", name)
		}

		return Project{ID: project.ProjectID, Path: project.Path}, nil
	}

	return Project{}, fmt.Errorf("agent: AGENT_PROJECT_NAME=%q: no paseo project with that name", name)
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

// runDirName flattens owner/name into a filesystem-safe basename and appends
// the run timestamp. Flattening also removes any path separators, so a hostile
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
