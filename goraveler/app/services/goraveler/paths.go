package goraveler

import (
	"os"
	"path/filepath"
	"strings"
)

// paths helpers

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
