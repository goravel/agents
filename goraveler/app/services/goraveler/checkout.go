package goraveler

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
