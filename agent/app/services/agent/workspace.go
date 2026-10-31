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
	// gitWebURL is the HTTPS base for human-facing GitHub links (pull requests,
	// issues, comments). It is not used for cloning.
	gitWebURL = "https://github.com/"
	// gitCloneURL is the SSH remote prefix for clones; resolved by the runner's
	// existing SSH configuration.
	gitCloneURL = "git@github.com:"
	// cloneDepth keeps only the tip commit; enough for review, plan and ship runs.
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

// Ensure clones repository into dir when dir does not exist yet, claiming dir
// so a competing call for the same path fails instead of removing a live
// checkout.
func (c *gitCheckouter) Ensure(ctx context.Context, dir, repository string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", dir, err)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fmt.Errorf("create workspace %s: %w", filepath.Dir(dir), err)
	}

	if err := claimCheckout(dir); err != nil {
		return fmt.Errorf("claim checkout %s: %w", dir, err)
	}

	result := facades.Process().
		WithContext(ctx).
		Timeout(cloneTimeout).
		Run("git", "clone", "--depth", cloneDepth, gitCloneURL+repository+".git", dir)
	if result.Failed() {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("clone %s (exit %d): %s", repository, result.ExitCode(), strings.TrimSpace(result.ErrorOutput()))
	}

	return nil
}

// claimCheckout takes ownership of dir, failing when the path is already
// occupied so a competing call cannot adopt an existing checkout.
func claimCheckout(dir string) error {
	return os.Mkdir(dir, 0o700)
}

// resolveWorkspace resolves the workspace root once: expands a leading `~`,
// cleans the path and follows symlinks.
func resolveWorkspace(workspace string) string {
	return resolveSymlinks(filepath.Clean(expandHome(workspace)))
}

// checkoutTimeLayout timestamps each run's checkout directory.
const checkoutTimeLayout = "20060102-150405"

// runDir resolves the per-run checkout directory for a repository under the
// workspace root. Each run gets its own uniquely named directory
// (<workspace>/<owner>-<repo>-<timestamp>) so a checkout left by a previous run
// is never reused or collided with.
func runDir(workspace, repository string, now time.Time) string {
	return filepath.Join(resolveWorkspace(workspace), checkoutName(repository, now))
}

// checkoutName flattens owner/name into a filesystem-safe basename and appends
// the run timestamp. Flattening also removes any path separators, so a hostile
// repository value cannot escape the workspace.
func checkoutName(repository string, now time.Time) string {
	var b strings.Builder
	for _, r := range repository {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}

	return strings.Trim(b.String(), "-") + "-" + now.UTC().Format(checkoutTimeLayout)
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
