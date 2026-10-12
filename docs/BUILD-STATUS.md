# Build status

**First compiled evidence on this branch.** Everything here is literal output,
pasted untidied, from a real toolchain.

Until today every commit on `portable-mode` carried an `UNCOMPILED` marker,
because the environment the code was written in had no Go toolchain and no
network egress. That is no longer true for `backend/datadir`.

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
characters is a stronger provenance statement than either one alone, and it is
cheap to state.

```
$ go version
go version go1.27.2 linux/amd64
```

## What was built

`backend/datadir` imports only the standard library — `fmt`, `log/slog`, `os`,
`path/filepath`, `strings`, `sync`, plus `go/ast`, `go/parser`, `go/token`,
`io/fs`, `strconv`, `runtime` and `testing` in the tests. No external module is
reachable from it.

That is what made this possible without network access: the package was
extracted into a bare module (`go mod init TDrive`, `GOPROXY=off`) and built
with zero module downloads. The rest of the repository needs Wails v3, gotd/td
and native dependencies, and remains unbuilt here.

```
$ go build ./...
EXIT=0

$ go vet ./...
EXIT=0
```

## Tests

```
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
extracted module there are none. A test that cannot fail is not evidence. The
next section is the one that counts.

## The claim, measured

Commit `49decba8` rewrote the chokepoint guard, asserting that the previous
version passed while seven production files bypassed the chokepoint. That was
an argument. Here it is as a measurement.

A synthetic tree was built containing real code at its real paths:

- `backend/media/video_thumbnails.go` — the two functions from blob
  `3beff00ee5a5f53e572f503d3c52af56adba5836` that resolve a directory for
  themselves (`ensureDir`, `sweepStaleVideoThumbnailDirs`), copied verbatim.
  The rest of that file is omitted; the walk only parses, so an incomplete
  package still exercises the detector against the real call shapes.
- `backend/services/file/download_staging.go` —
  `os.MkdirTemp(resolvedParent, ".tdrive-folder-download-*")`, which is
  **correct** and must keep passing.

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
its temp directory and ignores every override portable mode depends on; the
same call with a real parent stages a sibling of the destination and is the
pattern this repository should prefer. One function, two meanings, told apart
only by an argument — and a guard that banned the name outright would have
failed on correct code, which is precisely the mistake already documented in
that file about `os.UserHomeDir`.

### The previous guard, over the same tree

```
=== RUN   TestNoProductionCodeBypassesDatadir
--- PASS: TestNoProductionCodeBypassesDatadir (0.00s)
PASS
EXIT=0
```

**Zero offenders. A clean pass over code that leaks three times in one file.**

A guard that passes on the defect it was written to catch is worse than no
guard, because it certifies. Nobody reading a green run had any reason to look.

## A correction this produced

The audit recorded `backend/media/video_thumbnails.go` as having **one**
bypass. It has **three**: `os.MkdirTemp("", videoThumbTempPrefix)` in
`ensureDir`, and `os.TempDir()` **twice** in `sweepStaleVideoThumbnailDirs`.

The figure of seven production bypasses came from searching a single symbol,
`os.MkdirTemp("")`. **The real number is higher and is still unknown.** It will
be known when this guard runs over the whole tree — which is what the guard is
for, and why its own accuracy had to be established first.

## What is still unverified

- **`cmd/tdrive/portable_startup.go` has not been compiled.** It is `package
  main` and needs the rest of the CLI, which needs the dependency tree. The
  `init()` ordering argument in that file is still an argument.
- **`go build ./...` across the repository has never run.** Only
  `backend/datadir`.
- `datadir_test.go` was present in the extracted module and its tests are not
  listed above because the run shown was scoped to the chokepoint file; the
  full package run is `go test ./backend/datadir/...` with no `-run` filter.
- No Telegram transfer, no GUI, no Windows run, no portable-mode run from
  actual removable media.

## Reproducing

```sh
export GOROOT=/path/to/go1.27.2 PATH=$GOROOT/bin:$PATH GOPROXY=off
mkdir -p probe/backend/datadir && cd probe && go mod init TDrive
# copy backend/datadir/{datadir,portable,chokepoint_test,datadir_test}.go
go build ./... && go vet ./... && go test ./backend/datadir/... -v -count=1
```

To reproduce the measurement, add the two files named above at their real
paths and run the walk again.
