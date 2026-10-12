// Package datadir centralizes where TDrive keeps its own files. On desktop,
// daemon and CLI this stays os.UserConfigDir()/TDrive for data and
// os.UserCacheDir()/TDrive for cache, exactly as before. On mobile those
// defaults can be wrong -- Go's home dir is /sdcard on Android when HOME is
// unset, so the OS-convention dirs land on shared external storage -- and the
// host overrides the bases with the app-private sandbox at startup.
package datadir

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// appDir is the per-app subdirectory every TDrive file lives under, kept the
// same across platforms so the on-disk layout matches everywhere.
const appDir = "TDrive"

// dirMode is the private mode TDrive's data directories are created with,
// matching every caller's historical 0700.
const dirMode os.FileMode = 0o700

var cacheTempPrefixes = [...]string{
	"tdrive-rendition-source-",
	"tdrive-upload-",
	".tdrive-upload-part-",
	"tdrive-enc-",
	"tdrive-mountdav-put-",
}

var (
	mu            sync.RWMutex
	dataOverride  string
	cacheOverride string
)

// Set overrides the base Dir builds on, replacing the os.UserConfigDir default.
// Mobile calls it once at startup, before any path is read, with the
// app-private storage path. An empty dir clears the override.
func Set(dir string) {
	mu.Lock()
	dataOverride = dir
	mu.Unlock()
}

// SetCache overrides the base CacheDir builds on, replacing the
// os.UserCacheDir default. Android needs it (no usable home dir); iOS keeps the
// default, which already resolves to the app's Library/Caches and stays out of
// backups. An empty dir clears the override.
func SetCache(dir string) {
	mu.Lock()
	cacheOverride = dir
	mu.Unlock()
}

// Dir returns TDrive's data/config directory, creating it 0700. Defaults to
// os.UserConfigDir()/TDrive; Set replaces the base.
func Dir() (string, error) {
	mu.RLock()
	root := dataOverride
	mu.RUnlock()
	return resolve(root, os.UserConfigDir)
}

// CacheDir returns TDrive's cache directory, creating it 0700. Defaults to
// os.UserCacheDir()/TDrive; SetCache replaces the base.
func CacheDir() (string, error) {
	mu.RLock()
	root := cacheOverride
	mu.RUnlock()
	return resolve(root, os.UserCacheDir)
}

// CreateCacheTemp creates a private, app-owned temporary file. It is the
// single temporary-file boundary for data that may contain file bytes or
// decrypted metadata, so mobile builds never fall back to shared storage.
func CreateCacheTemp(pattern string) (*os.File, error) {
	dir, err := CacheDir()
	if err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, pattern)
}

// CreateCacheTempDir creates a private, app-owned scratch directory and
// returns its path. It is the directory counterpart to CreateCacheTemp.
//
// It exists because its absence was the bug. Five of the bypasses found on
// this branch -- thumbnail extraction, media preview sessions, the WebDAV
// resume buffer, archive import, the CLI's cat -- all needed a scratch
// *directory*, and the chokepoint only offered a scratch *file*. Faced with no
// correct call to make, every one of them reached for os.MkdirTemp(""), which
// lands in the system temp directory and ignores both portable mode and the
// mobile overrides. That is one missing function, not five separate lapses,
// and it is worth naming as such: a chokepoint with a hole in it routes
// traffic around itself.
//
// Callers own what they create here. CleanupCacheTemps will not reap these --
// see the note on that function -- because the sweeps that make these
// directories already know their own retention, and a scratch directory may
// still be in use by another copy of the app.
func CreateCacheTempDir(pattern string) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, pattern)
}

// CacheTempDirs lists the immediate subdirectories of the cache root.
//
// This is for the age-based sweeps that reap their own scratch directories on
// startup. They currently enumerate os.TempDir(), which is a bypass in its own
// right and, more quietly, the wrong place once CreateCacheTempDir is in use:
// a sweep looking in the system temp directory for directories that are now in
// the cache root finds nothing, reports success, and leaves them to accumulate
// forever.
//
// A missing cache directory is not an error. There is nothing to sweep before
// anything has been written, and a startup sweep should not be the thing that
// fails a launch.
func CacheTempDirs() ([]string, error) {
	dir, err := CacheDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("datadir: read cache directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, filepath.Join(dir, entry.Name()))
		}
	}
	return names, nil
}

// CleanupCacheTemps removes app-owned scratch files left by an interrupted
// upload, encryption, rendition, or mount write. It only examines the cache
// root and only removes names produced by CreateCacheTemp callers.
//
// Directories are skipped, and deliberately so. CreateCacheTempDir callers own
// their own lifetime: they sweep by age because a scratch directory can belong
// to a second copy of the app that is still using it, and deleting one out
// from under a running extractor would be worse than leaving it. Widening this
// function to remove directories would make the cheap, safe file sweep into a
// destructive one.
//
// A file it cannot delete is left where it is. Windows refuses to unlink a
// file another process still has open, and on that platform a second copy of
// the app -- or a test running beside this one -- is enough to make one of
// these undeletable. Tidying up is not a reason to refuse to start: the file
// is scratch, the next launch will try again, and the caller that owns it is
// the one that will finish with it.
func CleanupCacheTemps() error {
	dir, err := CacheDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("datadir: read cache temp directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !isOwnedCacheTemp(entry.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			slog.Debug("datadir: cache temp left in place", "name", entry.Name(), "error", err)
		}
	}
	return nil
}

func isOwnedCacheTemp(name string) bool {
	for _, prefix := range cacheTempPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// resolve derives the default base at call time, not at init, so tests that
// redirect HOME/XDG keep working through the defaults.
func resolve(root string, base func() (string, error)) (string, error) {
	if root == "" {
		var err error
		if root, err = base(); err != nil {
			return "", err
		}
	}

	dir := filepath.Join(root, appDir)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, dirMode)
	return dir, nil
}
