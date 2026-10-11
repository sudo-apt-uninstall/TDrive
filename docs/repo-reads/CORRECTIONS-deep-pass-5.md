# Deep pass 5 — a security finding, and the FR-008 model

---

## 1. noob-mnar/TGDrive — `pickle.load()` on a file fetched from Telegram

`utils/directoryHandler.py`, in `loadDriveData()`:

```python
msg = await client.get_messages(config.STORAGE_CHANNEL, config.DATABASE_BACKUP_MSG_ID)
if msg.document.file_name == "drive.data":
    dl_path = await msg.download()
    with open(dl_path, "rb") as f:
        DRIVE_DATA = pickle.load(f)
```

**Python's `pickle` executes arbitrary code during deserialization.**
That is not a bug in pickle; it is documented behaviour. The payload
here is downloaded from a **mutable remote message, addressed by ID**,
on every application start.

Anyone able to edit that message — a compromised session, a leaked bot
token, any channel admin — achieves **code execution on every client
that starts up**. The only validation is a filename check
(`msg.document.file_name == "drive.data"`), which the attacker
controls.

The matching write:

```python
def save(self) -> None:
    with open(drive_cache_path, "wb") as f:
        pickle.dump(self, f)
```

**For us:** the index must be rebuildable from a format that cannot
execute — JSON, protobuf, or the existing op-log text headers. This is
now a conformance rule: *no deserialiser that can execute code may be
pointed at remote bytes.* TDrive's `projection.Format`/parse of plain
text op headers is immune by construction; this is a concrete
advantage of that design that I had not previously articulated.

### Two corrections to my own scoring

**FR-008 is a PASS, not "partial".** I was wrong. `backup_drive_data()`
uploads the index into the storage channel and pins it:

```python
msg = await client.edit_message_media(
    config.STORAGE_CHANNEL,
    config.DATABASE_BACKUP_MSG_ID,
    media=InputMediaDocument(drive_cache_path, caption=...),
    file_name="drive.data",
)
...
await msg.pin()
```

The namespace survives loss of the local machine. That is a genuine
pass, achieved by exactly the mechanism we want.

**And a disk-write defect I missed entirely.** `save()` pickles the
**whole drive tree** on *every* mutation:

| Operation | Calls `save()` |
|---|---|
| `new_file` | ✔ |
| `new_folder` | ✔ |
| `rename_file_folder` | ✔ |
| `trash_file_folder` | ✔ |
| `delete_file_folder` | ✔ |
| `get_folder_auth` | ✔ |
| `set_folder` (**UI navigation**) | ✔ |

A drive with 100,000 entries rewrites its entire index to disk because
you renamed one file — or merely *navigated into a folder*. On an SSD,
under the owner's non-negotiable requirement, that is a worse pattern
than any single large staging write, because it is unbounded in
*frequency* rather than size.

Cadence is weak too: `DATABASE_BACKUP_TIME` is 24 h and the loop
`continue`s when `isUpdated` is false — so up to a day of metadata can
be lost, and a crash between `save()` and the next backup loses
everything since the last upload.

## 2. herliansyah `backup.go` — the FR-008 model, and the contrast

The same problem, solved properly:

```go
msgID, _, _, _, err := m.UploadFromReader(ctx, f, fileInfo.Size(), fileName, "application/gzip", limiter, nil)
```

**The snapshot goes up through the same streaming uploader as
everything else** — gzipped SQLite, no special path, no whole-file
buffer. Then pinned, then:

```go
// Auto-prune older snapshots to keep the newest 5
_ = m.PruneSnapshots(ctx, 5)
```

Discovery is by filename convention parsed out of the document
attributes:

```go
if strings.HasPrefix(fn.FileName, "teledrive-backup-") && strings.HasSuffix(fn.FileName, ".db.gz") {
```

sorted newest-first, with `IsPinned` carried through. **Retention,
listing, restore-by-id, restore-latest, and delete are all present.**
This is the most complete index-recovery story in all nineteen repos,
and it is ~250 lines.

### The flaw, and it is one I have already fixed elsewhere

```go
tempGz := filepath.Join(os.TempDir(), fmt.Sprintf("restored-%d.db.gz", messageID))
defer os.Remove(tempGz)
```

