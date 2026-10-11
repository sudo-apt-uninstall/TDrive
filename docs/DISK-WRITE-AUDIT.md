# Disk-write audit

**Requirement: disk-write discipline is non-negotiable.** Every byte TDrive
puts on disk must be accounted for, and bytes written for no reason must
stop being written.

This document records what the code actually does, read at
`0e8ef556`. Claims here cite the file that proves them, because the
headline finding contradicts the assumption the audit started from.

---

## 1. The named caveat, and what the code says

The complaint: *folder uploads are forcibly encrypted, and encryption
causes disk writes.*

The second half is true and worse than it sounds. **The first half is not
true at the backend layer**, which changes what the fix is.

### Encryption is a parameter, not a policy

`backend/services/file/import.go`:

```go
func (s *Service) RunImport(ctx context.Context, channelID int64,
    paths []string, parentID string, encrypt, extractArchives bool) error
```

`encrypt` is threaded through, unmodified, from both callers:

| Caller | Call |
|---|---|
| `internal/app/app.go` (GUI) | `svc.RunImport(ctx, a.ActiveChannelID(), paths, parentID, encrypt, extractArchives)` |
| `backend/daemon/server_commands.go` (CLI daemon) | `RunImport(ctx, drive.ID, []string{localPath}, parentID, encrypt, extract)` |

Nothing between the UI and the uploader forces it on. And the CLI
**defaults it off** — `cmd/tdrive/main.go`:

```go
encrypt := false
...
case "-e", "--encrypt":
    encrypt = true
```

> **Therefore `tdrive put <folder>` with no `-e` should already perform an
> unencrypted folder import, with zero encryption staging.**
>
> This is untested by us and is the **first thing to verify**, because if
> it holds, the most painful caveat is a GUI limitation with a working
> command-line path around it today.

Where forcing genuinely does exist, per the README: **photo and video
backup always encrypts.** That is a deliberate product decision for a
different feature, not the folder-import path.

**Open question for the frontend audit:** does the GUI import dialog
expose the encrypt toggle for folders, default it on, or hide it? That is
a TypeScript question and is the next read.

## 2. The staging write is real, and it is a full second copy

`backend/services/file/upload.go`:

```go
uploadSize := plaintextSize
if encrypted {
    started := time.Now()
    tempCipher, err := s.writeCiphertextTemp(
        &contextReader{ctx: ctx, source: source}, plaintextSize, masterKey)
    reportBackupTiming(ctx, "encrypt", started)
```

which reaches `backend/services/encryption/service.go`:

```go
func (s *Service) WriteCiphertextTemp(plain io.Reader, plaintextSize int64,
    masterKey []byte) (*os.File, error) {
    tmp, err := datadir.CreateCacheTemp("tdrive-enc-*")
```

So every encrypted upload writes **the entire ciphertext to disk** before
a single byte reaches Telegram. Import a 40 GB folder encrypted and TDrive
writes 40 GB of staging, on top of reading 40 GB of source — for data
whose destination is the network.

To its credit this goes through `datadir.CreateCacheTemp`, so it is
inside the app's own cache directory, it is cleaned up by
`CleanupCacheTemps`, and in portable mode it will land on the portable
volume rather than the system drive. It is **accounted for and
contained** — it simply should not exist.

## 3. The staging is avoidable, and the codebase already proves it

The only structural reason to stage ciphertext is that **Telegram needs a
content length before the upload starts**, and you cannot know a
ciphertext's length without producing it.

Except here you can, and TDrive already does:

```go
if uploadByteSize(size, encrypt) > s.largeFileMaxBytes() {
```

`uploadByteSize(size, encrypt)` is called in `planFile`, `addFileTask`
and twice in `extractArchiveToTemp` — **before any encryption happens** —
to decide whether a file exceeds the per-file limit. It computes the
exact encrypted size from the plaintext size, which it can do because
XChaCha20-Poly1305 in a chunked stream has deterministic overhead: a
fixed header plus a fixed tag per chunk.

> **If the size is computable in advance, the bytes can be encrypted on
> the fly.** The temp file buys nothing that the arithmetic does not
> already provide.

### The shape of the fix

Replace `WriteCiphertextTemp` with a streaming encrypting reader:

```go
// io.Reader that encrypts as it is read; length known in advance from
// uploadByteSize(plaintextSize, true).
func (s *Service) CiphertextReader(plain io.Reader, plaintextSize int64,
    masterKey []byte) (io.Reader, int64, error)
```

