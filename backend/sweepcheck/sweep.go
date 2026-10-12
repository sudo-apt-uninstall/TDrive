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

// sweepStaleVideoThumbnailDirsOld is the body BEFORE the edit.
func sweepStaleVideoThumbnailDirsOld() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-videoThumbTempMaxAge)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), videoThumbTempPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(os.TempDir(), entry.Name()))
	}
}
