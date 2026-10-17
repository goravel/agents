package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		Run("git", "clone", "--depth", "1", "https://github.com/goravel/goravel.git", testifymock.Anything).
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
		Run("git", "clone", "--depth", "1", "https://github.com/goravel/goravel.git", testifymock.Anything).
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
		Run("git", "clone", "--depth", "1", "https://github.com/goravel/goravel.git", testifymock.Anything).
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

func TestRepoDir(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	require.Equal(t, filepath.Join(home, "code", "goravel", "goravel"), repoDir("~/code", "goravel/goravel"))
	require.Equal(t, filepath.Join(home, "goravel", "goravel"), repoDir("~", "goravel/goravel"))
	require.Equal(t, "/srv/code/goravel/framework", repoDir("/srv/code", "goravel/framework"))
	require.Equal(t, "goravel/goravel", repoDir(".", "goravel/goravel"))
}

func TestRepoDirRejectsEscape(t *testing.T) {
	require.Equal(t, "/srv/code", repoDir("/srv/code", "../evil"))
	require.Equal(t, "/srv/code", repoDir("/srv/code", "../../tmp/evil"))
}

func TestRepoDirRejectsSymlinkEscape(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "ws")
	outside := t.TempDir()
	require.NoError(t, os.Mkdir(workspace, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, "escape")))

	// "escape" resolves via the symlink to a directory outside the workspace, so
	// the (symlink-resolved) workspace root is returned instead of the escaping
	// path.
	require.Equal(t, resolveSymlinks(workspace), repoDir(workspace, "escape"))
}

func TestRepoDirAllowsSymlinkedWorkspaceAncestor(t *testing.T) {
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "ws-link")
	require.NoError(t, os.Symlink(realRoot, link))

	// A workspace reached through a symlinked ancestor is resolved once, so a
	// legitimate repo inside it is accepted rather than judged as escaping.
	require.Equal(t, filepath.Join(resolveSymlinks(realRoot), "goravel", "goravel"), repoDir(link, "goravel/goravel"))
}

func TestRepoDirInResolvesUnresolvedSymlinkedRoot(t *testing.T) {
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "ws-link")
	require.NoError(t, os.Symlink(realRoot, link))

	// root is passed in its unresolved form (a symlinked ancestor that exists,
	// but the root path itself was captured before the workspace existed), while
	// the cloned repo directory now exists. repoDirIn must resolve the root on
	// the same basis as the child instead of judging it an escape.
	root := filepath.Join(link, "sub")
	require.NoError(t, os.MkdirAll(filepath.Join(realRoot, "sub", "goravel", "goravel"), 0o755))

	require.Equal(t, filepath.Join(resolveSymlinks(realRoot), "sub", "goravel", "goravel"), repoDirIn(root, "goravel/goravel"))
}
