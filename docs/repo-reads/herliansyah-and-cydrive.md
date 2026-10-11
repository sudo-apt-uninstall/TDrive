# herliansyah/teledrive + thecynetx/CyDrive

**I had been reading the wrong list.** The mandatory inventory at
`artifacts-4/telegram drive/must-read-repos.md` contains **nineteen**
repositories, not nine. I had been treating my own nine-repo catalogue
as complete. These are **#17** and **#11**; eight more remain.

The first one I opened contains the thing we were about to build.

---

# herliansyah/teledrive — #17

`b983748013da25708bb057e81eeab24df49e66eb` · **Go** · gotd/td ·
single binary

## `UploadFromReader` is the design from DISK-WRITE-AUDIT.md §3, already written

```go
// UploadFromReader streams an entire io.Reader to Telegram in sequential 512KB parts.
func (m *ClientManager) UploadFromReader(ctx context.Context, r io.Reader, size int64, fileName, mimeType string, limiter *SafeLimiter, onProgress func(uploaded, total int64)) (int, int64, int64, string, error) {
	totalParts := int((size + PartSize - 1) / PartSize)
	fileID := GenerateRandomID()
	hasher := sha256.New()
	buf := make([]byte, PartSize)

	for partIndex := 0; partIndex < totalParts; partIndex++ {
		n, err := io.ReadFull(r, buf)
		...
		chunk := buf[:n]
		hasher.Write(chunk)
		if err := m.UploadPart(ctx, fileID, partIndex, totalParts, chunk, limiter); err != nil {
```

Read what this does and does not do:

- Takes an **`io.Reader`**, not a path. The source can be a file, a
  pipe, or **an encrypting reader**.
- **One `buf` of 512 KB, reused every iteration.** Peak memory is one
  part, regardless of file size.
- **Zero disk writes.** No staging, no temp, no scratch.
- `hasher.Write(chunk)` — the content hash is computed **from bytes
  already in hand**, costing nothing. Same trick as Mahmud0808's
  `HashAccumulator`, and the opposite of sachinmandawi's
  `blob.arrayBuffer()`.
- `limiter.Pace(ctx)` between parts, `ExecuteWithFloodWait` around
  each part.
- `ctx.Err()` checked every iteration, so cancel is immediate and
  leaves nothing behind — because there is nothing to leave behind.

> This is exactly what `DISK-WRITE-AUDIT.md` §3 proposed, except it is
> not a proposal.

## And it is directly portable — same library, verified

| | gotd/td |
|---|---|
| **herliansyah/teledrive** | `v0.161.0` |
| **jeetrex17/TDrive** (our fork base) | `v0.145.1` |

**The same library.** The calls used here —
`UploadSaveBigFilePart`, `InputFileBig`, `MessagesSendMedia`,
`InputMediaUploadedDocument` — are the stable core of gotd and are
unchanged across that range. Both are `modernc.org/sqlite` too.

This changes the staging-write fix from *"design a streaming
encrypting reader and hope the protocol layer cooperates"* to
*"the reference implementation exists in our own language against
our own dependency."* Compose it as:

```
os.Open(src) → encryptingReader → UploadFromReader
```

with `uploadByteSize()` supplying the `size` argument that
`UploadFromReader` needs up front. That is why advance ciphertext
sizing mattered — it is the one parameter this function cannot derive
from a stream.

**Caveat, not yet checked:** 16 minor versions separate the two. The
API surface used here is stable, but that is an inference from the
call shapes, not a verified diff. Flagging rather than asserting.

### `PartSize = 512 * 1024`

```go
// PartSize is 512 KB, the optimal and maximum part size for MTProto upload.saveBigFilePart.
PartSize = 512 * 1024
```

Worth recording as a protocol fact to verify against TDrive's own
choice, and note the unrelated coincidence that Mahmud0808 splits
*files* at 512 MiB — different number, different layer.

## What I have not yet read here

`backup.go`, `discovery.go` (9 KB — probably the channel/namespace
recovery story, which bears on FR-008), `limiter.go`, and the `app`,
`db`, `crypto`, `web` packages. The uploader alone justified stopping
to write this up. **This repo is now the highest-priority remaining
read in the entire inventory.**

---

# thecynetx/CyDrive — #11

`62dfbe69c38adf1906bfd6eec68707eea029dfc8` · Python · WebDAV +
virtual drive

## The exact opposite: 2× payload, both directions

```python
@staticmethod
def split_file(file_path: str, output_dir: str, chunk_size_mb: int = 1900) -> List[str]:
    """Splits a file into multiple chunks and returns their paths."""
    ...
    with open(chunk_file, "wb") as chunk_f:
        while bytes_written < chunk_size_bytes:
            ...
            chunk_f.write(data)
```

Every part of a large file is **written to disk in full before any
upload begins** — and unlike Mahmud0808, not one at a time: `split_file`
returns `List[str]`, so **all** parts exist simultaneously before the
first byte is sent. A 10 GB file costs 10 GB of extra disk.

Download is symmetrical:

```python
def merge_chunks(chunk_paths: List[str], output_file_path: str) -> str:
    with open(output_file_path, "wb") as out_f:
        for part_path in chunk_paths:
```

Parts land on disk, then a second complete copy is written. **2× each
way.**

## The detail that makes it instructive

The very same file hashes correctly:

```python
sha256 = hashlib.sha256()
with open(file_path, "rb") as f:
    while chunk := f.read(CHUNK_BUFFER_SIZE):
        sha256.update(chunk)
```

A bounded 10 MB buffer, streaming, constant memory — *"efficiently"*,
says the docstring. **The author knows the pattern. They applied it to
hashing and not to uploading.**

That is now a recurring shape across the inventory, and it is worth
naming:

| Repo | Knows the pattern | Applies it to |
|---|---|---|
| mateof | `FileChunkStream` | …a path disabled by default |
| Mahmud0808 | `RangeInputStream` | …feeding a mandatory disk write |
| CyDrive | streaming hash loop | …hashing only, never upload |
| **herliansyah** | **`UploadFromReader`** | **the upload itself** |

Three projects built the right abstraction and pointed it at the wrong
problem. One pointed it at the right one.

`chunk_size_mb: int = 1900` is a conservative pre-Premium 2 GB figure
and does not consult the account's real limit — the
runtime-limits discipline mateof got right.

---

## Standing, and what is still unread

**Read: 11 of 19.** Still untouched: `anasty17/mirror-leech-telegram-bot`,
`mxvsh/drivegram`, `prgofficial/FileToLink`, `Ns-Bots/TG-File-Store`,
`GopalSaraf/TorrentLeechBot`, `noob-mnar/TGDrive`, `rheyoloe/TeleDrive`,
`Sam8r/TGWebDrive`.

### A correction I owe on an earlier document

`must-read-repos.md` states as a hard requirement: **"must not use
Saved Messages."** Yesterday I called sachinmandawi's manifest design
*"the best metadata story in the set"* — its namespace lives in **Saved
Messages**, which disqualifies the design rather than commending it.
`sachinmandawi-and-sauravsultane.md` needs that correction, and every
FR-008 judgement in the audit needs re-reading against the storage
location, not just against "is the index reconstructible."

### What changes now

1. **The staging-write fix has a reference implementation** in Go on
   our own dependency. Next concrete step is composing
   `encryptingReader` into `UploadFromReader` and verifying the gotd
   version gap.
2. **`uploadByteSize()` is load-bearing**, confirmed: `UploadFromReader`
   needs `size` up front and cannot derive it from the stream.
3. **Finish herliansyah first** — `discovery.go` and `backup.go` before
   the remaining eight.
