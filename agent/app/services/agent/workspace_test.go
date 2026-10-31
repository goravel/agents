package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunDir(t *testing.T) {
	t0 := time.Date(2026, time.October, 18, 14, 32, 5, 0, time.UTC)
	ts := t0.UTC().Format(checkoutTimeLayout)

	require.Equal(t, filepath.Join("/tmp", "goravel-goravel-"+ts), runDir("/tmp", "goravel/goravel", t0))
	require.Equal(t, "/srv/code/goravel-framework-"+ts, runDir("/srv/code", "goravel/framework", t0))
}

func TestPrepareRunDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "goravel-goravel-20261018-143205.000")

	require.NoError(t, prepareRunDir(dir))
	require.NoError(t, prepareRunDir(dir), "an existing run directory is reused as-is")
	require.DirExists(t, dir)

	// A regular file in the way is an error, not a silent skip.
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o644))
	require.ErrorContains(t, prepareRunDir(filepath.Join(blocker, "run")), "create run directory")
}

func TestCheckoutNameFlattensRepository(t *testing.T) {
	t0 := time.Date(2026, time.October, 18, 14, 32, 5, 0, time.UTC)
	ts := t0.UTC().Format(checkoutTimeLayout)

	require.Equal(t, "goravel-goravel-"+ts, checkoutName("goravel/goravel", t0))

	// Flattening removes the separator, so a hostile repository value cannot
	// escape the project folder.
	flattened := checkoutName("../evil", t0)
	require.Equal(t, "..-evil-"+ts, flattened)
	require.NotContains(t, flattened, "/")
	require.NotContains(t, flattened, string(filepath.Separator))
}
