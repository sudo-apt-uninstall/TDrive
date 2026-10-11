# Mahmud0808/TeleDrive — read in full

`0bb6a6e69bdd288363d1304cc8183c2d1f94be58` · Kotlin Multiplatform
(Android + desktop) · TDLib · 241 ★

This was the one genuine blank on the scorecard. Read:
`core/transfer/TransferExecutor.kt`, `core/transfer/PartUploader.kt`,
`data/repository/FileRepositoryImpl.kt` (28.9 KB).

**The answer is more interesting than a row in a table: TDLib makes
disk-write discipline structurally impossible, and that validates the
fork choice for a reason I had not previously established.**

---

## 1. The structural finding — TDLib uploads take a path, not a stream

`PartUploader.writePart()` contains this:

```kotlin
source.inputStream().use { input ->
    skipExactly(input, plainOffset)
    val ranged = RangeInputStream(input, plainSize)
    val hashed = hasher?.wrap(ranged) ?: ranged
    target.outputStream().buffered().use { output ->
        if (encrypt) {
            streamCrypto.encryptStream(key, hashed, output)
        } else {
            hashed.copyTo(output)
        }
    }
}
```

`RangeInputStream` is **the correct windowed-reader pattern** — the
third independent implementation in this catalogue, after caamer20's
Rust `encrypt_reader.rs` and mateof's C# `FileChunkStream.cs`. The
author knows exactly what the right abstraction is. They built it.

And then they can only use it as **the source of a disk write**,
because of the very next call:

```kotlin
telegramClient.uploadDocument(
    chatId = chatId,
    localPath = scratch.absolutePath,   // <-- a PATH
```

TDLib's `inputFileLocal` wants a path on disk. There is no
`inputFileStream`. The bytes must exist as a file before TDLib will
send them. So **every part of every split upload is written to scratch
— encrypted or not.** Note the `else` branch: `hashed.copyTo(output)`
is a straight copy of plaintext to disk for no reason other than to
give TDLib something to open.

**This is not a mistake by the author. It is the API.** No amount of
care inside a TDLib application can avoid it.

### Why this matters to us

> **Any TDLib-based design is permanently disqualified on the owner's
> non-negotiable requirement.**

TDrive speaks **raw MTProto** and controls its own `upload.SaveBigFilePart`
loop, so it *can* be handed a reader. The staging write in TDrive's
`upload.go` is a **choice that can be reversed**; the same write in
TeleDrive is a **constraint that cannot be**. I had recommended the
TDrive fork on maturity and licence. This is a harder reason, and it
should go in the roadmap.

It also retires a question I had left open: *should we consider a TDLib
base to save protocol work?* No. It would cost the one requirement that
is not negotiable.

## 2. What TeleDrive does well — bound the damage

Given that it must write, it writes as little as possible, and says so:

```kotlin
/**
 * Each part is sent and deleted before the next is built, so encryption never needs room for a
 * second copy, and recorded parts are skipped, so an interrupted upload resumes.
 */
```

```kotlin
} finally {
    withContext(NonCancellable + dispatchers.io) { scratch.delete() }
}
```

**Peak overhead is one part, not one file.** The `NonCancellable`
wrapper is the detail that makes it true — a cancelled coroutine still
runs the delete, so a cancelled 50 GB upload leaves one part behind at
worst, not fifty. Resume works by `filePartDao.partsOf()` filtering on
`messageId != null`, so acknowledged parts are skipped — the same
replay-not-staging idea as caamer20.

Non-split unencrypted uploads pass `sourceFile.absolutePath` straight
through: **zero extra bytes**. The staging write only appears for
encryption (`${entity.id}.tde`) and for splitting, and it is cleaned up
in a `finally`.

**Both patterns are worth copying into the fork**: bound peak overhead
to one part, and make cleanup uncancellable.

## 3. Defect — large files are deleted without verification

`releaseLocalCopy()` deletes the user's original after a backup upload
when `deleteAfterUpload` is set. It tries to verify first:

```kotlin
if (source.length() <= EAGER_HASH_LIMIT &&
    (contentHash == null || Hashing.sha256(source) != contentHash)
) {
    return
}
val cleanup = localCopyDeleter.delete(listOf(localPath))
```

`EAGER_HASH_LIMIT = 512L * 1024 * 1024`. Trace the three cases:

| Size | Hash | Result |
|---|---|---|
| ≤ 512 MiB | mismatch or null | returns — **safe** |
| ≤ 512 MiB | matches | deletes — **safe** |
| **> 512 MiB** | **never consulted** | **deletes** |