**`os.TempDir()`, not the application's own data directory.** Same
portable-mode bypass class as TDrive's `RunImport`
`os.MkdirTemp("")` — which I identified as TDrive's largest remaining
violation. Two independent Go projects, same mistake, same cause:
*restore and archive paths get written after the path discipline was
established, and nobody re-checks them.*

**That makes it a pattern, and it argues for the AST chokepoint guard
I already wrote** (`backend/datadir/chokepoint_test.go`) to be
extended to ban `os.TempDir` and `os.MkdirTemp("")` as well as
`UserConfigDir`/`UserCacheDir`.

Note also `DownloadSnapshotByID(ctx, messageID, w io.Writer)` — the
streaming variant exists; `RestoreSnapshotByID` just doesn't use it,
because `db.RestoreFromGzip` wants a path.

## 3. herliansyah `limiter.go` — small, correct, worth copying

```go
pacingDelay: 30 * time.Millisecond
```
```go
if d, ok := tgerr.AsFloodWait(err); ok {
    cooldown := d + 5*time.Second
```

- **Single-worker upload mutex** — `AcquireUpload()` returns the
  unlock func, so Safe Mode is one upload at a time, enforced
  structurally.
- **30 ms pacing** between parts, cancellable.
- **Flood-wait cooldown = reported duration + 5 s.** A deliberate
  safety margin. anasty17 uses `× 1.3`; this uses `+ 5s`. Both add
  headroom; neither trusts the server's number exactly.
- **Max 3 retries**, then a distinct terminal error.
- Every wait is a `select` on `ctx.Done()` — **cancellation is never
  blocked**, the exact failure GopalSaraf has.

---

## Corrections ledger — now 13 changed rows

| Repo | Published | Corrected |
|---|---|---|
| Ns-Bots | DB-only, FR-008 fail | Server-side copy; no file DB |
| GopalSaraf | n/a (leech) | Irreversible split; **blocks event loop; deletes on failure** |
| CyDrive | ≈2× | ≈3×; deletes source in `finally` |
| MondalLabs | hierarchy unexamined | Best hierarchy; `copy_folder` recursion bug |
| ali-abdollahzadeh | ZIP write | **Saved Messages — disqualified** |
| sachinmandawi | hashes every upload | Fallback path only; **Saved Messages — disqualified** |
| TDrive | full ciphertext staging | **Multipart streams; `mountdav` streams** |
| **noob-mnar** | 1× write, partial FR-008 | **FR-008 PASS; `pickle` RCE; whole-index rewrite per mutation** |

### Conformance rules earned so far

1. Round-trip must assert **byte-exact** equality, on **every splitter
   branch**.
2. Source deletion must be **structurally impossible** off the success
   path (3 instances).
3. Flood-wait handling must be **proven non-blocking** under
   concurrency.
4. **No code-executing deserialiser may be pointed at remote bytes.**
5. **Index writes must be incremental**, never whole-tree-per-mutation.
6. Extend the AST chokepoint guard to **`os.TempDir` and
   `os.MkdirTemp("")`** — two Go projects, same bypass.
7. Junk-path short-circuit (`fakeJunkWriteSuccess`).

### Remaining unread

`sachinmandawi/telegramBrowser.ts` · `CyDrive/webdav_server.py`,
`watcher.py`, `cache_manager.py` · `herliansyah` `downloader.go`,
`client.go`, `auth.go`, `session.go`, `app/`, `db/`, `crypto/`, `web/`
· `mxvsh` upload path · `mateof/FileService.cs` · `Mahmud0808`
`StreamCrypto.kt`, `PartDownloader`, `SyncRepositoryImpl`,
`BackupRepositoryImpl` · `caamer20/resumable_upload.rs` · `anasty17`
beyond one file · `Ns-Bots/commands.py` · `Sam8r` `manager.js`,
`db.js` · `rheyoloe` routes/db · `ilhambintang17` beyond upload ·
TDrive `service.go`, `mutations.go`, `hidden_upload.go`,
`resumable_*.go`, `photobackup/engine.go`, `rendition.go`,
`mountdav/locks.go`, `mountdav/resume.go`, `archive.go`, `limits.go`
