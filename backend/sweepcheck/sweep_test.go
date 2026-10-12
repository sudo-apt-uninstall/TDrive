package sweepcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"TDrive/backend/datadir"
)

// sweepStaleVideoThumbnailDirsOld is the body BEFORE the edit in 404a8ffb.
//
// It lives in this file rather than sweep.go for a reason worth keeping. It
// calls os.TempDir(), which is exactly what the chokepoint guard in
// backend/datadir bans -- and that guard walks every file that is NOT a
// _test.go file. Putting a deliberately-wrong fixture in a production-path
// file would have failed TestNoProductionCodeBypassesDatadir the first time it
// ran over the whole tree.
//
// Round 31 caught this: "backend/sweepcheck/sweep.go:54,67: old-sweep fixture
// uses os.TempDir() in a non-_test.go source file". It was reported as a
// likely false positive. It was not a false positive. The guard was right and
// the fixture was in the wrong file.
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

// age backdates a directory past the sweep cutoff.
func age(t *testing.T, dir string, d time.Duration) {
	t.Helper()
	when := time.Now().Add(-d)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatalf("backdate %s: %v", dir, err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// newCacheRoot points datadir at a private cache root for one test.
func newCacheRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	datadir.SetCache(root)
	t.Cleanup(func() { datadir.SetCache("") })
	// CacheDir appends the per-app subdirectory and creates it.
	dir, err := datadir.CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	return dir
}

// TestSweepRemovesAgedDirsInTheCacheRoot is the assertion the edit exists for.
func TestSweepRemovesAgedDirsInTheCacheRoot(t *testing.T) {
	cache := newCacheRoot(t)

	stale, err := datadir.CreateCacheTempDir(videoThumbTempPrefix)
	if err != nil {
		t.Fatalf("CreateCacheTempDir: %v", err)
	}
	// A scratch directory is never empty in practice; prove RemoveAll recurses.
	if err := os.WriteFile(filepath.Join(stale, "frame.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(stale, "sub")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	age(t, nested, 48*time.Hour)
	age(t, stale, 48*time.Hour)

	fresh, err := datadir.CreateCacheTempDir(videoThumbTempPrefix)
	if err != nil {
		t.Fatal(err)
	}

	// A directory belonging to somebody else, aged past the cutoff. It must
	// survive: the prefix is the only thing separating this sweep from
	// deleting another subsystem's scratch space.
	foreign := filepath.Join(cache, "tdrive-upload-not-ours")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	age(t, foreign, 48*time.Hour)

	// A FILE whose name carries the prefix. CacheTempDirs filters to
	// directories, so the dropped IsDir() check must not matter.
	strayFile := filepath.Join(cache, videoThumbTempPrefix+"stray")
	if err := os.WriteFile(strayFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	age(t, strayFile, 48*time.Hour)

	sweepStaleVideoThumbnailDirs()

	if exists(stale) {
		t.Errorf("aged scratch directory %s survived the sweep", stale)
	}
	if !exists(fresh) {
		t.Errorf("fresh scratch directory %s was deleted", fresh)
	}
	if !exists(foreign) {
		t.Errorf("another subsystem's directory %s was deleted", foreign)
	}
	if !exists(strayFile) {
		t.Errorf("a FILE matching the prefix was deleted; the dropped IsDir() check mattered after all")
	}
}

// TestTheOldSweepFindsNothingInTheCacheRoot is the inverse, and the reason the
// edit was necessary rather than cosmetic.
//
// The old body reads os.TempDir(). Once creation moved to the cache root there
// is nothing there to find, so it returns having done nothing -- no error, no
// log line, no symptom. The directories accumulate until the disk fills.
func TestTheOldSweepFindsNothingInTheCacheRoot(t *testing.T) {
	newCacheRoot(t)

	stale, err := datadir.CreateCacheTempDir(videoThumbTempPrefix)
	if err != nil {
		t.Fatal(err)
	}
	age(t, stale, 48*time.Hour)

	sweepStaleVideoThumbnailDirsOld()

	if !exists(stale) {
		t.Fatalf("the old sweep deleted %s; it should not be able to see the cache root at all", stale)
	}
	t.Logf("old sweep left %s in place: silent leak confirmed", stale)

	// And the new one collects it, over the identical tree.
	sweepStaleVideoThumbnailDirs()
	if exists(stale) {
		t.Fatalf("the new sweep also failed to remove %s", stale)
	}
	t.Log("new sweep removed the same directory: the edit is load-bearing")
}

// TestSweepSurvivesAMissingCacheRoot covers first launch. A startup sweep must
// never be the thing that fails a launch, and CacheTempDirs promises that a
// missing directory is not an error.
func TestSweepSurvivesAMissingCacheRoot(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "does", "not", "exist")
	datadir.SetCache(missing)
	t.Cleanup(func() { datadir.SetCache("") })

	sweepStaleVideoThumbnailDirs() // must not panic
	t.Log("sweep over an absent cache root returned cleanly")
}

// TestCacheTempDirsExcludesFilesAndSymlinks pins the contract the edit now
// depends on. The IsDir() check was removed from the caller, so if this ever
// stops holding, the sweep silently becomes able to delete files.
func TestCacheTempDirsExcludesFilesAndSymlinks(t *testing.T) {
	cache := newCacheRoot(t)

	realDir := filepath.Join(cache, "a-directory")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plainFile := filepath.Join(cache, "a-file")
	if err := os.WriteFile(plainFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink pointing at a real directory elsewhere. DirEntry.IsDir reports
	// on the link itself, so this must be excluded -- otherwise a sweep could
	// follow it out of the cache root entirely.
	outside := t.TempDir()
	link := filepath.Join(cache, "a-symlink-to-a-dir")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := datadir.CacheTempDirs()
	if err != nil {
		t.Fatalf("CacheTempDirs: %v", err)
	}
	found := map[string]bool{}
	for _, entry := range got {
		found[filepath.Base(entry)] = true
	}
	if !found["a-directory"] {
		t.Error("a real directory was not listed")
	}
	if found["a-file"] {
		t.Error("a plain file was listed; the sweep would delete files")
	}
	if found["a-symlink-to-a-dir"] {
		t.Error("a symlink was listed; a sweep could follow it out of the cache root")
	}
	if !exists(outside) {
		t.Error("the symlink target was removed")
	}
}
