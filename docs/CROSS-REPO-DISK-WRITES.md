# The nine repos, scored on one axis: what hits the disk

Disk-write discipline is non-negotiable, so the whole catalogue is
re-read against a single question:

> **Upload a folder. How many bytes get written to local disk before
> anything reaches Telegram?**

The ideal is **zero**: read the source file, encrypt in memory if asked,
stream to the network. Anything else is a second copy of the user's data
written to a drive that has a finite number of writes in it.

HEAD SHAs are those verified in the first-hand audit of 2026-10-09/10.

---

## Scorecard

| Repo | Folder upload writes | Verdict |
|---|---|---|
| **jeetrex17/TDrive** | **0 bytes unencrypted**; 1× payload if encrypted, in its own cache dir | **Best of the nine** |
| caamer20/Telegram-Drive | 0 — has a streaming encrypt reader | Technically excellent, **unusable** (§3) |
| Mahmud0808/TeleDrive | Unverified; TDLib manages its own cache | Unknown |
| sachinmandawi/Telegram-Drive | Whole file into RAM (`blob.arrayBuffer()`) | RAM, not disk, but no streaming |
| MondalLabs/telegram-cloud-file-manager | Unverified | Fails FR-008 anyway |
| **ilhambintang17/Teledrive** | **≈2× payload** — chunks to `/tmp/teledrive_upload_*`, then a merged file `/tmp/teledrive_merged_*` | Purest violation in the set |
| **ali-abdollahzadeh/teledrive** | **≈1× whole folder** — zips the folder to disk first | Catastrophic for folders |
| **mateof/TelegramFileManager** | **≈1× whole folder** + many uncoordinated roots | Catastrophic for folders |
| sauravsultane/Telegram-Cloud-Drive | Whole file in RAM (Multer `memoryStorage()`, 1 GiB) | Also permanently broken (§4) |

### The thing to notice

**Two repos implement folder upload by zipping the folder to disk first.**
Upload a 40 GB folder and 40 GB of ZIP is written before a byte is sent —
and unlike TDrive's staging, that is not optional, not contained in a
managed cache directory, and in `mateof`'s case not coordinated with any
other temp root.

A third writes every chunk to `/tmp` and then **merges them into a second
complete file**, peaking at roughly twice the payload.

So the caveat that prompted this audit — TDrive staging ciphertext —
is, measured against the field, **the mildest version of this problem
that exists.** And it is the only one that is: optional, contained in the
app's own cache directory, swept on startup, and relocatable by portable
mode.

---

## 1. TDrive, measured precisely

**Unencrypted folder import writes nothing.** Verified by reading
`backend/services/file/import.go`: the extraction temp directory is
created lazily and the code says so — *"so a folder-only import never
touches /tmp."* Files are handed to the uploader by path.

**Encrypted import writes exactly one copy**, via
`WriteCiphertextTemp` → `datadir.CreateCacheTemp("tdrive-enc-*")`. See
`DISK-WRITE-AUDIT.md` §2.

**And it is not forced.** `encrypt` is a `bool` parameter threaded
unmodified through `RunImport` from both callers, and the CLI defaults it
to `false`. See `DISK-WRITE-AUDIT.md` §1.

## 2. caamer20 proves the fix is real

`app/src-tauri/src/crypto/envelope/encrypt_reader.rs`:

```rust
fn encrypt_ready_chunk(&mut self) -> std::io::Result<()> {
    let cipher = XChaCha20Poly1305::new_from_slice(self.session.content_key.expose())
```

and `crypto/envelope/header.rs`:

```rust
pub header_length: u32,
pub chunk_size: u32,
pub key_slot_table_length: u32,
```

This is a **streaming encrypting reader**: a chunked XChaCha20-Poly1305
construction that encrypts each chunk as it is read, with a header that
records the chunk size so the total ciphertext length is computable from
the plaintext length.