The upload path already has a `contextReader` wrapper and already carries
`uploadSize` separately from `plaintextSize`, so the call site is close to
the right shape.

**What makes this non-trivial, and must not be hand-waved:**

1. **Retry.** A failed part needs re-reading from an offset. A temp file
   seeks; a stream does not. Either the source is re-opened and
   re-encrypted from the chunk boundary — requiring the nonce/chunk
   scheme to be deterministic and seekable — or retry falls back to
   staging. **Check what the existing format allows before designing.**
2. **The resume path.** The README advertises resuming large uploads from
   confirmed parts, explicitly for *unencrypted* files. Streaming
   encryption must not quietly break the way those two interact.
3. **`uploadByteSize` must be exactly right.** It is currently used for a
   limit comparison, where being off by a tag length is invisible. As the
   declared upload length it must be exact, or Telegram rejects the
   upload. **Test it against real encrypted output before trusting it.**

That third point is this project's governing defect pattern waiting to
happen: a value that is correct for the purpose it was written for, reused
for a purpose with a stricter requirement.

## 4. A third chokepoint bypass — archive extraction

`RunImport`:

```go
ensureTmp := func() (string, error) {
    if tmpRoot == "" {
        dir, err := os.MkdirTemp("", "tdrive-import-*")
```

`os.MkdirTemp("")` uses the **system** temp directory — not
`datadir.CacheDir()`. So archive extraction:

- is **not** counted by any cache accounting;
- is **not** removed by `CleanupCacheTemps`, which only scans the cache
  root (it *is* removed by the `defer os.RemoveAll` on the happy path, but
  a hard kill leaves it);
- **ignores portable mode entirely** — extract a 5 GB archive on a
  portable install and 5 GB lands on the system drive.

Different in kind from the `os.UserConfigDir` bypasses already fixed —
that was config, this is bulk payload — but the same shape: a centralising
abstraction that one caller goes around.

**Mitigation already present:** the temp dir is created lazily, and the
comment says so explicitly — *"so a folder-only import never touches
/tmp."* Folder imports, the case that matters here, do not reach it.

**Fix:** `os.MkdirTemp(cacheDir, "tdrive-import-*")` using
`datadir.CacheDir()`. One line, with the extraction size already bounded
by `maxArchiveExtractBytes`.

## 5. Every disk write in the upload path

| Write | Where | Through `datadir`? | Avoidable? |
|---|---|---|---|
| Ciphertext staging | `encryption/service.go` `WriteCiphertextTemp` | Yes (`tdrive-enc-*`) | **Yes — §3** |
| Archive extraction | `import.go` `ensureTmp` | **No — system temp** | No (must land somewhere), but must be relocated — §4 |
| Upload part staging | `tdrive-upload-*`, `.tdrive-upload-part-*` | Yes | Unaudited |
| Rendition source | `tdrive-rendition-source-*` | Yes | Unaudited |
| WebDAV PUT staging | `tdrive-mountdav-put-*` | Yes | Unaudited |
| `tdrive.db` projection | `backend/datadir` | Yes | No — it is the local projection |
| `cli.json`, `daemon.log` | *fixed on this branch* | Yes, now | No |

The three "unaudited" rows are the next read.

## 6. What is still missing versus the requirement

TDrive contains and cleans up its temp files, which is more than most
applications do. What it does **not** have:

- **Accounting.** No per-class byte ledger, so "how much has this written
  to my SSD" has no answer. The Python `core/journal.py` `WriteLedger`
  exists for exactly this and needs porting.
- **Fail-closed targeting.** No refusal to write where durability cannot
  be promised, and no guarantee it never falls back to the system drive —
  as §4 shows, one path already does.

Those are Goal 2. This document is the map for it.

---

## Reading programme

Audited so far: `datadir`, `daemon/state`, `cmd/tdrive/daemon_process`,
`services/file/import`, `services/file/upload` (partial),
`services/encryption` (partial).

Next, in order of how much they can write:

1. `services/file/upload.go` in full — part staging, retry, resume.
2. `backend/photobackup/` — always encrypts, so it always stages; it is
   the heaviest writer in the product.
3. `backend/mountdav/` — `tdrive-mountdav-put-*` staging.
4. `services/media/` or wherever renditions are produced.
5. `frontend/` import dialog — the encrypt toggle question from §1.
