package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goravel/app/facades"
)

const (
	// gitRemote is the clone URL prefix for GitHub repositories.
	gitRemote = "https://github.com/"
	// cloneDepth keeps only the tip commit; enough for review/plan/implement runs.
	cloneDepth = "1"
	// cloneTimeout caps a single clone.
	cloneTimeout = 5 * time.Minute
)

// Checkouter ensures a repository is checked out under the workspace.
type Checkouter interface {
	// Ensure clones the repository into dir when it does not already exist.
	Ensure(ctx context.Context, dir, repository string) error
}

// gitCheckouter clones GitHub repositories into the workspace on demand.
type gitCheckouter struct{}

// newGitCheckouter returns a git-backed checkouter.
func newGitCheckouter() *gitCheckouter {
	return &gitCheckouter{}
}

// Ensure clones repository into dir when dir does not exist yet.
func (c *gitCheckouter) Ensure(ctx context.Context, dir, repository string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", dir, err)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fmt.Errorf("create workspace %s: %w", filepath.Dir(dir), err)
	}

	// Clone into a uniquely named staging directory and rename it into place only
	// once git succeeds, so a clone killed partway through (for example by the
	// timeout) never leaves a directory that later polls would mistake for a
	// complete checkout. The random suffix keeps the staging path from colliding
	// with another repository's checkout.
	tmp, err := os.MkdirTemp(filepath.Dir(dir), filepath.Base(dir)+".partial-*")
	if err != nil {
		return fmt.Errorf("create clone staging dir for %s: %w", dir, err)
	}

	result := facades.Process().
		WithContext(ctx).
		Timeout(cloneTimeout).
		Run("git", "clone", "--depth", cloneDepth, gitRemote+repository+".git", tmp)
	if result.Failed() {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("clone %s (exit %d): %s", repository, result.ExitCode(), strings.TrimSpace(result.ErrorOutput()))
	}

	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("move clone into %s: %w", dir, err)
	}

	return nil
}

// resolveWorkspace resolves the workspace root once: expands a leading `~`,
// cleans the path and follows symlinks.
func resolveWorkspace(workspace string) string {
	return resolveSymlinks(filepath.Clean(expandHome(workspace)))
}

// repoDirIn resolves the checkout directory for a repository under the workspace
// root. The root is symlink-resolved internally, since it may still be in its
// unresolved form: a root captured before the first clone stays unresolved while
// its cloned child resolves, which would otherwise look like an escape. A
// repository path that resolves outside the root is rejected and the root is
// returned instead.
func repoDirIn(root, repository string) string {
	// Resolve the root on the same basis as the child below.
	root = resolveSymlinks(root)

	dir := filepath.Join(root, filepath.FromSlash(repository))
	if dir == root {
		return dir
	}

	rel, err := filepath.Rel(root, resolveSymlinks(dir))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return root
	}

	return dir
}

// repoDir resolves the workspace root and the repository in one call.
func repoDir(workspace, repository string) string {
	return repoDirIn(resolveWorkspace(workspace), repository)
}

// resolveSymlinks returns the fully resolved path, or the input when it cannot
// be resolved (for example when the path does not exist yet).
func resolveSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	return path
}

// expandHome expands a leading `~` or `~/` in path to the user's home dir.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}

	return filepath.Join(home, path[2:])
}
