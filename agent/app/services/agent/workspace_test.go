package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	testifymock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	mocksprocess "github.com/goravel/framework/mocks/process"
	"github.com/goravel/framework/testing/mock"
)

func TestGitCheckouterEnsureSkipsExistingDir(t *testing.T) {
	factory := mock.Factory()

	dir := filepath.Join(t.TempDir(), "goravel")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	require.NoError(t, newGitCheckouter().Ensure(context.Background(), dir, "goravel/goravel"))

	// An existing checkout must not shell out to git.
	factory.App().AssertNotCalled(t, "MakeProcess")
}

func TestGitCheckouterEnsureClonesMissingDir(t *testing.T) {
	processMock := mock.Factory().Process()
	result := mocksprocess.NewResult(t)

	dir := filepath.Join(t.TempDir(), "goravel")
	var target string

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Timeout(cloneTimeout).Return(processMock).Once()
	processMock.EXPECT().
		Run("git", "clone", "--depth", "1", "git@github.com:goravel/goravel.git", testifymock.Anything).
		Run(func(_ string, args ...string) {
			target = args[len(args)-1]
			// The claim created the run directory before git ran.
			require.DirExists(t, target)
			require.NoError(t, os.WriteFile(filepath.Join(target, "HEAD"), nil, 0o600))
		}).
		Return(result).
		Once()
	result.EXPECT().Failed().Return(false).Once()

	require.NoError(t, newGitCheckouter().Ensure(context.Background(), dir, "goravel/goravel"))

	// git clones straight into the per-run directory; there is no staging dir.
	require.Equal(t, dir, target)
	require.FileExists(t, filepath.Join(dir, "HEAD"))

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestGitCheckouterEnsureCloneFailure(t *testing.T) {
	processMock := mock.Factory().Process()
	result := mocksprocess.NewResult(t)

	dir := filepath.Join(t.TempDir(), "goravel")
	var target string

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Timeout(cloneTimeout).Return(processMock).Once()
	processMock.EXPECT().
		Run("git", "clone", "--depth", "1", "git@github.com:goravel/goravel.git", testifymock.Anything).
		Run(func(_ string, args ...string) {
			target = args[len(args)-1]
			// The claim created the target, which a failed clone may leave behind.
			require.DirExists(t, target)
		}).
		Return(result).
		Once()
	result.EXPECT().Failed().Return(true).Once()
	result.EXPECT().ExitCode().Return(128).Once()
	result.EXPECT().ErrorOutput().Return("boom").Once()

	err := newGitCheckouter().Ensure(context.Background(), dir, "goravel/goravel")
	require.Error(t, err)
	require.Equal(t, dir, target)
	require.Contains(t, err.Error(), "goravel/goravel")
	require.Contains(t, err.Error(), "exit 128")
	require.Contains(t, err.Error(), "boom")

	// The failed clone target is cleaned up on failure.
	_, statErr := os.Stat(target)
	require.ErrorIs(t, statErr, os.ErrNotExist)

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestGitCheckouterEnsureStatError(t *testing.T) {
	factory := mock.Factory()

	// dir sits under a regular file, so os.Stat fails with ENOTDIR rather than
	// os.ErrNotExist.
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o644))

	err := newGitCheckouter().Ensure(context.Background(), filepath.Join(blocker, "goravel"), "goravel/goravel")
	require.ErrorContains(t, err, "stat")

	// The stat short-circuit must not shell out to git.
	factory.App().AssertNotCalled(t, "MakeProcess")
}

func TestGitCheckouterEnsureClaimFailure(t *testing.T) {
	factory := mock.Factory()

	// A dangling symlink makes os.Stat report os.ErrNotExist while the path is
	// still occupied, so the os.Mkdir claim fails with EEXIST.
	dir := filepath.Join(t.TempDir(), "goravel")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "missing"), dir))

	err := newGitCheckouter().Ensure(context.Background(), dir, "goravel/goravel")
	require.ErrorContains(t, err, "claim checkout")

	// The claim must fail before shelling out to git.
	factory.App().AssertNotCalled(t, "MakeProcess")
}

func TestClaimCheckoutRejectsExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "goravel")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	// os.MkdirAll would return nil for an existing directory; the ownership
	// claim must not adopt a path this call did not create.
	require.ErrorIs(t, claimCheckout(dir), os.ErrExist)
}

func TestRunDir(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	t0 := time.Date(2026, time.October, 18, 14, 32, 5, 0, time.UTC)
	ts := t0.UTC().Format(checkoutTimeLayout)

	require.Equal(t, filepath.Join(home, "code", "goravel-goravel-"+ts), runDir("~/code", "goravel/goravel", t0))
	require.Equal(t, "/srv/code/goravel-framework-"+ts, runDir("/srv/code", "goravel/framework", t0))
}

func TestCheckoutNameFlattensRepository(t *testing.T) {
	t0 := time.Date(2026, time.October, 18, 14, 32, 5, 0, time.UTC)
	ts := t0.UTC().Format(checkoutTimeLayout)

	require.Equal(t, "goravel-goravel-"+ts, checkoutName("goravel/goravel", t0))

	// Flattening removes the separator, so a hostile repository value cannot
	// escape the workspace.
	flattened := checkoutName("../evil", t0)
	require.Equal(t, "..-evil-"+ts, flattened)
	require.NotContains(t, flattened, "/")
	require.NotContains(t, flattened, string(filepath.Separator))
}
