// Portable mode: keep every TDrive file beside the executable.
//
// The rest of this package already resolves TDrive's paths through one
// pair of overridable bases, and mobile already replaces them at startup
// because Android's home directory is shared external storage. Portable
// desktop mode is that same mechanism pointed at the directory holding
// the running executable, so a copy of the folder is a copy of the
// installation.
//
// It is switched on by a marker file beside the executable, or by the
// TDRIVE_PORTABLE_DIR environment variable. A marker file rather than a
// build flag means one binary serves both modes and the user decides by
// dropping a file next to it.
//
// # Refusing is better than relocating
//
// The important behaviour here is the failure path. If portable mode is
// requested and cannot be delivered -- the executable sits in Program
// Files, the stick is read-only, the volume is full -- this returns an
// error and TDrive must stop. It never falls back to the user-config
// directory.
//
// That is deliberate. A portable application that quietly writes to the
// system drive is worse than one that refuses to start, because the user
// believes their data travelled with them and it did not. They find out
// when they plug the stick into another machine and their drive is empty.
package datadir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MarkerName is the file whose presence beside the executable turns
// portable mode on. Its contents are ignored, so an empty file works.
const MarkerName = "tdrive-portable.txt"

// EnvDir overrides everything, including the marker, and names the
// directory to use. Mainly for tests and for putting the data somewhere
// other than the executable's own folder.
const EnvDir = "TDRIVE_PORTABLE_DIR"

// ErrPortableUnwritable means portable mode was requested and the target
// cannot hold TDrive's files. Callers must treat it as fatal rather than
// continuing with the default location.
var ErrPortableUnwritable = errors.New("datadir: portable directory is not writable")

var (
	portableMu  sync.RWMutex
	portableDir string
)

// ExecutableDir is the directory holding the running executable, with
// symlinks resolved so a launcher link does not scatter data next to the
// link instead of next to the program.
func ExecutableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("datadir: locate executable: %w", err)
	}
	// A path that cannot be resolved is not a reason to refuse to start;
	// the unresolved path is still the best answer available.
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// MarkerPath is where EnablePortable looks for the marker file.
func MarkerPath() (string, error) {
	dir, err := ExecutableDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, MarkerName), nil
}

// EnablePortable configures this package for portable mode when it has
// been asked for, and reports whether it did.
//
// Call it once at startup, before any path is read -- the same contract
// mobile already follows for Set and SetCache.
//
// The three outcomes are distinct on purpose:
//
//	dir, true, nil    portable mode is on and usable
//	"", false, nil    not a portable installation; defaults apply
//	"", true, err     portable mode was asked for and CANNOT be
//	                  delivered. Fatal. Do not continue.
//
// The third case is the one that matters. Collapsing it into the second
// would mean a portable installation silently writing to the system
// drive, which is the failure this whole mechanism exists to prevent.
func EnablePortable() (string, bool, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvDir)); raw != "" {
		dir, err := filepath.Abs(raw)
		if err != nil {
			return "", true, fmt.Errorf("datadir: %s is not a usable path: %w", EnvDir, err)
		}
		if err := adopt(dir); err != nil {
			return "", true, err
		}
		return dir, true, nil
	}

	marker, err := MarkerPath()
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(marker); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		// The marker exists but cannot be read. Refusing is right: this
		// is ambiguous, and guessing "not portable" would relocate the
		// user's data to the system drive on a permissions hiccup.
		return "", true, fmt.Errorf("datadir: cannot read portable marker %s: %w", marker, err)
	}

	dir := filepath.Dir(marker)
	if err := adopt(dir); err != nil {
		return "", true, err
	}
	return dir, true, nil
}

// adopt points both bases at dir after proving it can actually be
// written to.
//
// Data and cache deliberately share one base, which means both resolve
// to <dir>/TDrive. On a portable installation one folder is easier to
// copy, delete and reason about than two. CleanupCacheTemps only removes
// names carrying the prefixes it created, so the database and
// configuration sitting in the same directory are not at risk -- and
// there is a test asserting exactly that, because it is the kind of
// assumption that stops being true quietly.
func adopt(dir string) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("datadir: create portable directory %s: %w", dir, err)
	}
	if err := checkWritable(dir); err != nil {
		return fmt.Errorf("%w: %s: %v. TDrive will not fall back to the "+
			"user profile: your data would stay on this machine instead of "+
			"travelling with the app. Move the application to a writable "+
			"location, or remove %s to use the normal install location",
			ErrPortableUnwritable, dir, err, MarkerName)
	}
	Set(dir)
	SetCache(dir)
	portableMu.Lock()
	portableDir = dir
	portableMu.Unlock()
	return nil
}

// checkWritable proves the directory accepts a write.
//
// It creates, writes, closes and removes a real file rather than
// inspecting permission bits. Windows ACLs make stat-based checks
// unreliable, and a read-only USB stick reports mode bits that look
// perfectly writable.
func checkWritable(dir string) error {
	file, err := os.CreateTemp(dir, ".tdrive-writable-")
	if err != nil {
		return err
	}
	name := file.Name()
	_, writeErr := file.Write([]byte{0})
	closeErr := file.Close()
	removeErr := os.Remove(name)
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

// Portable reports the portable base directory and whether portable mode
// is active. Useful for a status screen, and for telling the user where
// their files actually are.
func Portable() (string, bool) {
	portableMu.RLock()
	defer portableMu.RUnlock()
	return portableDir, portableDir != ""
}

// DisablePortable clears portable mode and the overrides it set. Exists
// for tests; production code calls EnablePortable once and leaves it.
func DisablePortable() {
	portableMu.Lock()
	portableDir = ""
	portableMu.Unlock()
	Set("")
	SetCache("")
}
