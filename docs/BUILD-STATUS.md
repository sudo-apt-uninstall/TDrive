# Build status

**First compiled evidence on this branch.** Everything here is literal output,
pasted untidied, from a real toolchain.

Until today every commit on `portable-mode` carried an `UNCOMPILED` marker,
because the environment the code was written in had no Go toolchain and no
network egress. That is no longer true for `backend/datadir` and
`backend/httprange`.

## Toolchain

The 69 MB Go archive was delivered in three parts to get past an attachment
size limit, reassembled, and verified before use.

```
parts:  26,214,400 + 26,214,400 + 18,162,023 = 70,590,823 bytes (ZIP container)
inner:  go1.27.2.linux-amd64.tar.gz           = 70,590,635 bytes
sha256: ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5
        MATCH
```

That digest is worth a sentence. It was recorded independently, in another
session, by a reviewer who downloaded the same archive from a different source
and checked it before running it. Two independent paths to the same 64 hex
characters is a stronger provenance statement than either one alone.

```
$ go version
go version go1.27.2 linux/amd64
```

## What was built

`backend/datadir` imports only the standard library. That is what made this
possible without network access: the package was extracted into a bare module
(`go mod init TDrive`, `GOPROXY=off`) and built with zero module downloads. The
rest of the repository needs Wails v3, gotd/td and native dependencies, and
remains unbuilt here.

```
$ go build ./...
EXIT=0

$ go vet ./...
EXIT=0

$ go test ./backend/datadir/... -v -count=1
=== RUN   TestNoProductionCodeBypassesDatadir
--- PASS: TestNoProductionCodeBypassesDatadir (0.00s)
=== RUN   TestTheGuardFiresOnAKnownBypass
--- PASS: TestTheGuardFiresOnAKnownBypass (0.00s)
=== RUN   TestTheGuardStaysSilentOnCorrectCode
--- PASS: TestTheGuardStaysSilentOnCorrectCode (0.00s)
=== RUN   TestTheForbiddenSetsAreNotEmpty
--- PASS: TestTheForbiddenSetsAreNotEmpty (0.00s)
PASS
ok  	TDrive/backend/datadir	0.006s
EXIT=0
```

⚠️ **`TestNoProductionCodeBypassesDatadir` passing above means nothing.** It
walks up to `go.mod` and checks every production file beneath it, and in an
extracted module there are none. A test that cannot fail is not evidence.

## The claim, measured

Commit `49decba8` rewrote the chokepoint guard, asserting that the previous
version passed while production files bypassed the chokepoint. That was an
argument. Here it is as a measurement.

A synthetic tree was built containing real code at its real paths: the two
functions from `backend/media/video_thumbnails.go` that resolve a directory for
themselves, and `backend/services/file/download_staging.go`, which calls
`os.MkdirTemp(resolvedParent, ...)` and is **correct**.

### The rewritten guard

```
=== RUN   TestNoProductionCodeBypassesDatadir
    chokepoint_test.go:122: production code resolved a directory without datadir:
          backend/media/video_thumbnails.go:40: os.MkdirTemp
          backend/media/video_thumbnails.go:59: os.TempDir
          backend/media/video_thumbnails.go:72: os.TempDir
--- FAIL: TestNoProductionCodeBypassesDatadir (0.00s)
```

Three offenders, and **nothing reported for `download_staging.go`**. That pair
is the whole point. `os.MkdirTemp` with an empty first argument asks the OS for
its temp directory; the same call with a real parent stages a sibling of the
destination and is the pattern this repository should prefer. A guard that
banned the name outright would have failed on correct code.

### The previous guard, over the same tree

```
=== RUN   TestNoProductionCodeBypassesDatadir
--- PASS: TestNoProductionCodeBypassesDatadir (0.00s)
PASS
EXIT=0
```

**Zero offenders. A clean pass over code that leaks three times in one file.**

## Bypasses closed

Three sites are now routed through `datadir`. None could be compiled in its own
package — they need the transport stack, the image pipeline, or the full CLI —
so each was checked three ways before being pushed: `gofmt -l` for formatting,
`go/parser` with `AllErrors` for syntax, and the chokepoint detector itself for
compliance.

