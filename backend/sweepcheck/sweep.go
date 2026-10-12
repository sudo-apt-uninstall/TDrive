// Package sweepcheck runs the post-edit sweepStaleVideoThumbnailDirs against a
// real cache root.
//
// The edit landed in 404a8ffb without being compiled or run -- the environment
// that made it could not fetch the repository. The function is the one place
// on this branch where a wrong edit is SILENT: a sweep that looks in the wrong
// directory finds nothing, returns no error, and leaks scratch directories
// forever. Inspection cannot distinguish that from success, so it is executed
// here instead.
//
// This package is test scaffolding, not production code. It holds copies of
// the function before and after the edit so the two can be run over the same
// tree, because a fix is only demonstrated by showing the thing it replaced
// failing on the same input.
//
// The PRE-edit copy lives in sweep_test.go, not here. It necessarily calls
// os.TempDir(), and the chokepoint guard in backend/datadir walks every file
// that is not a _test.go file. Round 31 caught this: the fixture was
// originally in this file and would have failed
// TestNoProductionCodeBypassesDatadir the first time that guard ran over the
// whole tree. A test fixture that trips the project's own guard is not a
// fixture, it is a bypass with an explanation attached.
package sweepcheck

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"TDrive/backend/datadir"
)

// videoThumbTempPrefix and videoThumbTempMaxAge mirror backend/media.
const (
	videoThumbTempPrefix = "tdrive-videothumb-"
	videoThumbTempMaxAge = 24 * time.Hour
)

// sweepStaleVideoThumbnailDirs is the body committed in 404a8ffb, copied
// verbatim apart from the package-local constants.
func sweepStaleVideoThumbnailDirs() {
	entries, err := datadir.CacheTempDirs()
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-videoThumbTempMaxAge)
	for _, entry := range entries {
		if !strings.HasPrefix(filepath.Base(entry), videoThumbTempPrefix) {
			continue
		}
		info, err := os.Stat(entry)
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(entry)
	}
}
