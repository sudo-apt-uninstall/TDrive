# Deep pass 4 — two exposed claims resolved

Both of the claims I flagged as most exposed have now been checked
against the files that back them. **One clears TDrive; one makes
GopalSaraf worse.**

---

## 1. TDrive `mountdav/writes.go` — my concern was unfounded

I had listed WebDAV staging as an open disk-write risk, having read
only `put_body_buffer.go` (1 of 33 files). The actual request path:

```go
body := request.Body
contentLength := request.ContentLength
if contentLength < 0 {
    // ... buffer only when the client sent no Content-Length
}
...
result, err := application.writer.Put(request.Context(), PutRequest{
    OperationID:   operationID,
    Path:          path,
    ContentLength: contentLength,
    ...
}, body)
```

> **The normal PUT passes the live `request.Body` straight through to
> the write coordinator. Nothing is staged.**

There are exactly **two** staging exceptions, both named
client-compatibility workarounds:

| Path | Trigger | Bound |
|---|---|---|
| `bufferUnknownLengthPUTBody` | chunked Transfer-Encoding, no `Content-Length` — **macOS Finder's copy engine** | `8 << 30`, `datadir`-scoped, always removed |
| `servePutResume` | `Content-Range` chunks — macOS resuming an interrupted upload | non-final chunks only; final chunk commits via the **same** `writer.Put` |

The resume design is notably careful:

> *"the final chunk assembles the complete content and commits it
> through the exact same `application.writer.Put` path a normal PUT
> uses, so mountwrite/mountadapter need no awareness this chunking
> happened."*

One commit path, one set of invariants, regardless of how the client
chose to deliver the bytes.

### Worth stealing: junk-path short-circuit

```go
// fakeJunkWriteSuccess answers a mutation targeting macOS's own metadata
// with success without staging, coordinating, locking, or storing anything
// for it. It drains any request body first ... so a client mid-upload sees
// the success response it expects rather than a connection reset.
```

`.DS_Store` and friends are acknowledged and thrown away — body
drained to `io.Discard`. This saves a disk write, a Telegram message,
*and* keeps the drive free of macOS droppings. Applied to PUT, MKCOL
and DELETE alike.

**Net: `mountdav` is removed from the disk-write risk list.** The
error taxonomy is also unusually complete — `507 Insufficient
Storage`, `423 Locked`, `412 Precondition Failed`, `Retry-After` on
503 — which matters because a WebDAV client's retry behaviour is
driven entirely by these codes.

## 2. GopalSaraf `upload_to_tg.py` — three findings, all bad

I characterised this repo from `split_large_files.py` alone. The
27 KB uploader was unread. It is worse than the splitter suggested.

### Blocking sleep inside async — freezes the whole bot

```python
except FloodWait as g:
    LOGGER.info(g)
    time.sleep(g.x)
```

`time.sleep`, not `await asyncio.sleep`. Inside an `async def`, on a
pyrogram bot. **This blocks the entire event loop** for the full
flood-wait — which Telegram routinely sets to tens or hundreds of
seconds. Every concurrent transfer, every progress update, every
other user's command stops dead.

It appears twice more in the same file, both in cleanup paths.

Compare anasty17, which handles the identical exception correctly:

```python
await sleep(f.value * 1.3)
```

**Same library, same exception, same year — one awaits, one blocks.**
Worth recording as a conformance item: our flood-wait handling must be
proven non-blocking under concurrent transfers, not merely present.

### Deletes the source on the failure path — third instance

```python
        except Exception as e:
            LOGGER.info(e)
            await message_for_progress_display.edit_text("**FAILED**\n" + str(e))
        else:
            ...
        os.remove(local_file_name)
    return sent_message
```

`os.remove` is at **function level**, after the whole
`try/except/else`. So the sequence on failure is: exception caught →
user told **"FAILED"** → **file deleted anyway**.

That is the third independent instance of this exact defect:

| Repo | Mechanism |
|---|---|
| CyDrive | `os.remove` in a `finally` |
| Mahmud0808 | hash guard short-circuits above 512 MiB |
| **GopalSaraf** | `os.remove` after `try/except/else` |

**Three of nineteen delete the user's source on a path where the
upload did not succeed.** This is no longer a curiosity; it is the
single most common serious defect in the field.

### The author flags their own recursion

```python
            # recursion: will this FAIL somewhere?
            await upload_to_tg(
```

Twice, on both recursive paths — directory walk and split-parts walk.
And `split_large_files` has already executed:

```python
    try:
        os.remove(input_file)
    except Exception as r:
        LOGGER.error(r)
```

**The original is deleted before the parts are uploaded.** So a
failure part-way through a split upload loses the parts *and* the
source, and the irreversible 3-second-overlap splitting means even a
successful upload cannot reconstruct the original.

---

## Running tally

**Fully read this pass:** TDrive `writes.go`, `put_body_buffer.go`,
`upload.go`, `upload_single.go`, `rendition_prepare.go`,
`ImportOptionsModal.svelte`; GopalSaraf `upload_to_tg.py`,
`split_large_files.py`.

**Corrections to date: 11 published rows changed.**

### New conformance items earned here

1. **Flood-wait handling must be proven non-blocking** under
   concurrent transfers — not merely present. GopalSaraf has a handler
   and it freezes the process.
2. **Source deletion must be structurally impossible off the success
   path.** Three independent instances. The guard must be a required
   argument carrying a verified hash, not a statement placed after a
   `try` block.
3. **Junk-path short-circuit** (`.DS_Store`, `._*`, `Thumbs.db`) —
   adopt TDrive's `fakeJunkWriteSuccess` pattern wholesale.

### Still unread

- `sachinmandawi/telegramBrowser.ts` — the real Telegram layer; I have
  already retracted one claim about this repo.
- `noob-mnar/directoryHandler.py` (11 KB) — its hierarchy.
- `CyDrive/webdav_server.py` (14 KB), `watcher.py`, `cache_manager.py`.
- `herliansyah` `backup.go`, `limiter.go`, `downloader.go`, `app/`,
  `db/`, `crypto/`.
- `mxvsh` upload path — still not located.
- `mateof/FileService.cs` in full.
- `Mahmud0808` `StreamCrypto.kt`, `PartDownloader`, `SyncRepositoryImpl`.
- `caamer20/resumable_upload.rs` at current HEAD.
- `anasty17` beyond `telegram_uploader.py`.
- TDrive: `service.go` (25 K), `mutations.go` (21 K),
  `hidden_upload.go` (19 K), `resumable_*.go` (~60 K),
  `photobackup/engine.go`, `rendition.go`, `mountdav/locks.go` (19 K),
  `mountdav/resume.go` (13 K), `archive.go`, `limits.go`.
