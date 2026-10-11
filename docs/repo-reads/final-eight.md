# The final eight — 19/19 read

Closes `artifacts-4/telegram drive/must-read-repos.md`. Every
repository on the mandatory list has now been opened and read at its
current default branch.

---

## prgofficial/FileToLink — the most technically valuable file in the inventory

`04d6d25429853df16582be9a4337d47114c62095` · Python · Telethon ·
**AGPL-3.0**

`tgfilestream/paralleltransfer.py` solves a problem nobody else in
the catalogue even attempts: **parallel downloads across Telegram's
datacentres.**

```python
dc_id, location = utils.get_input_location(file)
part_size = 512 * 1024
first_part_cut = offset % part_size
first_part = math.floor(offset / part_size)
last_part_cut = part_size - (limit % part_size)
last_part = math.ceil(limit / part_size)
```

Then the download yields exactly the requested bytes:

```python
if part == first_part:
    yield result.bytes[first_part_cut:]
elif part == last_part:
    yield result.bytes[:last_part_cut]
else:
    yield result.bytes
```

**An arbitrary HTTP Range maps onto 512 KB protocol parts with no
over-fetch and no buffering.** Zero disk, zero whole-file RAM.

### The part worth understanding properly

A file lives in a specific datacentre. The logged-in session is
authenticated to *its* DC, not necessarily the file's. So:

```python
auth = await self.client(ExportAuthorizationRequest(self.dc.id))
req = self.client._init_with(ImportAuthorizationRequest(id=auth.id, bytes=auth.bytes))
await conn.sender.send(req)
self.auth_key = conn.sender.auth_key
```

It exports an authorisation from the main session and imports it into
a fresh `MTProtoSender` pointed at the file's DC — **then caches that
auth key per DC**, so subsequent connections to the same DC skip the
handshake. A pool of five `DCConnectionManager`s, each handing out the
least-loaded connection.

And a hard-won operational limit, with its reason stated:

```python
if connection_limit > 25:
    root_log.warning("The connection limit should not be set above 25 to avoid"
                     " infinite disconnect/reconnect loops")
```

> **25 connections per DC is the ceiling**, and exceeding it causes
> reconnect loops rather than a clean error. That is a protocol fact we
> did not have.

### Licence — technique only

**AGPL-3.0**, and the header is Tulir Asokan's original tgfilestream.
TDrive is MIT. **No code may be copied.** The *technique* —
export/import auth to reach a file's own DC, cache the key per DC, cap
at 25 — is a protocol fact, not a creative work, and can be
reimplemented independently. This belongs in the roadmap as a future
throughput item, clearly flagged as clean-room.

## anasty17/mirror-leech-telegram-bot — operational constants

Architecturally irrelevant (it is a leech bot: it downloads to disk,
then uploads, so it is disk-bound by purpose). But it is the most
heavily-exercised code in the catalogue, and its constants are earned:

| Constant | Value | Where |
|---|---|---|
| **Bot→user session switch** | **`2097152000`** bytes = **2000 MiB**, *not* 2 GiB | `hybrid_leech` |
| FloodWait sleep | `f.value * 1.3` | exception handler |
| Retry policy | exponential `multiplier=2, min=4, max=8`, **3 attempts** | `@retry` |
| Filename cap | **60 chars**, archive/`.partN` base preserved | `_prepare_file` |
| Media group batch | **10** | `_send_media_group` |

Two behaviours worth copying:

```python
if f_size == 0:
    LOGGER.error(f"{self._up_path} size is zero, telegram don't upload zero size files")
```

**Zero-byte files are rejected before the attempt** — Telegram refuses
them. Our conformance suite should assert this rather than discover it.

```python
if isinstance(err, BadRequest) and key != "documents":
    LOGGER.error(f"Retrying As Document. Path: {self._up_path}")
    return await self._upload_file(cap_mono, file, o_path, True)
```

A typed send (video/audio/photo) that fails with `BadRequest` is
**retried as a plain document**. Telegram rejects media it cannot
parse; the fallback is to stop claiming a type. Cheap, and it converts
a hard failure into a successful upload.

Note `2000 MiB`, not `2 GiB` — a deliberate margin below the real
ceiling. Worth checking TDrive's own threshold against this.

## Sam8r/TGWebDrive — best range-streaming outside caamer20

`5058246a9a5d34384367a8fe2ed33f1bc9cde63f` · Node · gramjs

```js
// Stream only the requested byte range directly from Telegram (no temp file),
// enabling fast seeking for large videos.
for await (const chunk of client.iterDownload({
    file: msg.media,
    offset: bigInt(start),
    fileSize: bigInt(total),
    requestSize: 524288,
})) {
```

Correct backpressure (`await new Promise(r => res.once("drain", r))`),
abort on client disconnect, and **suffix ranges** (`bytes=-N`) handled:

```js
else if (m[2]) start = Math.max(0, total - parseInt(m[2], 10)); // suffix range (last N bytes)
```

Most implementations get suffix ranges wrong. This one doesn't.

