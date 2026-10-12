package mountscratch

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newStore(t *testing.T, maxBytes int64) *Store {
	t.Helper()
	store, err := NewStore(Config{Root: filepath.Join(t.TempDir(), "scratch"), MaxBytes: maxBytes})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// TestPathForNameRejectsEscapes is the containment boundary. The names come
// from WebDAV resume sequences, which are keyed by client-supplied resource
// paths, so this is reachable from outside.
func TestPathForNameRejectsEscapes(t *testing.T) {
	store := newStore(t, 1<<20)

	bad := []string{
		"", ".", "..", "../escape", "../../etc/passwd",
		"sub/file", `sub\file`, "/absolute", `\absolute`,
		"/", "./file", "dir/", "a/../b",
	}
	for _, name := range bad {
		t.Run("reject "+name, func(t *testing.T) {
			if _, err := store.pathForName(name); !errors.Is(err, ErrInvalidName) {
				t.Fatalf("pathForName(%q) err = %v, want ErrInvalidName", name, err)
			}
			// And the public door, not just the private helper.
			if f, err := store.CreateExclusive(name, os.O_RDWR); err == nil {
				f.Close()
				t.Fatalf("CreateExclusive(%q) succeeded; it must not", name)
			}
		})
	}

	// Names that look dangerous but are legal single path elements.
	good := []string{"file.resume", "1.resume", "..leading-dots", "a..b", "-", "~"}
	for _, name := range good {
		t.Run("accept "+name, func(t *testing.T) {
			path, err := store.pathForName(name)
			if err != nil {
				t.Fatalf("pathForName(%q) = %v, want success", name, err)
			}
			if filepath.Dir(path) != store.Root() {
				t.Fatalf("pathForName(%q) escaped to %s", name, path)
			}
		})
	}
}

// TestCreateExclusiveIsPrivateAndExclusive pins the two properties the name
// promises. The file holds the user's bytes mid-upload.
func TestCreateExclusiveIsPrivateAndExclusive(t *testing.T) {
	store := newStore(t, 1<<20)

	first, err := store.CreateExclusive("one.resume", os.O_RDWR)
	if err != nil {
		t.Fatalf("CreateExclusive: %v", err)
	}
	defer first.Close()

	info, err := first.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("scratch file mode is %o, want 600", perm)
	}

	second, err := store.CreateExclusive("one.resume", os.O_RDWR)
	if err == nil {
		second.Close()
		t.Fatal("CreateExclusive reopened an existing name; O_EXCL is not being enforced")
	}
	if !os.IsExist(err) {
		t.Errorf("second create err = %v, want an IsExist error", err)
	}
}

// TestReserveDoesNotOverflow pins the guard. The obvious spelling,
// `s.usedBytes+size > s.maxBytes`, wraps for sizes near MaxInt64 and would
// admit a reservation that cannot be honoured.
func TestReserveDoesNotOverflow(t *testing.T) {
	store := newStore(t, 1<<20)

	if err := store.Reserve(math.MaxInt64); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Reserve(MaxInt64) err = %v, want ErrQuotaExceeded", err)
	}
	if used := store.UsedBytes(); used != 0 {
		t.Fatalf("a refused reservation changed usage to %d", used)
	}

	// Fill it, then try to overflow past the top.
	if err := store.Reserve(1 << 20); err != nil {
		t.Fatalf("Reserve(max) = %v, want success", err)
	}
	if err := store.Reserve(math.MaxInt64 - (1 << 20)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("overflowing reservation err = %v, want ErrQuotaExceeded", err)
	}
	if used := store.UsedBytes(); used != 1<<20 {
		t.Fatalf("usage = %d, want exactly the one honoured reservation", used)
	}
}

// TestReserveBoundaries checks the edges rather than the middle.
func TestReserveBoundaries(t *testing.T) {
	store := newStore(t, 100)

	if err := store.Reserve(100); err != nil {
		t.Fatalf("Reserve(exactly max) = %v, want success", err)
	}
	if err := store.Reserve(1); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Reserve(1) past a full store err = %v, want ErrQuotaExceeded", err)
	}
	if err := store.Reserve(0); err != nil {
		t.Errorf("Reserve(0) = %v, want success", err)
	}
	if err := store.Reserve(-1); !errors.Is(err, ErrInvalidSize) {
		t.Errorf("Reserve(-1) err = %v, want ErrInvalidSize", err)
	}
	if used := store.UsedBytes(); used != 100 {
		t.Errorf("usage = %d, want 100", used)
	}
}

// TestReleaseIsIdempotentAndClamped covers the rollback paths, which run on
// error and may run more than once.
func TestReleaseIsIdempotentAndClamped(t *testing.T) {
	store := newStore(t, 1000)
	if err := store.Reserve(400); err != nil {
		t.Fatal(err)
	}

	store.Release(400)
	store.Release(400) // a double rollback must not go negative
	store.Release(999999)

	if used := store.UsedBytes(); used != 0 {
		t.Fatalf("usage = %d after over-release, want 0", used)
	}
	// The full budget must still be available: a clamped counter that stayed
	// negative would hand out more than MaxBytes.
	if err := store.Reserve(1000); err != nil {
		t.Fatalf("Reserve(full budget) after over-release = %v", err)
	}
	if err := store.Reserve(1); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("store handed out more than MaxBytes: err = %v", err)
	}
}