```
$ gofmt -l cat.go poster_desktop.go mpv_thumbnail_session_unix.go
(no output)

$ verify cat.go poster_desktop.go mpv_thumbnail_session_unix.go
CLEAN        cat.go  (syntax ok, 0 offenders)
CLEAN        poster_desktop.go  (syntax ok, 0 offenders)
CLEAN        mpv_thumbnail_session_unix.go  (syntax ok, 0 offenders)
EXIT=0
```

| File | Was | Now |
|---|---|---|
| `cmd/tdrive/cat.go` | `os.MkdirTemp("", "tdrive-cat-*")` | `datadir.CreateCacheTempDir` |
| `backend/thumbnail/poster_desktop.go` | `os.MkdirTemp("", "tdrive-poster-")` | `datadir.CreateCacheTempDir` |
| `backend/media/mpv_thumbnail_session_unix.go` | `os.MkdirTemp("", mpvThumbnailTempPrefix)` | `datadir.CreateCacheTempDir` + length guard |

### Why the helper did not exist

All of these needed a temporary **directory**, and the chokepoint only offered
`CreateCacheTemp`, which returns a **file**. There was no correct call to make,
so every author reached for the OS. That is one gap in an abstraction, not
several independent lapses — a chokepoint with a hole in it routes traffic
around itself. `CreateCacheTempDir` and `CacheTempDirs` were added in
`6dd1f200`.

### The hazard the mpv fix surfaced

That directory exists to hold a **Unix domain socket**, and `sun_path` is 108
bytes on Linux, 104 on macOS and BSD. The OS temp directory is short, so the
limit was never near. TDrive's cache root is not: on a portable installation it
can be arbitrarily deep, and a stick mounted at
`/Volumes/Backup Drive 2024/Apps/TDrive Portable` is ordinary rather than
contrived.

`bind()` does not degrade past that limit. It fails with `EINVAL`, mpv reports
nothing useful, and the symptom appears five seconds later as *"mpv IPC socket
was not ready"* — which points at mpv, at the network, at anything except the
length of a path. The fix carries an explicit check at 100 bytes that names the
real cause.

Routing the directory without that check would have traded a portability bug
for an intermittent, badly-attributed failure on exactly the installations this
branch exists to support.

## `backend/httprange` — written, not borrowed

The nineteen-repository survey behind this branch set out to find a correct
HTTP Range implementation to copy. It did not find one. Every candidate failed
at a boundary: negative lengths from `bytes=9-3` used to build headers, starts
past the end clamped to the last byte and returned under a 206, range handling
skipped entirely for zero-length files, and a part plan whose two ends were
computed in different coordinate systems.

So this package was written rather than adapted. It is the second on this
branch to compile, and like `datadir` it imports only the standard library.

```
$ gofmt -l backend/httprange/
(no output)

$ go vet ./backend/httprange/...
EXIT=0

$ go test ./backend/httprange/... -count=1 -v
--- PASS: TestParse (0.00s)
--- PASS: TestLengthAlwaysAgreesWithTheInterval (0.00s)
--- PASS: TestSplitCoversExactlyTheRange (0.00s)
--- PASS: TestSplitSingleByteFetchesOnePart (0.00s)
--- PASS: TestSplitRejectsNonsense (0.00s)
--- PASS: TestContentRangeHeaders (0.00s)
PASS
ok  	TDrive/backend/httprange	0.002s
```

Six tests, forty subtests, green on the first run.

### Which is exactly why they were then attacked

**A suite that passes first try has not yet been shown to be capable of
failing.** Nine mutants were seeded into `httprange.go`, one at a time, each
disabling a single guard. Seven died immediately. Two survived.

```
killed    inverted-range reject (bytes=9-3)
killed    end truncation (end > size-1)
killed    suffix-zero reject (bytes=-0)
killed    suffix clamp (n > size)
killed    digit-only check in parseUint
SURVIVED  Split last-part from End not Length   <-- NO TEST KILLS THIS
killed    LastKeep inclusive +1
killed    FirstTrim subtraction
SURVIVED  multiple-range reject   <-- NO TEST KILLS THIS
```

**The first survivor is the FileToLink defect itself** — the one bug this
entire package was written to avoid — and the suite built specifically to catch
it did not. That is the finding of this section.

