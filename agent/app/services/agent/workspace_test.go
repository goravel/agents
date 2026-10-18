package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	var staging string

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Timeout(cloneTimeout).Return(processMock).Once()
	processMock.EXPECT().
		Run("git", "clone", "--depth", "1", "git@github.com:goravel/goravel.git", testifymock.Anything).
		Run(func(_ string, args ...string) {
			staging = args[len(args)-1]
			// git clones into the staging directory Ensure created.
			require.NoError(t, os.WriteFile(filepath.Join(staging, "HEAD"), nil, 0o600))
		}).
		Return(result).
		Once()
	result.EXPECT().Failed().Return(false).Once()

	require.NoError(t, newGitCheckouter().Ensure(context.Background(), dir, "goravel/goravel"))

	// A uniquely named staging dir is renamed into place on success.
	require.True(t, strings.HasPrefix(filepath.Base(staging), "goravel.partial-"))
	require.FileExists(t, filepath.Join(dir, "HEAD"))
	_, err := os.Stat(staging)
	require.ErrorIs(t, err, os.ErrNotExist)

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestGitCheckouterEnsureCloneFailure(t *testing.T) {
	processMock := mock.Factory().Process()
	result := mocksprocess.NewResult(t)

	dir := filepath.Join(t.TempDir(), "goravel")
	var staging string

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Timeout(cloneTimeout).Return(processMock).Once()
	processMock.EXPECT().
		Run("git", "clone", "--depth", "1", "git@github.com:goravel/goravel.git", testifymock.Anything).
		Run(func(_ string, args ...string) {
			staging = args[len(args)-1]
			// A failed clone can leave the staging directory behind.
			require.NoError(t, os.MkdirAll(staging, 0o700))
		}).
		Return(result).
		Once()
	result.EXPECT().Failed().Return(true).Once()
	result.EXPECT().ExitCode().Return(128).Once()
	result.EXPECT().ErrorOutput().Return("boom").Once()

	err := newGitCheckouter().Ensure(context.Background(), dir, "goravel/goravel")
	require.Error(t, err)
	require.Contains(t, err.Error(), "goravel/goravel")
	require.Contains(t, err.Error(), "exit 128")
	require.Contains(t, err.Error(), "boom")

	// The staging directory is cleaned up on failure.
	_, statErr := os.Stat(staging)
	require.ErrorIs(t, statErr, os.ErrNotExist)

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestGitCheckouterEnsureRenameFailure(t *testing.T) {
	processMock := mock.Factory().Process()
	result := mocksprocess.NewResult(t)

	dir := filepath.Join(t.TempDir(), "goravel")

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Timeout(cloneTimeout).Return(processMock).Once()
	processMock.EXPECT().
		Run("git", "clone", "--depth", "1", "git@github.com:goravel/goravel.git", testifymock.Anything).
		Run(func(_ string, args ...string) {
			// git reports success but leaves no staging directory, so the rename
			// into place fails.
			require.NoError(t, os.RemoveAll(args[len(args)-1]))
		}).
		Return(result).
		Once()
	result.EXPECT().Failed().Return(false).Once()

	err := newGitCheckouter().Ensure(context.Background(), dir, "goravel/goravel")
	require.ErrorContains(t, err, "move clone into")

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
