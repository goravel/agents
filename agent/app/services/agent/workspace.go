package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// checkoutTimeLayout timestamps each run directory. Milliseconds keep two
// mentions handled in the same second from sharing one directory.
const checkoutTimeLayout = "20060102-150405.000"

// runDir resolves the per-run directory for a repository under the resolved
// paseo project folder. Each run gets its own uniquely named directory
// (<root>/<owner>-<repo>-<timestamp>) so two runs never share one; paseo's setup
// and the primary agent check the repository out inside it.
func runDir(root, repository string, now time.Time) string {
	return filepath.Join(root, checkoutName(repository, now))
}

// checkoutName flattens owner/name into a filesystem-safe basename and appends
// the run timestamp. Flattening also removes any path separators, so a hostile
// repository value cannot escape the project folder.
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