// TestTrackWithoutReserveUnderCountsUsage documents an API hazard rather than
// a bug in the implementation.
//
// Track records a name against a size WITHOUT adding to usage, on the stated
// assumption that the caller already reserved it. ReleaseTracked then
// subtracts. A caller that tracks something it never reserved therefore
// deflates the counter and can exceed its own quota.
//
// The implementation is doing what it documents. This test exists so the
// asymmetry is visible and so a future change to either half is caught.
func TestTrackWithoutReserveUnderCountsUsage(t *testing.T) {
	store := newStore(t, 1000)
	if err := store.Reserve(600); err != nil {
		t.Fatal(err)
	}

	// Never reserved, but tracked.
	store.Track("phantom", 500)
	store.ReleaseTracked("phantom", 0)

	used := store.UsedBytes()
	if used != 100 {
		t.Fatalf("usage = %d, want 100 (600 reserved, 500 released untracked)", used)
	}
	t.Logf("usage fell to %d while 600 bytes are still outstanding: "+
		"Track must only ever be called for bytes already Reserved", used)
}

// TestReleaseTrackedFallbackOnlyAppliesWhenUntracked pins the fallback rule.
// Getting this backwards would double-release every tracked file.
func TestReleaseTrackedFallbackOnlyAppliesWhenUntracked(t *testing.T) {
	store := newStore(t, 1000)
	if err := store.Reserve(300); err != nil {
		t.Fatal(err)
	}
	store.Track("known", 300)

	// The tracked size must win over the fallback.
	store.ReleaseTracked("known", 999)
	if used := store.UsedBytes(); used != 0 {
		t.Fatalf("usage = %d, want 0; the fallback overrode the tracked size", used)
	}

	// An unknown name uses the fallback.
	store2 := newStore(t, 1000)
	if err := store2.Reserve(250); err != nil {
		t.Fatal(err)
	}
	store2.ReleaseTracked("never-tracked", 250)
	if used := store2.UsedBytes(); used != 0 {
		t.Fatalf("usage = %d, want 0; the fallback was not applied", used)
	}
}

// TestRepeatedReleaseTrackedAppliesTheFallbackAgain is a real hazard in the
// API, demonstrated rather than argued.
//
// ReleaseTracked deletes the record and then releases. Called a second time
// for the same name, the record is already gone, so the FALLBACK is applied
// and the bytes are released a second time. A cleanup path that can run twice
// -- an error rollback plus a deferred tidy-up, say -- therefore deflates the
// counter by the fallback amount each extra time.
//
// It is contained: Release clamps at zero, so the counter cannot go negative
// and the store cannot be tricked into unbounded over-allocation in a single
// sequence. But between other live reservations it under-counts, and the
// quota is the only thing bounding scratch growth.
//
// No caller in backend/mountdav does this today -- resumeStore releases via
// Release(entry.total), and writes.go has no ReleaseTracked call at all -- so
// this is LATENT. It is pinned here so it stays that way.
func TestRepeatedReleaseTrackedAppliesTheFallbackAgain(t *testing.T) {
	store := newStore(t, 1000)

	if err := store.Reserve(300); err != nil {
		t.Fatal(err)
	}
	store.Track("doomed", 300)
	store.ReleaseTracked("doomed", 300)
	if used := store.UsedBytes(); used != 0 {
		t.Fatalf("usage = %d after the first release, want 0", used)
	}

	// Another sequence's bytes, correctly reserved and still outstanding.
	if err := store.Reserve(800); err != nil {
		t.Fatal(err)
	}

	// The duplicate cleanup call.
	store.ReleaseTracked("doomed", 300)

	used := store.UsedBytes()
	if used == 800 {
		t.Fatal("ReleaseTracked became idempotent; update this test and the " +
			"note on the hazard, because the API contract changed")
	}
	if used != 500 {
		t.Fatalf("usage = %d, want 500 (800 outstanding minus a 300 fallback)", used)
	}
	t.Logf("duplicate ReleaseTracked dropped usage to %d while 800 bytes are "+
		"outstanding: callers must release a name exactly once", used)

	// The containment that keeps this latent rather than exploitable.
	for i := 0; i < 20; i++ {
		store.ReleaseTracked("doomed", 300)
	}
	if used := store.UsedBytes(); used != 0 {
		t.Fatalf("usage = %d, want 0 (clamped)", used)
	}
	if err := store.Reserve(1000); err != nil {
		t.Fatalf("Reserve(full budget) = %v", err)
	}
	if err := store.Reserve(1); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("the clamp failed and the store exceeded MaxBytes: %v", err)
	}
}