That is precisely the design proposed in `DISK-WRITE-AUDIT.md` §3, and it
is running in a shipped application with 5,378 stars. **The fix is not
speculative.** It removes the temp file entirely, and the size Telegram
needs up front comes from arithmetic — the same arithmetic TDrive already
performs in `uploadByteSize(size, encrypt)`.

## 3. Why caamer20 is not the answer, despite being the best engineered

Three disqualifications, in order of severity:

**No licence.** There is no `LICENSE` file at HEAD `81aa3a70` — only
`SUPPORTER_*.md`. It is **source-available, not open source.** There is no
grant permitting a fork, modification, or redistribution.

> **Practical consequence:** we may read it and learn from it. We may
> **not** copy its code. The streaming-AEAD technique above is standard
> construction, not caamer20's invention, and would be implemented from
> the primitives — but the licence must be respected and the distinction
> kept sharp.

**It fails the recoverability requirement, dangerously.** From the
first-hand audit: encryption key-wrapping headers are stored **only in
local SQLite** (`app/src-tauri/src/db.rs`, table `encrypted_files`). Lose
the machine and every encrypted file in Telegram is permanently
undecryptable *even with the correct password*. For a portable drive whose
entire premise is that the data outlives the machine, that is fatal.

**Folder model.** `folder_layout.rs` keys `folder_metadata` by
`channel_id` with no parent field — folder *is* channel, exactly two
levels. No nested folders.

## 4. The others, briefly

- **sauravsultane** — chunks at 45 MiB via the Bot API but downloads via
  `getFile`, whose hosted cap is 20 MiB. **Any file over 20 MiB uploads
  successfully and can never be read back.** Shipped, unfixed.
- **ilhambintang17** — `/tmp/teledrive_upload_*` then
  `/tmp/teledrive_merged_*`; stale-temp cleanup only runs when a client
  polls upload status, so an abandoned upload's chunks persist
  indefinitely.
- **mateof** — storage roots at `local`, `local/temp`, `_temp`, a staging
  dir, caches and ZIPs, with no global budget over any of them.
- **ali-abdollahzadeh** — folder-as-ZIP is the *only* folder path, and
  success-path cleanup is unverified.
- **5 of 9 fail FR-008** (cannot rebuild from Telegram alone):
  sauravsultane, MondalLabs, ilhambintang17, mateof, sachinmandawi.

---

## Conclusion

**Stay with TDrive.** Nothing in the catalogue is better on the
non-negotiable requirement, most are dramatically worse, and the one
repo that is technically ahead on streaming encryption cannot be forked
and loses your data if the laptop dies.

The work, in order:

1. **Verify `tdrive put <folder>` without `-e` writes nothing.** If it
   holds, the painful caveat already has a working path around it today.
2. **Find out whether the GUI forces encryption** — a frontend read. If
   it does, that is a UI fix, not an architecture change.
3. **Implement the streaming encrypting reader** (`DISK-WRITE-AUDIT.md`
   §3), which removes the last avoidable write. caamer20 proves the
   design; implement it from the primitives.
4. **Relocate archive extraction** into `datadir.CacheDir()`
   (`DISK-WRITE-AUDIT.md` §4).
5. **Port `WriteLedger`** so "how much has this written to my SSD" has a
   number rather than an opinion.

## What has actually been read

First-hand, at the SHAs in the audit: all nine repos (2026-10-09/10).
This pass re-read, specifically for disk writes: TDrive
`datadir`/`daemon.state`/`daemon_process`/`file.import`/`file.upload`/
`encryption.service`, and caamer20 `crypto/envelope/{encrypt_reader,
header}.rs`.

**Not yet read for this requirement:** TDrive `photobackup/` (always
encrypts, therefore always stages — the heaviest writer in the product),
`mountdav/`, the rendition pipeline, and the frontend import dialog.
Those are next and are listed at the end of `DISK-WRITE-AUDIT.md`.
