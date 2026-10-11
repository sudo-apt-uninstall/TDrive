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
| caamer20/Telegram-Drive | 0 — has a streaming encrypt reader | Technically ahead; see §3 |
| Mahmud0808/TeleDrive | Unverified; TDLib manages its own cache | Unknown |
| sachinmandawi/Telegram-Drive | Whole file into RAM (`blob.arrayBuffer()`) | RAM, not disk, but no streaming |
| MondalLabs/telegram-cloud-file-manager | Unverified | Fails FR-008 anyway |
| **ilhambintang17/Teledrive** | **≈2× payload** — chunks to `/tmp/teledrive_upload_*`, then a merged file `/tmp/teledrive_merged_*` | Purest violation in the set |
| **ali-abdollahzadeh/teledrive** | **≈1× whole folder** — zips the folder to disk first | Catastrophic for folders |
| **mateof/TelegramFileManager** | **≈1× whole folder** + many uncoordinated roots | Catastrophic for folders |
| sauravsultane/Telegram-Cloud-Drive | Whole file in RAM (Multer `memoryStorage()`, 1 GiB) | Also permanently broken (§5) |

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

## 2. caamer20's licence — correcting an error in the previous revision

The previous version of this document said caamer20 offered *"no grant to
fork or redistribute."* **The fork half of that is wrong**, and the
correction matters because it was used to rank the repo.

GitHub's Terms of Service §D.5:

> *"By setting your repositories to be viewed publicly, you agree to
> allow others to view and 'fork' your repositories... By making a
> repository public, you grant other Users a nonexclusive, worldwide
> license to use, display, perform and reproduce (by forking) Your
> Content through the Service as permitted by GitHub's functionality."*

**Forking a public repository on GitHub is expressly permitted.** The
repo's 808 forks are legitimate, forking is enabled, the project
description says *"an Open-source desktop app"*, and `open-source` is one
of its topics. The author's intent is not in doubt.

What is accurate, and is narrower:

| Claim | Status |
|---|---|
| Forking on GitHub is permitted | **Yes** — ToS §D.5 |
| The author intends it to be open source | **Yes** — description, topics, 808 forks |
| There is a `LICENSE` file | **No** — not at root, not in `Docs/` |
| GitHub detects a recognised licence | **No** — reports `Other` / `NOASSERTION` |
| Rights beyond the GitHub ToS are granted in writing | **Not found** |

The ToS grant is scoped — *"through the Service as permitted by GitHub's
functionality"* — and GitHub's own documentation adds: *"You may grant
additional rights by adopting a license"*, and that without one,
*"default copyright laws apply."*

So this is a **paperwork gap, not hostility**, and the fix is to ask.
`choosealicense.com` recommends exactly that: open an issue requesting a
licence. Given the project's self-description, an MIT or Apache-2.0 file
would simply record what the author already believes to be true.

**And for this project it is close to moot.** We are forking
**jeetrex17/TDrive, which is MIT**. caamer20 is being *read*, and what we
take from it is a *technique* — chunked AEAD with a computable ciphertext
length — which is a standard construction, is not copyrightable, and is
not caamer20's invention. No caamer20 code goes into this fork.

## 3. caamer20 publishes the format, and it settles every open question

`Docs/TDENC2_FORMAT.md` is a **complete, published format specification**
— the kind of document written to be read and implemented. It resolves
all three concerns raised in `DISK-WRITE-AUDIT.md` §3 as blockers to
removing TDrive's staging write.

**The exact ciphertext length, as a formula:**

> *"Exact ciphertext length is header length + plaintext length +
> 16×ceil(plaintext/chunk_size) + 68"*

So the length Telegram needs up front is pure arithmetic over the
plaintext length. **Concern 3 — "`uploadByteSize` must be exactly
right" — is answerable by construction, not by hope.**

**Retry and resume, without a temp file:**

> *"Re-reading the unchanged plaintext from the beginning reproduces
> exactly the same ciphertext; acknowledged Telegram parts can be skipped
> by the part-upload layer."*

The encryption is **deterministic** given the saved header (UUID, nonce
prefix, DEK). A retry re-reads the source and re-encrypts; already
acknowledged parts are skipped by the layer above. **Concerns 1 and 2 —
retry and resume — are solved by replay, not by staging.**

With the honest limit stated too:

> *"It does not start encrypting at an arbitrary plaintext offset without
> recomputing the final hash."*

So resuming at an offset costs a re-read, not a re-upload. For a drive
whose bottleneck is the network, that is the right trade — and it is
still strictly better than writing a second full copy to disk.

Two more details worth copying as *design*, not code:

- **Each chunk's AAD binds its own index, offset, length and the total
  length**, so a reordered or truncated stream fails authentication
  rather than decrypting into plausible garbage.
- **A final record carries the chunk count, total length and SHA-256 of
  all plaintext**, and a download is published only after it verifies.

**The staging write is therefore not merely avoidable in theory. The
design that avoids it has been published, in prose, by someone who
shipped it.**

## 4. Where caamer20 still should not be the base

Two reasons remain, and neither is the licence:

**It fails recoverability, dangerously.** From the first-hand audit:
encryption key-wrapping headers are stored **only in local SQLite**
(`app/src-tauri/src/db.rs`, table `encrypted_files`). Lose the machine and
every encrypted file in Telegram is permanently undecryptable *even with
the correct password*. For a portable drive whose premise is that the
data outlives the machine, that is disqualifying.

> Note the irony worth learning from: the format spec above is
> meticulous, and the *storage* of the thing that unwraps it is not.

**Folder model.** `folder_layout.rs` keys `folder_metadata` by
`channel_id` with no parent field — folder *is* channel, exactly two
levels. No nested folders. TDrive has real nesting.

## 5. The others, briefly

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
non-negotiable requirement, most are dramatically worse, and the one repo
technically ahead on streaming encryption loses your data if the laptop
dies.

But **caamer20's published format spec is the blueprint for the fix**,
and it is better evidence than its source code would have been: a
specification is written to be implemented.

The work, in order:

1. **Verify `tdrive put <folder>` without `-e` writes nothing.** If it
   holds, the painful caveat already has a working path around it today.
2. **Find out whether the GUI forces encryption** — a frontend read. If
   it does, that is a UI fix, not an architecture change.
3. **Implement the streaming encrypting reader** (`DISK-WRITE-AUDIT.md`
   §3), using the design evidence in §3 above. Removes the last
   avoidable write.
4. **Relocate archive extraction** into `datadir.CacheDir()`
   (`DISK-WRITE-AUDIT.md` §4).
5. **Port `WriteLedger`** so "how much has this written to my SSD" has a
   number rather than an opinion.

## What has actually been read

First-hand, at the SHAs in the audit: all nine repos (2026-10-09/10).
This pass re-read, specifically for disk writes: TDrive
`datadir`/`daemon.state`/`daemon_process`/`file.import`/`file.upload`/
`encryption.service`, and caamer20 `crypto/envelope/{encrypt_reader,
header}.rs`, `SUPPORTER_TERMS.md`, `SUPPORTER_LICENSE_INVARIANTS.md` and
`Docs/TDENC2_FORMAT.md` in full.

**Not yet read for this requirement:** TDrive `photobackup/` (always
encrypts, therefore always stages — the heaviest writer in the product),
`mountdav/`, the rendition pipeline, and the frontend import dialog.
Those are next and are listed at the end of `DISK-WRITE-AUDIT.md`.