// TestAccountExistingCountsLeftovers covers restart after a crash, which is
// the whole reason the option exists.
func TestAccountExistingCountsLeftovers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scratch")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "left.resume"), make([]byte, 700), 0o600); err != nil {
		t.Fatal(err)
	}
	// A subdirectory must not be counted: only regular files are.
	if err := os.Mkdir(filepath.Join(root, "a-dir"), 0o700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(Config{Root: root, MaxBytes: 1000, AccountExisting: true})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if used := store.UsedBytes(); used != 700 {
		t.Fatalf("usage = %d, want 700 from the leftover file", used)
	}
	if err := store.Reserve(400); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Reserve(400) with 700 outstanding err = %v, want ErrQuotaExceeded", err)
	}
	// And the leftover can be released by name, which is the point of
	// recording it in trackedBytes at construction.
	store.ReleaseTracked("left.resume", 0)
	if used := store.UsedBytes(); used != 0 {
		t.Fatalf("usage = %d after releasing the leftover, want 0", used)
	}
}

// TestAccountExistingCanStartOverQuota is a real consequence worth knowing.
// Leftovers larger than MaxBytes leave the store unable to reserve anything
// until something is released. That is safe, but it means a crash can wedge
// the subsystem until cleanup runs.
func TestAccountExistingCanStartOverQuota(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scratch")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "huge.resume"), make([]byte, 2000), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(Config{Root: root, MaxBytes: 1000, AccountExisting: true})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if used := store.UsedBytes(); used != 2000 {
		t.Fatalf("usage = %d, want 2000", used)
	}
	if err := store.Reserve(1); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err = %v, want ErrQuotaExceeded", err)
	}
	t.Log("a store that starts over quota refuses every reservation until released")
}

// TestNewStoreRejectsBadConfig covers the constructor's own guards.
func TestNewStoreRejectsBadConfig(t *testing.T) {
	if _, err := NewStore(Config{Root: "", MaxBytes: 10}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("empty root err = %v, want ErrInvalidConfig", err)
	}
	if _, err := NewStore(Config{Root: t.TempDir(), MaxBytes: 0}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("zero MaxBytes err = %v, want ErrInvalidConfig", err)
	}
	if _, err := NewStore(Config{Root: t.TempDir(), MaxBytes: -1}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("negative MaxBytes err = %v, want ErrInvalidConfig", err)
	}
}

// TestRootIsPrivate pins the directory mode. Scratch holds plaintext bytes of
// the user's files on a multi-user machine.
func TestRootIsPrivate(t *testing.T) {
	store := newStore(t, 1<<20)
	info, err := os.Stat(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("scratch root mode is %o, want 700", perm)
	}
}

// TestNewStoreTightensAnExistingLooseRoot is the case MkdirAll alone does not
// cover: the directory already exists with permissive bits, so the explicit
// Chmod after MkdirAll is load-bearing.
func TestNewStoreTightensAnExistingLooseRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scratch")
	if err := os.MkdirAll(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(Config{Root: root, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	info, err := os.Stat(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("pre-existing world-accessible root left at %o; "+
			"the Chmod after MkdirAll is what fixes this", perm)
	}
}

// TestConcurrentReserveNeverExceedsTheBudget is the assertion the mutex
// exists for: 400 goroutines competing for exactly 100 slots.
//
// NOT VERIFIED UNDER -race. The environment these tests were written in has
// no C toolchain, and -race requires cgo. This test checks the OUTCOME is
// sound -- nothing over-allocates and the counter agrees with what was handed
// out -- which a torn read could still pass by luck. Run it with -race where
// a C toolchain exists before treating the locking as proven.
func TestConcurrentReserveNeverExceedsTheBudget(t *testing.T) {
	const budget = 1000
	const chunk = 10
	store := newStore(t, budget)

	var wg sync.WaitGroup
	var granted int64
	var mu sync.Mutex
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Reserve(chunk); err == nil {
				mu.Lock()
				granted += chunk
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if granted > budget {
		t.Fatalf("granted %d bytes against a %d-byte budget", granted, budget)
	}
	if used := store.UsedBytes(); used != granted {
		t.Fatalf("usage %d disagrees with granted %d", used, granted)
	}
	if granted != budget {
		t.Errorf("granted %d, want the whole %d-byte budget to be usable", granted, budget)
	}
}

// TestConcurrentTrackAndReleaseExercisesTheMapUnderLock drives concurrent
// writes to trackedBytes. Without the mutex this is a concurrent map write,
// which Go detects and panics on even without -race -- so this one does carry
// evidence in the environment it was run in.
func TestConcurrentTrackAndReleaseExercisesTheMapUnderLock(t *testing.T) {
	store := newStore(t, 1<<20)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		name := "f" + strings.Repeat("x", i%7)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Reserve(10); err != nil {
				return
			}
			store.Track(name, 10)
			store.ReleaseTracked(name, 10)
		}()
	}
	wg.Wait()
	t.Logf("final usage %d", store.UsedBytes())
}
