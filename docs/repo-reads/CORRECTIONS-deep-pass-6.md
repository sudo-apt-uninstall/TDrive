# Deep pass 6 — 2026-10-11

Four reads completed, all of them files I had previously made published claims
about **without opening them**. One claim is retracted for the second time.

Files read in full this pass:

| Repo | File | Bytes | Blob |
|---|---|---|---|
| sachinmandawi/Telegram-Drive | `app/src/telegramBrowser.ts` | 165,680 | `ea338af4b9571a4d67a1a1728c740bb43ed7ca68` |
| thecynetx/CyDrive | `cydrive/webdav_server.py` | 14,132 | `cfc033e433e9869e393d147e67f6623bb11ab3ba` |
| mateof/TelegramFileManager | `TelegramDownloader/Data/FileService.cs` | 120,873 | `3395982e9ef935c21fac57fc75a89cec40fa3ffe` |
| Mahmud0808/TeleDrive | `core/crypto/StreamCrypto.kt` | — | `61027bcd6b4cbb455ad0e2de5c8e243444dac899` |

---

## 1. 🔴🔴 RETRACTION OF A RETRACTION — sachinmandawi `sha256Blob`

In `CORRECTIONS-deep-pass-2.md` I wrote that I had been wrong to claim
`sha256Blob` (whole file → `ArrayBuffer`) runs on every upload, and said it was
"behind the `isSavedMessagesDefaultStorage()` early return, so it only runs on
the browser/IndexedDB fallback that never contacts Telegram."

**That correction was itself wrong. The original claim was right.**

`telegramUploadFile` (line 671) in `telegramBrowser.ts`:

```ts
export async function telegramUploadFile(file: File, folderId, onProgress, conflictStrategy = 'version') {
    const client = await authorizedTelegramClient();     // ← real MTProto client
    ...
    const checksum = await sha256Blob(file);             // ← line 700, UNCONDITIONAL
    ...
    const message = await sendTelegramFileWithRetry(client, 'me', { file: uploadFile, ... });
```

and `sha256Blob` is:

```ts
async function sha256Blob(blob: Blob): Promise<string> {
    const digest = await crypto.subtle.digest('SHA-256', await blob.arrayBuffer());
```

There is **no early return between the authorized client and the digest**. The
function obtains a live Telegram client, then materialises the entire file in
RAM to checksum it, then uploads. **Every Telegram upload holds the whole
payload in memory.** The IndexedDB fallback is a different function entirely.

**Why I got it wrong twice:** I searched for the guard name, found it in the
file, and assumed scope from its presence rather than reading the control flow
of the function that calls the digest. That is the same error as scoring a repo
from one file, applied to one function.

**Method change, binding from here:** a claim about whether code *runs* must be
supported by reading the function top-to-bottom from its entry point to the
line in question — not by a keyword hit elsewhere in the file.

### Confirmed in the same read
- `'me'` is the entity on **every** call: `sendTelegramFileWithRetry(client, 'me', …)`,
  `client.iterMessages('me')`, `client.getMessages('me')`, `deleteMessages('me')`,
  and the manifest write at line 1723. **Saved Messages is not a fallback, it is
  the only store.** The hard disqualifier stands and is now verified at every
  call site, not inferred.
- `telegramDownloadBlob` (1487) → `new Blob([bytes])` + `sha256Blob(blob)`
  verification (1536) ⇒ download holds the payload in RAM at least twice.
- Offline cache ceiling `OFFLINE_CACHE_MAX_BYTES = 512 * 1024 * 1024`, evicting
  oldest first (line 4052).

### Worth stealing anyway
`sendBrowserFileWithRetry` separates `client.uploadFile()` (bytes) from
`client.sendFile()` (message), so a retry of the **message** never re-sends the
**bytes** — the file handle is reused. That is the correct split, and it is the
retry structure TDrive's `retryVisibleSend` lacks: TDrive rewinds the whole
body because its retry unit is the entire send.
Flood handling: `(floodSeconds + 1) * 1000` capped at 60 s; otherwise
`min(1500 * 2**attempt, 12_000)`; a global `nextTelegramUploadAt` gate so
concurrent uploads serialise. `parseTelegramFloodWaitSeconds` matches three
regex shapes because the error text is not stable — pragmatic, and a warning
that string-matching flood waits is fragile.

