# sachinmandawi + sauravsultane — read in full

The last two of nine. Both had been carried from the earlier audit on
trust. Reading them moved both **down**.

---

# sauravsultane/Telegram-Cloud-Drive

`3f284a5df8f3546a5ffcf30b640432d42db3bf56` · Node + Express + React ·
**Bot API** · 1 ★

## The 20 MiB break is confirmed, and it is unconditional

I had this recorded as *"any file >20 MiB is permanently
undownloadable."* That is true but too generous. The two halves of the
system were built against two different APIs with two different limits.

**Upload** — `chunk.service.js`, in its entirety a 45 MiB splitter:

```js
const CHUNK_SIZE = 45 * 1024 * 1024; // 45MB in bytes
...
const result = await telegramService.uploadDocument(chunkBuffer, chunkFilename, mimeType);
```

**Download** — `telegram.service.js`:

```js
const fileLink = await bot.getFileLink(fileId);
// We use axios to get a read stream from the Telegram CDN
const response = await axios({ method: 'get', url: fileLink, responseType: 'stream' });
```

`bot.getFileLink()` is Bot API `getFile`, **documented maximum 20 MB**.
Every chunk is 45 MiB *by construction*. So:

> **Every chunk of every multi-chunk file is permanently
> unrecoverable** — not an edge case, the designed behaviour of the
> happy path.

And the upload reports success. The file list shows the file. The
bytes are in Telegram. They cannot be got back out by this code.

The comment *"We use axios to get a read stream from the Telegram
CDN"* is the tell: the author was thinking about streaming efficiency
on a call that will never return a link for the files their own
uploader produces.

### The lesson worth keeping

This is not a stupid mistake. It is the **limit-asymmetry** failure:
the write path and the read path were each individually reasonable,
tested against their own API, and never tested as a round trip on a
file big enough to matter.

Our `ROUND-TRIP` conformance requirement already covers this in
principle. This repo is the proof that it must be tested **at a size
above every limit in the system**, not at a convenient small size.
A 1 KB round-trip test passes here.

## Disk and RAM

`uploadDocument(buffer, ...)` takes a **Buffer** — the whole file is
in RAM before anything is sent. `buffer.slice()` in Node returns a
view rather than a copy, so chunking adds nothing, but the full file
is already resident. No disk writes; the cost is memory, and on the
free-tier host this is aimed at, that is the binding constraint.

**Verdict: not usable prior art.** The only transferable thing is the
failure mode.

---

# sachinmandawi/Telegram-Drive

`0b67789a24a296a0bb15cd103fd6c4f49d49c882` · Tauri + TypeScript ·
MTProto (gramjs in-browser) · 23 ★

## The RAM problem is confirmed, and it compounds

Recorded as *"whole file to RAM via `blob.arrayBuffer()`."* Confirmed
— and it happens **twice, on both directions, for different reasons**.

### Upload — the whole file in RAM to compute a checksum

```js
const checksum = await sha256Blob(file);
```

```js
async function sha256Blob(blob: Blob): Promise<string> {
    const digest = await crypto.subtle.digest('SHA-256', await blob.arrayBuffer());
```

`crypto.subtle.digest` is one-shot: it has no streaming interface, so
the entire file must be materialised as an `ArrayBuffer` to hash it.
This runs on **every upload**, before anything is sent.

Compare Mahmud0808's `HashAccumulator`, which wraps the stream and
hashes the bytes **as they are already being read**, at zero extra
cost. Same goal, one design decision apart.

### Download — RAM, then base64, then across an IPC bridge

```js
const bytes = new Uint8Array(await blob.arrayBuffer());
const { invoke } = await import('@tauri-apps/api/core');
await invoke('cmd_save_download_bytes', {
    filename: targetName,
    bytesBase64: uint8ArrayToBase64(bytes),
});
```

Three multiplications of the same payload:

1. `blob` — the complete file, in RAM.
2. `arrayBuffer()` → `Uint8Array` — a second copy.
3. `uint8ArrayToBase64(...)` — a JS string at **+33%**, passed across
   the Tauri IPC boundary as a **command argument**.

