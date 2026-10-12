package stagingcheck

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestStagingIsASiblingOfTheDestination is the assertion that makes the rename
// safe, and the reason this file must never be routed through datadir.
//
// os.MkdirTemp is called with the resolved destination parent. Same directory
// means same filesystem, so os.Rename cannot return EXDEV. If staging ever
// moved to the cache root, this test fails and names the consequence.
func TestStagingIsASiblingOfTheDestination(t *testing.T) {
	dest := t.TempDir()

	resolved, staging, err := createFolderDownloadStaging(dest)
	if err != nil {
		t.Fatalf("createFolderDownloadStaging: %v", err)
	}
	defer os.RemoveAll(staging)

	if got := filepath.Dir(staging); got != resolved {
		t.Fatalf("staging %s sits in %s, not in the destination parent %s.\n"+
			"A rename out of it can now cross a filesystem boundary and fail "+
			"with EXDEV on portable installations.", staging, got, resolved)
	}
}

// TestStagingIsPrivate pins the mode. The staging tree holds the user's file
// contents mid-download.
func TestStagingIsPrivate(t *testing.T) {
	_, staging, err := createFolderDownloadStaging(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(staging)

	info, err := os.Stat(staging)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("staging directory mode is %o, want 700", perm)
	}
	if !info.IsDir() {
		t.Error("staging path is not a directory")
	}
}

// TestStagingNameIsHidden covers the window during which the staging tree is
// visible inside the user's chosen folder. A dot prefix keeps a half-finished
// download out of the way on the platforms that honour it.
func TestStagingNameIsHidden(t *testing.T) {
	_, staging, err := createFolderDownloadStaging(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(staging)

	if base := filepath.Base(staging); base[0] != '.' {
		t.Errorf("staging directory %q is not hidden", base)
	}
}

// TestPublishMovesTheWholeTree is the happy path, run rather than assumed.
func TestPublishMovesTheWholeTree(t *testing.T) {
	dest := t.TempDir()
	_, staging, err := createFolderDownloadStaging(dest)
	if err != nil {
		t.Fatal(err)
	}

	nested := filepath.Join(staging, "sub", "deeper")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "file.bin"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	final := filepath.Join(dest, "My Folder")
	if err := publishFolderDownload(staging, final); err != nil {
		t.Fatalf("publishFolderDownload: %v", err)
	}

	if _, err := os.Lstat(staging); !os.IsNotExist(err) {
		t.Errorf("staging path still exists after publish: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(final, "sub", "deeper", "file.bin"))
	if err != nil {
		t.Fatalf("read published file: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("published content = %q, want %q", got, "payload")
	}
}

// TestPublishRefusesToClobber covers the race the function was written for: a
// folder created at the destination after the download started.
func TestPublishRefusesToClobber(t *testing.T) {
	dest := t.TempDir()
	_, staging, err := createFolderDownloadStaging(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(staging)

	final := filepath.Join(dest, "Occupied")
	if err := os.Mkdir(final, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(final, "theirs.txt"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = publishFolderDownload(staging, final)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("publish over an existing folder: err = %v, want os.ErrExist", err)
	}

	// The pre-existing content must be untouched. os.Rename onto a non-empty
	// directory would fail anyway on most platforms, but on some it merges or
	// replaces, and "most platforms" is not a guarantee.
	got, err := os.ReadFile(filepath.Join(final, "theirs.txt"))
	if err != nil || string(got) != "keep me" {
		t.Fatalf("pre-existing content was disturbed: %q, %v", got, err)
	}
	if _, err := os.Lstat(staging); err != nil {
		t.Errorf("staging was destroyed on a refused publish: %v", err)
	}
}

// TestPublishRefusesWhenDestinationIsAFile is the case the Lstat check exists
// for. A file at the destination is not os.IsNotExist, so it must be reported
// as a conflict rather than silently replaced.
func TestPublishRefusesWhenDestinationIsAFile(t *testing.T) {
	dest := t.TempDir()
	_, staging, err := createFolderDownloadStaging(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(staging)

	final := filepath.Join(dest, "name-taken")
	if err := os.WriteFile(final, []byte("a file, not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := publishFolderDownload(staging, final); !errors.Is(err, os.ErrExist) {
		t.Fatalf("publish onto a file: err = %v, want os.ErrExist", err)
	}
	got, err := os.ReadFile(final)
	if err != nil || string(got) != "a file, not a folder" {
		t.Fatalf("the existing file was disturbed: %q, %v", got, err)
	}
}

// TestPublishRefusesWhenDestinationIsADanglingSymlink is why the check is
// Lstat and not Stat.
//
// os.Stat follows symlinks, so a link pointing at a path that does not exist
// reports IsNotExist -- and the function would conclude the destination is
// free. The rename would then replace the link, and anything later written
// through that name would land somewhere the user never chose. Lstat examines
// the link itself.
func TestPublishRefusesWhenDestinationIsADanglingSymlink(t *testing.T) {
	dest := t.TempDir()
	_, staging, err := createFolderDownloadStaging(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(staging)

	final := filepath.Join(dest, "dangling")
	if err := os.Symlink(filepath.Join(dest, "nothing-here"), final); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Confirm the trap is real before asserting the function avoids it.
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("precondition: os.Stat on a dangling link gave %v, want IsNotExist", err)
	}

	if err := publishFolderDownload(staging, final); !errors.Is(err, os.ErrExist) {
		t.Fatalf("publish onto a dangling symlink: err = %v, want os.ErrExist.\n"+
			"Using os.Stat here instead of os.Lstat would make this succeed.", err)
	}
}