---

## 2. 🔴 CyDrive WebDAV — two severe findings

### 2a. The WebDAV server is unauthenticated by construction

```python
config = {
    "host": self.host, "port": self.port,
    "provider_mapping": {...},
    "simple_dc": {"user_mapping": {"*": True}},
    "enable_cors": True,
}
```

`{"*": True}` in wsgidav means **anonymous access, all realms, read and write**.
Combined with `enable_cors: True` and a host taken from config (the banner calls
`get_display_ip(self.host)`, which exists precisely to print a LAN address),
anyone who can reach the port can list, read, overwrite and delete the entire
drive — and the delete path removes the DB row, so it is not recoverable from
the UI either.

This is the second security finding of the audit, after noob-mnar's
`pickle.load()` RCE. Different class: that one is remote code execution, this
one is a missing authN boundary on a network service that is started by default
from the CLI.

**Rule earned (8): any network listener we ship binds loopback by default and
carries a credential; "convenient on the LAN" is not a default.**

### 2b. A failed download is served as a successful empty file

```python
def get_content(self):
    ...
    try:
        success = future.result(timeout=180)
        if success and os.path.exists(local_cached):
            return open(local_cached, "rb")
    except Exception as e:
        print(f"⚠️ [WebDAV Hydration Error] {e}")

    # Return empty stream fallback
    return io.BytesIO(b"")
```

Every failure mode — Telegram error, loop not running, missing message, and
notably the **hard 180-second timeout** — returns an empty body. Meanwhile
`get_content_length()` still reports the record's real size, so the client is
told "this file is 4 GB" and handed zero bytes with a 200. Windows Explorer and
Finder will write a 0-byte file and report success.

The 180 s wall is an undeclared size ceiling: at 10 MB/s it caps hydration at
roughly 1.8 GB, and on a slower link far lower — silently, per file, with no
error surfaced anywhere but stdout.

**Rule earned (9): a read path may never substitute empty content for an
error. If bytes cannot be produced, the transport must fail loudly.** Our
`downloader.py` must have a test that asserts an exception — not an empty
body — on every hydration failure branch.

### 2c. WebDAV write = a third full copy

`begin_write` returns `open(local_cached, "wb")` — wsgidav pipes the PUT body
straight into the cache file — then `end_write` fires
`telegram_engine.upload_file(local_cached, clean_path)`, which (deep pass 2)
encrypts to a `tempfile.mkstemp` ciphertext and *then* chunks that. So a WebDAV
PUT of an N-byte file touches disk for **cache copy + full ciphertext + every
part**, all coexisting, under a banner that prints
`[Zero-Disk Storage] 0 Bytes used on your hard drive`.

### 2d. Folder delete orphans the subtree

```python
def handle_delete(self):   # VirtualTelegramFolder
    rel_path = "/" + self.path.strip("/").replace("\\", "/")
    self.db.delete_file(rel_path)
    return True
```

One row. No descendant removal, no Telegram deletion. Every child keeps a
`parent_dir` pointing at a directory that no longer exists: invisible in the
mount, still counted by `get_stats()`, still occupying Telegram. Contrast
MondalLabs' `$graphLookup` subtree delete — still the best hierarchy code in
the inventory.