Peak is roughly **2.3× the file in RAM**, plus a string of the same
order being serialised through an IPC channel designed for small
messages. A 2 GB download needs several GB of RAM to land — for a file
that is going to disk anyway and could have been streamed there by the
Rust side.

**This is the opposite failure to the one we have been hunting.** Every
other repo in the catalogue spends *disk* it did not need to spend.
This one spends *memory* to avoid spending disk. Both come from the
same root cause: **the payload is treated as a value rather than as a
stream.**

That is worth stating as a principle for the fork, because our
disk-write discipline could easily push us into exactly this trap:

> Avoiding a disk write by buffering in RAM is not a solution. The
> requirement is that the payload is never **materialised whole**, in
> any medium.

`DISK-WRITE-AUDIT.md` §3 already satisfies this — a streaming
encrypting reader with a 1 MiB chunk peak — but the principle was
implicit. It is now explicit, and this repo is why.

## What it gets right

**FR-008 pass, and the best metadata story in the set.** The command
surface includes `cmd_export_manifest`, `cmd_import_manifest`,
`cmd_repair_manifest`, `cmd_flush_manifest` — the namespace lives in
Saved Messages, not only in a local database, and there is an explicit
repair path. Of the nine, this is the clearest acknowledgement that
*the index must be reconstructible from the remote*.

`cmd_copy_item` is handled by `telegramCopyItem`, server-side — a
**third** independent instance of the copy-without-moving-bytes
pattern, after MondalLabs and Mahmud0808.

Conflict handling is genuinely thought through — `version`, `skip`,
`replace`, `keep_both`, with `version` as the default and real
sibling-name uniquification.

## The surprise — browser mode is not a Telegram drive

```js
const record: WebFileRecord = { ..., blob: file, ... };
await putWebFile(record);
addBandwidth('up_bytes', file.size);
```

Outside Tauri and outside Saved Messages mode, "uploading" writes the
`Blob` into **IndexedDB** and increments a bandwidth counter. Nothing
is sent to Telegram. It is a local file manager wearing the UI of a
cloud drive, and the bandwidth stat makes it look like it uploaded.

Not relevant to our requirement, but a good reminder that **a fallback
path that silently changes the storage medium is worse than one that
fails.**

---

## Nine of nine, final standing

| Repo | Verdict |
|---|---|
| **TDrive** | Fork base. One avoidable staging write + one temp bypass. |
| **MondalLabs** | Zero-byte server-side copy. Different interaction model. |
| **caamer20** | Streaming crypto + published format spec. Technique source. |
| **Mahmud0808** | Most disciplined; **TDLib makes compliance impossible**. |
| **mateof** | Correct streaming code, **off by default**. |
| **ali-abdollahzadeh** | Folder→ZIP, with cleanup. Disqualified by design. |
| **ilhambintang17** | 2× payload to `/tmp`. |
| **sachinmandawi** | Best manifest story; **spends RAM instead of disk**. |
| **sauravsultane** | **Multi-chunk files are unrecoverable by construction.** |

**The reading is complete.** Every repo in the catalogue has now been
opened and read at its recorded HEAD rather than summarised from a
prior audit. Three scorecard rows were wrong, two "unverified" rows are
now filled, and the two repos carried on trust turned out to be the two
worst in the set.

### What the whole exercise changed

1. **TDLib is disqualified** as a protocol base — structurally, not by
   preference. This is the strongest available argument for the TDrive
   fork and belongs in the roadmap.
2. **Three languages compute ciphertext length in advance.** The
   streaming-upload design is standard practice, not speculation.
3. **Three repos copy server-side.** TDrive move/rename are pure DB
   mutations, so it is already correct here.
4. **A new principle:** never materialise the payload whole — in RAM
   either. Disk discipline must not be bought with memory.
5. **A new test requirement:** round-trip conformance must run above
   every limit in the system. sauravsultane passes a small round-trip
   test and loses every real file.
