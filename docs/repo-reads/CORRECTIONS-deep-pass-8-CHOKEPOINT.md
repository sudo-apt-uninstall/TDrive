# Deep pass 8 — 2026-10-11 — TDrive, delegated round 1

First round of the delegated deep-pass programme. An external agent with GitHub
read **and code execution** was given a seven-task reading brief with mandatory
evidence rules. It could not clone (its environment blocks git network ops) and
was rate-limited partway, and **it said so plainly rather than implying
coverage** — the behaviour the brief was designed to force.

Its highest-impact finding contradicts something I published as settled. I
verified that finding independently before accepting it. It is correct and I
was wrong.

HEAD read: `bbb96469757254f9f08f2e082187c9bb3d09c70b` (master, 2026-10-11T17:35:20Z).

---

## 1. 🔴🔴 MY "COMPLETE AND BOUNDED" BYPASS LIST WAS WRONG — 7 PRODUCTION
   BYPASSES, NOT 2

In the C-1 analysis I wrote:

> GitHub code search returned `incomplete_results: false`, 9 hits: these 2 +
> datadir itself + `internal/app/app.go` + 5 test files. **The list is complete
> and bounded.**

That claim rested on a search for **`os.UserConfigDir` only**. The requirement
it was supposed to answer — "every path must derive from the `datadir`
chokepoint" — is not a question about one function name.

My own verification this turn, `repo:jeetrex17/TDrive "os.MkdirTemp(\"\""`,
`incomplete_results: false`, **8 hits**:

| File | Status |
|---|---|
| `backend/services/file/import.go` | production — known (`tdrive-import-*`) |
| `backend/mountdav/resume.go` | production — **NEW** (`tdrive-mountdav-resume-*`) |
| `backend/thumbnail/poster_desktop.go` | production — **NEW** (`tdrive-poster-`) |
| `backend/media/mpv_thumbnail_session_unix.go` | production — **NEW** (`mpvThumbnailTempPrefix`) |
| `backend/media/video_thumbnails.go` | production — **NEW** (`videoThumbTempPrefix`) |
| `backend/nativeplayer/player_linux.go` | production — **NEW** (`tdrive-mpv-*`) |
| `cmd/tdrive/cat.go` | production — **NEW** (`tdrive-cat-*`) |
| `cmd/tdrive/cli_wire_unix_test.go` | test |

So the portable-mode work is **not** "2 bypasses, closed". It is **2 closed and
5 more still open on desktop**, plus `import.go` which I already knew about and
under-weighted. Three of the new ones (`poster_desktop`, `video_thumbnails`,
`nativeplayer`) are media/thumbnail paths — exactly the code a user exercises
constantly by browsing a gallery, writing to the system temp directory on every
preview.

### This is governing pattern instance #10, and it is mine

The pattern I have been cataloguing in other people's code is *"a check that
covers part of a value and is treated as covering all of it."* Here:

- I searched **one symbol** and reported the result as a property of **the whole
  class of bypasses**.
- I did it in the analysis whose entire purpose was to establish that the
  chokepoint does not leak.
- And I had *already written down* that the guard must be extended to
  `os.TempDir`/`os.MkdirTemp("")` (deep pass 4, rule 6) — I recorded the
  extension as a to-do and then left the **completeness claim** standing as if
  the to-do did not invalidate it.

The last part is the real lesson. **A known gap in a search silently invalidates
every completeness claim that search supports, and does not announce itself.**
When I widen a search, I must re-examine what I previously concluded from the
narrow one.

### Consequences
- `backend/datadir/chokepoint_test.go` must ban `os.MkdirTemp("")`,
  `os.TempDir`, and `os.CreateTemp("")` — not as a refinement, but because the
  test as written **passes on code with 7 live bypasses**. A guard that passes
  on the defect it exists to catch is worse than no guard: it certifies.
- The `portable-mode` branch is **not** parity-ready. Five more call sites.
- Every "the list is complete" sentence I have written in this audit needs the
  search behind it named, so the claim can be re-checked when the search widens.

---

## 2. 🔴 RESUMABLE DOWNLOAD PEAKS AT 2× THE FILE, AND 3× WHEN REPLACING

Previously unexamined. The resumable download path writes a full staged copy
**and then a full output copy**, and both exist at once.

`backend/services/file/resumable_download.go`:
> `root = filepath.Join(dataDir, "downloads-in-progress")`
> `stagePath := filepath.Join(root, id+".partial")`
> `stage, err := os.OpenFile(stagePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)`

`backend/services/file/resumable_download_run.go`:
> `outputPath := filepath.Join(filepath.Dir(job.Destination), ".tdrive-download-"+job.ID+".tmp")`
> `output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)`

The stage is removed only after the transfer succeeds:
> `_ = os.Remove(job.StagePath)`

and if the destination already exists it is first moved aside:
> `return filepath.Join(filepath.Dir(job.Destination), ".tdrive-download-"+job.ID+".backup")`
> `if err := os.Rename(job.Destination, backup); err != nil {`

**A 10 GB file costs 20 GB of disk; re-downloading over an existing 10 GB file
costs 30 GB.** Against R1 ("minimise internal SSD writes") this is the largest
violation found in TDrive so far — larger than the ciphertext staging I had
been treating as the main one, and it is on the *download* side, which I had
not audited at all.

Mitigating, and worth crediting: the stage uses random-access `WriteAt` and is
`Sync()`ed **before** the block records are committed to the DB, so the
durability ordering is correct — this is resume done properly, just expensively.

> `if n, err := stage.WriteAt(data, block.local); err != nil {`
> `if err := stage.Sync(); err != nil {`
> `tx, err := s.DB.BeginTx(groupCtx, nil)`