**Weaknesses:** `uploadFile` takes a `filePath` — the web server has
already written the upload to disk. Folders are channels
(`createChannelFolder`), so **exactly two levels**, the same ceiling as
caamer20. Thumbnails round-trip through `/tmp`. Multipart downloads
set `Accept-Ranges: none` — honest, but no seeking in split files.

## rheyoloe/TeleDrive — honest about RAM, still spends it

`fb502c7eb8a7069c7e28d8b6fefff6c23445805b` · TypeScript · gramjs

```ts
const MAX_CACHE_BYTES = 256 * 1024 * 1024; // 256 MB total
const MAX_ITEM_BYTES = 25 * 1024 * 1024;   // only cache files <= 25 MB
```

A **bounded LRU** with a stated rationale (*"TG message contents never
change → safe to cache"*), correct insertion-order eviction, and
`evictCache()` called on delete so a deleted file cannot be served
from cache. That last detail is the kind of thing most caches forget.

But `uploadFile(buffer: Buffer, ...)` and
`downloadFile(): Promise<Buffer>` — **whole file in RAM both
directions.** Disciplined bookkeeping around an unbounded core.

## noob-mnar/TGDrive — 1× write, premium-aware

`88e2590edd6cbcf2246a7eaded19e62258d373aa` · Python · pyrogram

```python
if file_size > 1.98 * 1024 * 1024 * 1024:
    # Use premium client for files larger than 2 GB
    client: Client = get_client(premium_required=True)
```

Selects a Premium session above ~1.98 GiB — a third independent
confirmation that the 2 GB/4 GB split is handled by *account*, not by
retry. The web layer writes the upload to disk first, pyrogram sends
from the path, then:

```python
try:
    os.remove(file_path)
except:
    pass
```

**1× write**, and cleanup that silently swallows every failure — a bare
`except:` catching even `KeyboardInterrupt`. Leaked files accumulate
invisibly.

## mxvsh/drivegram and Ns-Bots/TG-File-Store — FR-008 failures

**mxvsh/drivegram** (`9e689be0…`, Next.js + Drizzle): the entire file
model is a Postgres row holding `fileId`, `chatId`, `messageId`, with
`folderPath` as a **plain string column**. No remote metadata at all —
lose the database, lose the namespace. Hierarchy is string-prefix
matching, not structure.

**Ns-Bots/TG-File-Store**: a file-store bot, not a drive. Bot API
`file_id` in a database, no hierarchy by design.

Both are honest about what they are. Neither is prior art for us.

---

## Final standing — all 19

| # | Repo | Disk on upload | FR-008 |
|---:|---|---|---|
| 1 | **jeetrex17/TDrive** | 0 plain / 1× if encrypted | **pass** |
| 2 | caamer20 | 0 — streaming | fail (keys local-only) |
| 3 | sachinmandawi | 0 disk, **whole file RAM** | **disqualified — Saved Messages** |
| 4 | Mahmud0808 | 1×, **TDLib-forced** | pass |
| 5 | ali-abdollahzadeh | ≈1× folder ZIP | fail |
| 6 | sauravsultane | RAM | **broken — unrecoverable files** |
| 7 | MondalLabs | **0 — server-side copy** | fail (Mongo-only) |
| 8 | ilhambintang17 | **≈2×** | fail |
| 9 | mateof | 1× default / 0 opt-in | fail |
| 10 | anasty17 | n/a (leech) | n/a |
| 11 | thecynetx/CyDrive | **≈2× both ways** | partial |
| 12 | mxvsh/drivegram | server-side | fail |
| 13 | **prgofficial/FileToLink** | 0 — parallel stream | n/a (AGPL) |
| 14 | Ns-Bots/TG-File-Store | n/a | fail |
| 15 | GopalSaraf/TorrentLeechBot | n/a (leech) | n/a |
| 16 | noob-mnar/TGDrive | 1× | partial |
| 17 | **herliansyah/teledrive** | **0 — `UploadFromReader`** | **pass — DB snapshot to channel** |
| 18 | rheyoloe/TeleDrive | 0 disk, **whole file RAM** | partial |
| 19 | Sam8r/TGWebDrive | 1× (server-written) | partial, 2 levels |

### What the full read delivered

1. **The streaming uploader** — herliansyah `UploadFromReader`, Go, on
   TDrive's own `gotd/td`. Removes the staging write.
2. **The DC-parallel download technique** — FileToLink. Clean-room only
   (AGPL). Includes the **25-connection-per-DC ceiling**.
3. **Verified constants** — 2000 MiB session switch, FloodWait ×1.3,
   3 attempts exponential 4–8 s, 60-char filenames, 10-item media
   groups, zero-byte rejection, `BadRequest`→document fallback.
4. **TDLib disqualified** structurally.
5. **Never materialise the payload whole** — in RAM either.
6. **Round-trip tests must exceed every limit in the system.**
7. **Folder-as-channel caps hierarchy at two levels** — caamer20,
   Sam8r, and TDrive's shared-drive model all hit this. TDrive's
   folder-in-metadata approach is the only one that scales.