### 2e. Smaller, still real
- `get_available_bytes()` returns a hardcoded **10 TB**.
- `create_empty_resource` inserts a row before any byte is written, and
  `end_write` *returns early* on `file_size == 0` ("Skip initial 0-byte touch by
  Windows Explorer") — so an abandoned Explorer write leaves a permanent phantom
  row with `is_uploaded=False` that nothing ever reaps.
- `evict_lru(size)` runs **before** the download, so a hydration that then fails
  has already evicted other files to make room for nothing.
- `support_ranges() -> True` on files, but the first ranged byte forces a full
  materialisation to cache; there is no range-aware fetch.
- `get_resource_inst` carefully builds `env` with the provider defaulted in,
  then passes the original `environ` on both non-root returns — the fix-up is
  computed and discarded. (Same shape as Mahmud0808's discarded content hash.)

---

## 3. mateof `FileService.cs` — read in full (120,873 B)

### 3a. Correction to my own scorecard: mateof is accidentally **portable**

Every storage root is derived from `Directory.GetCurrentDirectory()`:

```csharp
IMGDIR        = Path.Combine(Directory.GetCurrentDirectory(), "wwwroot", "img", "telegram");
LOCALDIR      = Path.Combine(Directory.GetCurrentDirectory(), "local");
TEMPDIR       = Path.Combine(Directory.GetCurrentDirectory(), "local", "temp");
TEMPORARYPDIR = Path.Combine(Directory.GetCurrentDirectory(), "local", "temp", "_temp");
```

No `%AppData%`, no `Path.GetTempPath()`, no `os.TempDir()` equivalent anywhere
in the file. I had filed mateof under "many uncoordinated storage roots" (anti
pattern 6) — that remains true, there are five of them with no global budget —
but it is the **only project in the inventory whose paths are already
portable**, which TDrive and herliansyah both are not. The defect is the
absence of a budget, not the location.

### 3b. The worst single line in the file

```csharp
await mergeFileStreamAsync(splitPaths, Path.Combine(currentFilePath, fileName));
foreach (string filePath in splitPaths) File.Delete(filePath);
using (FileStream ff = new FileStream(.., FileMode.Open, FileAccess.Read, FileShare.Read))
    ms = await ToMemoryStreamAsync(ff);          // ← ENTIRE merged file into RAM
File.Delete(Path.Combine(currentFilePath, fileName));
return ms;
```

That is `getImage` (line 640). For a **split** file — i.e. one larger than the
2 GB single-message limit — it downloads every part to disk, merges them into a
second full copy on disk, then reads the whole merged object into a
`MemoryStream` before returning. Peak = all parts + merged copy on disk + the
entire file in managed memory. For anything that was split, this is an
`OutOfMemoryException` by construction.

`ToMemoryStreamAsync` is just `stream.CopyToAsync(new MemoryStream())` — and at
line 2242 it is called as `ToMemoryStreamAsync(new MemoryStream(buffer))`,
copying a MemoryStream into another MemoryStream: 2× RAM for no effect.

### 3c. Verified constants
- `TelegramService.splitSizeGB = 2`, overridden by `GeneralConfigStatic.config.SplitSize`.
  Split threshold is `(long)MaxSize * (long)TelegramService.splitSizeGB` and part
  size `(long)splitSize * _gb` — both casts are applied **before** the multiply,
  so there is no 32-bit overflow here. (Checked specifically; this is where I
  expected the GiB/GB trap and it is not present.)
- `FILESPLITSIZE = 524288 // 512 * 1024` — the same 512 KiB protocol part size
  as every other project.
- `MemorySplitSizeGB` is clamped server-side to `SplitSize` and to the account
  limit (4 GB Premium / 2 GB otherwise) in `GeneralConfig.SaveChanges` and
  `ConfigController` — correct, and still gated behind
  `EnableMemorySplitUpload = false` by default. The deep-pass-1 finding stands:
  *the fix existing in the repo is not the fix being in effect.*
- Integrity is **MD5** (`GetMd5HashFromFile`), streamed correctly over a
  `FileStream`, but MD5.
- `SaveToFile` does `file.OpenReadStream(long.MaxValue).CopyToAsync(fs)` —
  unbounded; the Blazor upload limit is explicitly disabled.
- `throw ex;` (line 681) resets the stack trace; should be `throw;`.

---

## 4. Mahmud0808 `StreamCrypto.kt` — the best crypto in the inventory, and it
   refutes a constraint I accepted from TDrive

Format: `MAGIC(4) VERSION(1) SALT(8)`, then per chunk a 4-byte length whose top
bit is the final-chunk flag, then the sealed bytes. AES-GCM, 128-bit tag,
1 MiB chunks, nonce = `salt(8) || chunkIndex(4)`, and **AAD binds
`(VERSION, chunkIndex, isFinal)`** so chunks cannot be dropped, reordered or
truncated. `decryptStream` ends with `if (input.read() != -1) throw` — trailing
data after the final chunk is rejected. Two-buffer lookahead (`current`/`next`,
swapped) so "is this the last chunk" is known before sealing it, with no extra
copy. This is the only authenticated, truncation-resistant streaming format in
the whole set.

### 4a. The magic bytes are `0x54 0x44 0x45 0x31` = **"TDE1"**

TDrive's `put_body_buffer.go` comment — which I quoted in deep pass 3 as
governing — says *"TDE1's encrypted stream header embeds the plaintext size and
cannot be back-filled after streaming starts, so encrypted writes hard-require a
known length."*

Mahmud0808's **TDE1 header embeds no size at all**: it is 13 bytes of magic,
version and salt. Two unrelated projects ship different formats under the same
four-byte magic — an interop hazard if anyone ever points one decryptor at the
other's file, since both will pass the magic check and then misparse.

### 4b. The constraint is self-inflicted, and there is an exact fix

```kotlin
fun storedSize(plainSize: Long): Long {
    if (plainSize <= 0) return headerSize().toLong() + frameStoredSize(0)
    val whole = plainSize / CHUNK_SIZE
    val remainder = (plainSize % CHUNK_SIZE).toInt()
    var total = headerSize().toLong() + whole * frameStoredSize(CHUNK_SIZE)
    if (remainder > 0) total += frameStoredSize(remainder)
    return total
}
```

The ciphertext length is a **pure arithmetic function of the plaintext
length**. Telegram needs the total size before the first part; it does not need
the bytes. So wherever the plaintext size is known, the ciphertext size is known
too, and no ciphertext ever has to be materialised to measure it.

Applied to TDrive:
- `writeCiphertextTemp` on the **single-part encrypted path** — plaintext size is
  known, so `storedSize()`-style arithmetic plus re-encrypt-on-retry removes the
  temp file entirely. This upgrades my deep-pass-3 item 3 from "small
  refinement" to "a complete removal with a worked precedent".
- The **chunked-PUT buffer** stands. There the plaintext size is genuinely
  unknown (macOS Finder sends no `Content-Length`), and no arithmetic can
  recover it. That exception remains legitimate.

`frameStart(i) = headerSize() + i * (LENGTH_BYTES + CHUNK_SIZE + TAG_BYTES)`
plus `decryptFrame` gives **O(1) seek into ciphertext** — fixed-size frames
chosen deliberately so a plaintext offset maps to a byte offset by arithmetic.
That is how encrypted media streaming should work, and nothing else in the set
attempts it.

### 4c. One real weakness
The GCM nonce is `salt(8) || chunkIndex(4)` with an **8-byte (64-bit) random
salt per file**. GCM is catastrophically broken by nonce reuse under the same
key. If the key is per-file, this is fine. If the same key covers many files,
uniqueness rests on 64 bits of salt — birthday collision around 2³² files,
versus the 96-bit random nonce NIST assumes. `encryptBytes` does use a fresh
12-byte random nonce, so the author knows the distinction; the streaming path
traded it for seekability.

**Our rule: if we adopt this frame layout, the salt must be 12 bytes with the
counter in separate AAD, or the key must be provably per-file.**

---

## Conformance rules — cumulative (8 and 9 are new)
1. Round-trip = byte-exact, on every splitter branch.
2. Source deletion structurally impossible off the success path.
3. Flood-wait proven non-blocking under concurrency.
4. No code-executing deserialiser on remote bytes.
5. Index writes incremental, never whole-tree-per-mutation.
6. AST chokepoint guard bans `os.TempDir` / `os.MkdirTemp("")`.
7. Junk-path short-circuit (`.DS_Store` → success, body to `io.Discard`).
8. **Network listeners bind loopback and carry a credential by default.**
9. **A read path never substitutes empty content for an error.**
10. **A claim that code "runs" or "doesn't run" requires reading the call path
    from entry point to the line — not a keyword hit in the same file.**

## Still unread
- TDrive backend: `service.go` (25 K), `mutations.go` (21 K), `hidden_upload.go`
  (19 K), `resumable_*.go` (~60 K), `photobackup/engine.go`, `mountdav/locks.go`
  (19 K) + `writes.go` (18 K) + `server.go` (12 K)
- noob-mnar `directoryHandler.py`; herliansyah `downloader.go` + `app/db/crypto`;
  CyDrive `watcher.py`, `cache_manager.py`, `database.py`; Mahmud0808
  `PartDownloader.kt`, `SyncRepositoryImpl.kt` (23 K); mxvsh upload path;
  anasty17 (~100 files); caamer20 `resumable_upload.rs` not re-read at HEAD
