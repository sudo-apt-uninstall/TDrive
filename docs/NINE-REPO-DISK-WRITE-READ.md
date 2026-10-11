# All nine repos, read first-hand, on one question

> **Upload a folder. How many bytes hit local disk before anything
> reaches Telegram?**

The previous cross-repo document answered this from a memory summary of
an earlier audit. This one is from reading the code. **Three entries
changed**, and one of them is the best result in the set.

Read at the HEAD SHAs recorded in the audit of 2026-10-09/10.

---

## Scorecard, corrected

| Repo | Bytes to disk | Status |
|---|---|---|
| **MondalLabs** | **0 — server-side copy** | **Corrected ↑** (was "unverified") |
| **jeetrex17/TDrive** | **0** unencrypted; 1× if encrypted, in its own cache | Confirmed |
| caamer20 | 0 — streaming encrypt reader | Confirmed |
| **mateof** | **1× (default) / 0 (opt-in)** | **Corrected ↑** (was "catastrophic") |
| sachinmandawi | 0 to disk; whole file to **RAM** | From prior audit |
| **ali-abdollahzadeh** | ≈1× whole folder, **cleanup confirmed** | **Partly corrected** |
| ilhambintang17 | **≈2× payload** | Confirmed, re-verified |
| sauravsultane | whole file to RAM; also broken | From prior audit |
| Mahmud0808 | **Genuinely unverified** | Honest blank |

---

## Correction 1 — MondalLabs has the best answer in the set

My scorecard said "Unverified". `services/file_service.py` opens with
this, and it is the most disciplined comment in the entire catalogue:

```python
#   • Safeguard #1: route_to_cdn uses copy_message() — NEVER download_media().
#     This is a server-side copy on Telegram's infrastructure; the bot
#     process touches ZERO bytes of the file payload, preventing OOM on Render.
```

And the implementation does exactly that:

```python
copied_msg = await client.copy_message(
    chat_id=dump_chat_id,
    from_chat_id=message.chat.id,
    message_id=message.id,
)
```

**Zero bytes to disk. Zero bytes to RAM.** Telegram moves the data
between its own chats; the application only records the resulting
`file_id`.

### Why it does not simply win

The interaction model is different. The user **sends the file to the bot
inside Telegram**, and the bot files it. The payload is already on
Telegram's servers before the application is involved. You cannot upload
a local folder this way.

So it does not solve our problem — but it answers a question nobody in
this project has asked:

> **When TDrive copies or moves a file between drives, does it
> download and re-upload, or does it ask Telegram to copy server-side?**

If it downloads and re-uploads, that is a full round trip of the user's
data through their disk and network for an operation Telegram will
perform for free. **This is now the highest-value unread question in the
fork**, and it came from a repo I had written off.

MondalLabs still fails FR-008 — the index is MongoDB-only, so losing the
database loses the namespace even though the bytes survive in the dump
group.

## Correction 2 — mateof built the right thing and shipped it off

My scorecard said *"≈1× whole folder + many uncoordinated roots |
Catastrophic for folders."* The second half stands. **The first half is
wrong.**

`TelegramDownloader/Services/Streams/FileChunkStream.cs`:

```csharp
/// A read-only stream that provides access to a specific chunk of a file.
/// This allows uploading large files in memory-efficient chunks without
/// creating temporary split files on disk.
```

It is a windowed `Stream` over the source: seeks to `chunkStart`, reports
`Length = chunkLength`, clamps every `Read` to the window, and refuses
`Write` and `SetLength`. **This is the correct pattern**, in C#, and it
is the same idea as caamer20's reader in Rust.

But `FileService.cs` contains both paths:

```csharp
// Disk-based splitting - create temp split files
List<string> filesSplit = await splitFileStreamAsync(folderPath, file.Name, MaxSize);
```

gated on config — and `GeneralConfig.cs` says:

```csharp
public bool EnableMemorySplitUpload { get; set; } = false;
```

**The default is disk-based splitting.** A user who never opens the
config page gets the temp-file path. The right code exists, is written,
is wired up, and is off.