**Folder download is better and confirms the earlier read**: the per-file temp
is created inside the staging tree that becomes the final folder, so publish is
a directory rename, not a copy — plain 10 GB folder peaks at 10 GB, not 20.
`maxConcurrentFolderFiles = 2`.

---

## 3. AN ENTIRE PACKAGE I DID NOT KNOW EXISTED: `backend/updater/`

29 files, ~135 KB — a **self-updater** that downloads release assets, verifies
them, extracts a ZIP and installs over the running binary:

> `f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)` — `download.go`
> `out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm|0o600)` — `extract_zip.go`
> `out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)` — `install.go`

It contains `manifest_signature.go` (9,233 B) + its test (9,357 B), so the
update manifest is signature-verified — a good sign, and the kind of thing worth
copying. But: a component that fetches a binary from the network and writes it
over the installed application is the **highest-consequence code in the repo**,
and it was not on any list of mine. For a portable app on a USB stick, an
auto-updater that writes to the install location is also a direct R2 concern.

**Added to the must-read list at the top**, specifically `service.go` (18,584 B),
`manifest_signature.go`, `install_windows.go` (4,330 B), and `download.go`.

---

## 4. Other verified facts from this round

**Upload, multipart (confirmed, now with the retry quote):** staging is one part
at a time —
> `stagedPart, err = stageUploadPart(ctx, stagingDir, encryptedStream, partLen)`
> `_ = os.Remove(stagedPart.Name())`

Peak = one part, bounded by `s.maxPartBytes()`. Plain multipart creates no
staged part at all: it seeks and streams the source.

**Resumable upload does NOT stage** — it reads the original file directly:
> `f, err := os.Open(sourcePath)`
and its part plan is explicitly sized to bound retransmission:
> `// buildResumablePartPlan limits the worst-case retransmission to about 512 MiB`
> `const target int64 = 512 << 20`

**Retry semantics, precisely (two different answers, which I had conflated):**
- automatic retry of a live attempt re-sends the **whole body / whole current
  part** (`// A retried attempt must resend the whole body from its start.`)
- resuming a durable job sends **only unconfirmed parts**
  (`for i := len(parts); i < job.PartCount; i++ {`)

**Plaintext size is required up front — confirmed at the entry point:**
> `info, err := plainFile.Stat()` / `plaintextSize := info.Size()` — `upload_single.go`
> `plan, err := s.buildResumablePartPlan(size)` — `resumable_run.go`

(Which is exactly why Mahmud0808's `storedSize()` arithmetic from deep pass 6
is the fix for the single-part ciphertext temp: the input it needs is already
in hand.)

**Import bounds, now quantified** (`import.go`):
> `archiveExtractedFileLimit            = 10000`
> `archiveExtractedBytesLimitFloor      = 512 * 1024 * 1024`
> `archiveExtractedBytesLimitMultiplier = 8`
> `written, copyErr := io.Copy(f, io.LimitReader(&contextReader{ctx: ctx, source: r}, entryReadLimit))`

⇒ at most 10,000 files, total ≤ `max(512 MiB, 8 × s.largeFileMaxBytes())`,
cleaned with `_ = os.RemoveAll(tmpRoot)`. So the archive bomb risk is handled;
the defect is purely that the root is `os.MkdirTemp("")`.

**T6 — my phrasing was imprecise and is corrected.** Move/rename are **not
"pure DB mutations"**. They emit projection operations to Telegram:
> `op := projection.Op{ Type: projection.OpRename, Obj: ..., Name: newName }`
> `_, err = s.emit(ctx, channelID, op)`

No bytes move, which was my actual point — but "DB mutation" was the wrong
description of a design whose whole merit is that the mutation goes **into the
channel**. There is no `Copy` function in `mutations.go` at this SHA.
Hard purge does delete Telegram messages (`s.TG.DeleteMessages(ctx, peer, msgIDs)`).

**T7 — `state.save()` confirmed verbatim:**
> `tmp := path + ".tmp"`
> `if err := os.WriteFile(tmp, b, 0o600); err != nil {`
> `return os.Rename(tmp, path)`

No `Sync()`, no parent-directory fsync, no Windows sharing-violation retry.
Blocker B-2, in Go, confirmed at the source.

**Also newly surfaced, bounds not yet established:** `backend/applog/applog.go`
opens the log `O_TRUNC` with no size cap; `cmd/tdrive/daemon_process.go` appends
to a log file unbounded; `backend/mountscratch/store.go` has an aggregate
scratch reservation whose constant was not retrieved; `cmd/tdrive/install_cli.go`
appends to the user's shell rc file with no deduplication established.

---

## What this round did NOT establish
Declared by the agent, and I am recording it rather than letting it blur:
- The **file inventory of `backend/` and `cmd/` is incomplete.** These
  directories were enumerated but not recursed: `mountcontroller`,
  `nativeplayer`, `photobackup`, `processlock`, `projection`, `services`,
  `sync`, `tgclient`, `thumbnail`, `updater`, and both `cmd/` subtrees.
- T1 is **not exhaustive** — it is "every write site found before the rate
  limit", which is not the same thing.
- T2 covered `os.MkdirTemp("")` and `os.UserConfigDir` but **not**
  `os.UserHomeDir`, `os.TempDir`, or absolute-literal `filepath.Join`.
- The numeric value of `s.maxPartBytes()` and `s.largeFileMaxBytes()` is still
  unretrieved, so two bounds above are symbolic, not numeric.

## Next round for the delegate
Re-run T1/T2 to true exhaustion (the rate limit, not capability, was the
blocker), complete the inventory, resolve the two constants, and add
`backend/updater/` as a first-class target.
