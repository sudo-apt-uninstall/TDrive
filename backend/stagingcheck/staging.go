// Package stagingcheck executes backend/services/file/download_staging.go.
//
// That file is stdlib-only, so unlike the rest of its package it can be run
// here. It is worth running because it is the ONE place on this branch where a
// scratch directory is renamed to a user-visible destination, and because the
// chokepoint guard deliberately stays silent on it.
//
// The reason the guard stays silent is the thing this package pins:
// os.MkdirTemp is called with the RESOLVED DESTINATION PARENT, not with "".
// Source and destination are therefore siblings, on the same filesystem, and
// the rename cannot fail with EXDEV.
//
// Routing this through datadir.CreateCacheTempDir -- which is what a careless
// reading of "close the bypasses" would do -- would move the staging tree to
// the cache root and INTRODUCE a cross-device rename on exactly the portable
// installations this branch exists to support.
package stagingcheck

import (
	"fmt"
	"os"
	"path/filepath"
)

// createFolderDownloadStaging is copied verbatim from
// backend/services/file/download_staging.go, blob
// 06f5c9ceff2bdffe9b243c44b31bacd4d0d90602.
func createFolderDownloadStaging(parentPath string) (resolvedParent, stagingPath string, err error) {
	resolvedParent, err = filepath.Abs(parentPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve download destination: %w", err)
	}
	resolvedParent, err = filepath.EvalSymlinks(resolvedParent)
	if err != nil {
		return "", "", fmt.Errorf("resolve download destination: %w", err)
	}
	stagingPath, err = os.MkdirTemp(resolvedParent, ".tdrive-folder-download-*")
	if err != nil {
		return "", "", err
	}
	return resolvedParent, stagingPath, nil
}

// publishFolderDownload is copied verbatim from the same file.
func publishFolderDownload(stagingPath, finalPath string) error {
	if _, err := os.Lstat(finalPath); err == nil {
		return &os.LinkError{Op: "rename", Old: stagingPath, New: finalPath, Err: os.ErrExist}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(stagingPath, finalPath)
}