It survived for a reason worth keeping. `Length == End - Start + 1`, so
`Start + Length == End + 1`, and the two formulas diverge only when a range
*ends on the last byte of a part*. At that offset the buggy plan adds a part
and sets `LastKeep: 0` — and a trailing part contributing zero bytes **rebuilds
the correct interval exactly**, because the extra part and the zero cancel.
The differential test was satisfied. The over-fetch bound then missed by one
byte: waste was 1,048,575 against a threshold of 1,048,576.

```
range [524287,524287] len=1
   correct: parts=1 fetch=524288   rebuild_end=524287 LastKeep=524288
   mutant : parts=2 fetch=1048576  rebuild_end=524287 LastKeep=0
   rebuild agrees? True   waste=1048575   waste>=2P? False
```

A megabyte fetched to serve one byte, and two independent assertions both
reporting success.

The missing invariant was never about arithmetic: **every part in a plan must
contribute at least one byte.** `LastKeep == 0` is proof that a part should not
be in the plan at all. With that assertion plus an independently-computed
minimum part count, the mutant dies.

The regression test had a second weakness worth naming: it used `bytes=0-0`,
which is the *single offset where the buggy formula agrees with the correct
one*. It now also tests `bytes=524287-524287`, where they diverge.

The second survivor is benign and was verified rather than assumed. Removing
the comma check still refuses `bytes=0-10,20-30`, because the comma lands
inside a bound and `parseUint` rejects it. The guard is deliberately redundant;
it is kept, and labelled in the source as a known survivor so nobody deletes it
as dead code or trusts it as load-bearing.

**Rule earned: a test suite that passes on the first run is an untested
measuring instrument. Seed the defect it was written to catch and watch it
fail, or it has proved nothing.** This is the second time on this project that
the measuring instrument carried the defect it was measuring for.

## Still open

- **`backend/media/video_thumbnails.go` — three sites, unfixed.** 28,128 bytes.
  Re-emitting a file that size through this channel to change three lines is a
  larger risk than the defect: a transcription error in working code is harder
  to find than a known bypass. It needs a local clone and a three-line edit:
  `os.MkdirTemp("")` in `ensureDir`, and `os.TempDir()` twice in
  `sweepStaleVideoThumbnailDirs`. The second of those should move to
  `datadir.CacheTempDirs()` — a sweep that keeps looking in the system temp
  directory after creation moved will find nothing, report success, and leak
  forever.
- `backend/mountdav/resume.go`, `backend/nativeplayer/player_linux.go` (16,944 B)
  and `backend/services/file/import.go` (24,294 B) — same shape, not yet done.
- **The true bypass count is still unknown.** The figure of seven came from
  searching a single symbol; `video_thumbnails.go` alone has three. The guard
  will give the real number the first time it runs over the whole tree.
- **`cmd/tdrive/portable_startup.go` has not been compiled.** It is `package
  main` and needs the dependency tree. The `init()` ordering argument in that
  file is still an argument.
- **`httprange` is not wired to anything yet.** It compiles and it is tested,
  but no handler calls it. Until the WebDAV and HTTP serving paths use it, the
  correct implementation and the shipping behaviour are different things.
- `go build ./...` across the repository has never run.
- No Telegram transfer, no GUI, no Windows run, no portable-mode run from
  actual removable media.

## Reproducing

```sh
export GOROOT=/path/to/go1.27.2 PATH=$GOROOT/bin:$PATH GOPROXY=off
mkdir -p probe/backend/datadir && cd probe && go mod init TDrive
# copy backend/datadir/{datadir,portable,chokepoint_test,datadir_test}.go
go build ./... && go vet ./... && go test ./backend/datadir/... -v -count=1
```

To reproduce the measurement, add the two files named above at their real paths
and run the walk again.

`backend/httprange` is standard-library-only and needs no other package:

```sh
mkdir -p probe/backend/httprange   # copy httprange.go + httprange_test.go
go test ./backend/httprange/... -count=1 -v
```

To reproduce the mutation run, replace one guard at a time with `if false {`
and confirm a test fails. If none does, the guard is untested — fix the suite,
not the guard.