That is worth recording as a pattern in its own right: *the fix existing
in the repository is not the same as the fix being in effect.* It is also
mild encouragement — two independent projects reached the same windowed-
stream design, so it is not exotic.

Two further details from `GeneralConfig.cs`, both good:

- `StreamingMode.DirectStream = 0` — *"nothing is stored on disk"* — is
  the effective default for exported STRM playback.
- `MemorySplitSizeGB` is clamped server-side to the account's real
  Telegram ceiling (4 GB Premium, 2 GB otherwise), which is the
  runtime-limits discipline most of this field lacks.

## Correction 3 — ali-abdollahzadeh cleans up after itself

The folder-as-ZIP write is real and unavoidable in that design — it is
the *only* folder path, named in the UI as `Upload Folder (.zip)` with
subtitle *"Automatically compress a folder into .zip"*.

But my note said success-path cleanup was unverified. It is not:

```dart
/// Cleans up any stale temporary zip uploads in cache.
Future<void> cleanTempFolderUploads({Duration maxAge = const Duration(hours: 6)}) async {
```

The ZIP goes to `getTemporaryDirectory()/folder_uploads` and stale
entries are swept after six hours. Still ≈1× the folder written before a
byte is sent, which for this requirement is disqualifying — but it is
housekeeping, not abandonment.

## Confirmed unchanged — ilhambintang17 writes twice

`bot/server/cloud_routes.py`, verified directly:

```python
chunk_dir = Path(f"/tmp/teledrive_upload_{upload_id}")
...
"""Background task to merge chunks and send to Telegram, with auto-cleanup."""
merged_path = f"/tmp/teledrive_merged_{upload_id}_{filename}"
```

Chunks to `/tmp`, then **merged into a second complete file**, peaking at
roughly twice the payload. The purest violation in the set, unchanged.

## Honest blank — Mahmud0808

TDLib-based, and TDLib manages its own cache directory. Whether an upload
streams from the source path or is first copied into TDLib's store is
**not established**, and `TransferRepositoryImpl.kt` did not settle it.

The prior audit's findings about this repo stand on their own merits and
are not disk-write claims: `TelegramPacer` treats flood-wait as
account-global (correct, and we match it), `TelegramLimits.kt` names
`FILE_PARTS_INVALID`, and it splits at 512 MiB rather than 2 GB.

Listing it as unverified rather than giving it a row that implies a
finding is the whole point of redoing this.

---

## What changed as a result

1. **A new question for the fork, from MondalLabs:** does TDrive use
   server-side copy for cross-drive copy/move? Telegram will do it for
   free. Highest-value unread item.
2. **Two independent proofs of the streaming pattern** — caamer20 in
   Rust, mateof in C#. The streaming encrypting reader proposed in
   `DISK-WRITE-AUDIT.md` §3 is ordinary engineering, not research.
3. **A caution worth keeping:** mateof shows that the correct
   implementation can sit in the repository, complete, behind a default
   that nobody changes. When we add streaming encryption, **it must be
   the default**, not a setting.

## Reading log

**Read first-hand this session:** TDrive (`datadir`, `daemon/state`,
`cmd/tdrive/daemon_process`, `services/file/import`,
`services/file/upload`, `services/encryption/service`, photo-backup
forcing across Go + frontend); caamer20
(`crypto/envelope/encrypt_reader.rs` in full, `Docs/TDENC2_FORMAT.md` in
full, supporter/licence docs); mateof (`FileChunkStream.cs` in full,
`GeneralConfig.cs` in full, both split paths in `FileService.cs`);
MondalLabs (`services/file_service.py` in full); ilhambintang17
(`cloud_routes.py` upload path); ali-abdollahzadeh
(`folder_compression_service.dart`, `app_text.dart`,
`upload_options_sheet.dart`).

**Not read first-hand this session, carried from the 2026-10-09/10
audit:** sachinmandawi, sauravsultane, Mahmud0808. Their entries above
are labelled accordingly.

**Still unread in TDrive:** `backend/mountdav/` staging, the rendition
pipeline, `photobackup/engine.go` in full (32 KB), and the initial value
of `encrypt` in `ImportOptionsModal.svelte`.
