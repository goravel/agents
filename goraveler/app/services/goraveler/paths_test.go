package goraveler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