Above 512 MiB the first conjunct is false, the whole condition is
false, and the file is deleted **on a size check alone**. The guard is
inverted exactly at the boundary where the data is most expensive to
lose.

**And the hash was available.** The split path computes one while
writing parts:

```kotlin
val hasher = if (done.isEmpty()) HashAccumulator() else null
...
emit(PartUploadEvent.Completed(done.values.sortedBy { it.partIndex }, hasher?.result()))
```

`TransferExecutor` receives it, notices it is new, and saves it:

```kotlin
event.contentHash
    ?.takeIf { it != entity.contentHash }
    ?.let { fileDao.setContentHash(entity.id, it) }
```

…and then calls `releaseLocalCopy(transfer.type, entity, localPath, contentHash)`
passing the **stale local variable**, not `event.contentHash`.

> The ingredient for a safe decision is computed, written to the
> database, and then discarded at the one call site that needed it.

This is our governing defect pattern again, instance #10: *the
protection exists, is correct, and is bypassed on the path that matters.*
It is the same shape as mateof's `EnableMemorySplitUpload = false`.

**Guard for our fork:** a test that deletes a source only after a hash
comparison that provably ran — the hash must be an argument the delete
cannot be called without, not a value checked by a conditional that can
short-circuit.

## 4. Server-side copy — second independent confirmation

```kotlin
val document = telegramClient.copyDocument(
    chatId = chatId,
    remoteFileId = remoteFileId,
    ...
```

Copying a file re-sends it **by `remoteFileId`**, so Telegram
duplicates the message server-side; no bytes move through the device.
Same conclusion as MondalLabs, reached independently in a different
language. Two of nine do this deliberately.

The local half is worse:

```kotlin
private fun duplicateLocalCopy(sourcePath: String?, name: String): String? {
    val target = File(source.parentFile, name)
    return runCatching { source.copyTo(target, overwrite = false).absolutePath }
        .getOrNull()
}
```

A **full byte-for-byte copy of the cached local file** on every copy
operation, with the failure swallowed to `null`. A user duplicating a
10 GB video writes 10 GB to disk for a remote operation that cost zero.

## 5. Advance ciphertext sizing — third independent instance

```kotlin
val uploadSize = if (encrypt) streamCrypto.storedSize(plainSize) else plainSize
return FileParts.splits(uploadSize, limit)
```

With the comment: *"the limit is checked against the sealed size, which
grows by a frame header per megabyte."*

This is the exact analogue of TDrive's `uploadByteSize()` and
caamer20's TDENC2 length formula. **Three codebases, three languages,
all compute ciphertext length before encrypting.** The streaming-upload
proposal in `DISK-WRITE-AUDIT.md` §3 depends on precisely this being
possible, and it is now confirmed standard practice rather than an
inference from one repo.

## 6. Import is zero-write

`importLocalFile()` records `source.absolutePath` and never copies.
Staged imports (content-URI material that genuinely must be
materialised) are tracked by `fileImporter.isStaged(localPath)` and
swept by `sweepImportOrphans()`. The distinction between *the user's
file* and *our copy of it* is explicit in the data model — which is
what makes `releaseLocalCopy` deleting the wrong one in §3 possible at
all.

## 7. Scorecard row, finally

| | |
|---|---|
| **Unencrypted, no split** | **0 extra bytes** — path passed straight to TDLib |
| **Encrypted** | **1× ciphertext** to `cacheDir/staging`, deleted in `finally` |
| **Split (either way)** | **1× total**, written and deleted **one part at a time** |
| **Copy** | 0 remote; **1× full local copy** |
| **Peak overhead** | **one part**, not one file |
| **Cause** | **TDLib API — path-based, unavoidable** |

Honest verdict: **the most disciplined implementation in the catalogue
that still cannot meet the requirement.** It fails for a reason outside
the author's control.

## What changes

1. **Roadmap gets a harder reason for the TDrive fork** — not just
   maturity and MIT licence, but that TDLib *cannot* satisfy the
   non-negotiable requirement and raw MTProto can.
2. **Copy two patterns**: bound peak staging to one part; make scratch
   cleanup `NonCancellable`.
3. **New conformance test**: a source file may only be deleted when a
   verified hash is a *required argument* of the delete path.
4. **Open question for TDrive, from §4**: `duplicateLocalCopy` shows
   that a correct server-side copy can still be paired with a pointless
   local one. TDrive has no `copy.go` at all — move/rename are pure DB
   mutations — but the cached-copy question should be checked when
   `mountdav/` is read.
