# Deep-pass corrections

The owner asked whether all nineteen had been deep-dived. They had
not. Depth ranged from *three files read in full* to **two repositories
that were given scorecard rows without being opened at all**.

This document corrects what the deep pass has changed so far. **Both
unopened repos were scored wrongly.** One of them was scored backwards.

---

## 1. Ns-Bots/TG-File-Store — I had this backwards

`874526a4174a41fe653659ac4cef8adf21580955` · Python · pyrogram

**What I published:** *"Bot API `file_id` in a database, no hierarchy
by design."* **FR-008: fail.**

**What the code does:**

```python
if DB_CHANNEL_ID:
    msg = await m.copy(int(DB_CHANNEL_ID))
    await msg.reply(text)
```

`m.copy()` is a **server-side copy** — zero bytes through the process,
zero to disk, zero to RAM. That is the **fourth** independent instance
of this pattern (MondalLabs, Mahmud0808, sachinmandawi, Ns-Bots).

And the "database" I claimed held the file index:

```python
class Database(BASE):
    __tablename__ = "database"
    id = Column(String, primary_key=True)
    up_name = Column(Boolean)
```

**One table. One boolean.** It is a per-user preference ("use original
name"), not a file index. There is no file database.

File identity lives entirely in the share link:

```python
base64_string = await encode_string(f"{m.chat.id}_{msg.message_id}")
url = f"https://t.me/{bot.username}?start={base64_string}"
```

The chat ID and message ID **are** the address. Nothing local to lose,
nothing to rebuild. For a link-sharing bot that is arguably a *better*
answer to FR-008's underlying concern than half the drives in this
inventory — the state is in Telegram and in the URL.

It is still not a drive: no hierarchy, no listing, no rename. But
*"DB-only index, FR-008 fail"* was simply wrong, and I wrote it
without looking.

## 2. GopalSaraf/TorrentLeechBot — "n/a (leech)" hid the best lesson

`83c595806522170e03d95751eff99dd4004a9479` · Python

I dismissed this as a leech bot. Its `split_large_files.py` contains
the single most instructive splitting decision in the inventory.

**For video, it splits by time, not by bytes:**

```python
minimum_duration = (total_duration / total_file_size) * (MAX_TG_SPLIT_FILE_SIZE)
...
await cult_small_video(input_file, output_file, str(start_time), str(end_time))
```

via `ffmpeg -ss <start> -to <end> -c copy`. And critically:

```python
# adding offset of 3 seconds to ensure smooth playback
start_time = end_time - 3
```

> **The parts deliberately overlap by three seconds. They cannot be
> concatenated back into the original file.** Byte-exact recovery is
> impossible by design.

That is a legitimate choice *for a leech bot* — each part is
independently playable in Telegram, which is what its users want. It
is **disqualifying for a drive**, where the only acceptable outcome is
the original bytes back.

The same file also contains the byte-exact alternatives:

```python
"split", "--numeric-suffixes=1", "--suffix-length=5", f"--bytes={MAX_TG_SPLIT_FILE_SIZE}"
```
```python
"rar", "a", f"-v{MAX_TG_SPLIT_FILE_SIZE}b", "-m0"
```

**One repository, two splitters, only one reversible** — selected by
file extension, silently. A user who uploads an `.mkv` gets
unrecoverable parts; the same user uploading a `.zip` gets exact ones.

### Why this matters to us

Our conformance suite tests round-trip fidelity. This repo is the
proof that the test must assert **byte-exact equality of the
reassembled file**, not "the parts play" or "the sizes add up". A
3-second overlap passes every weaker check.

It also pairs with the sauravsultane lesson: *write path and read path
must be tested as one round trip, at a size above every limit, on
every file type the splitter branches on.*

## 3. thecynetx/CyDrive — the worst disk offender, advertising the opposite

I scored this from `chunker.py` alone (2.6 KB) as *"≈2× both ways."*
`telegram_client.py` (16.8 KB) makes it considerably worse.

### Three full copies on disk, simultaneously

```python
crypto = CyCrypto(self.config.encryption_password)
temp_fd, temp_encrypted_file = tempfile.mkstemp(prefix="cydrive_enc_")
crypto.encrypt_file(local_path, temp_encrypted_file)
upload_path = temp_encrypted_file
```

Then chunking runs **on the encrypted temp file**:

```python
if FileChunker.needs_chunking(upload_path, self.config.chunk_size_mb):
    chunk_dir = tempfile.mkdtemp(prefix="cydrive_chunks_")
    chunks = FileChunker.split_file(upload_path, chunk_dir, self.config.chunk_size_mb)
```

So for an encrypted large file, disk simultaneously holds:

1. the original,
2. the complete ciphertext temp,
3. **every part**, all of them, before the first is sent.

**≈3× the payload.** And on download of the same file: all parts, plus
the merged `.enc_tmp`, plus the decrypted output — **≈3× again**.

### And it prints this while doing it

```python
os.remove(local_path)
print(f"🧹 [Zero-Disk Storage] Local temporary buffer deleted. 0 Bytes used on your hard drive.")
```

The claim is made at the one moment it is least true. Elsewhere it
logs `[PURE CLOUD VFS] ... [0 Bytes Local Disk]` — which **is** honest,
but only for *incoming* files indexed from Telegram
(`_process_incoming_file` stores metadata only). The marketing from the
honest path has been pasted onto the dishonest one.

### The deletion is in a `finally`

```python
except Exception as e:
    print(f"❌ [Telegram Upload Error] Failed to upload {file_name}: {e}")
    return None
finally:
    ...
    if os.path.exists(local_path):
        os.remove(local_path)
```

`finally` runs on **every** path, including the failure path. Upload
throws → caught → `return None` → **and the source file is deleted
anyway.** No hash verification, no success check.

Compare Mahmud0808, which at least *attempts* verification and only
skips it above 512 MiB. CyDrive does not attempt it at all — and its
hash is unavailable regardless:

```python
sha256 = FileChunker.calculate_sha256(local_path) if file_size <= 100 * 1024 * 1024 else None
```

**No hash above 100 MB** — the same inverted-guard shape as
Mahmud0808's 512 MiB limit, now seen twice in two languages.

### Unbounded recursion on flood wait

```python
except FloodWaitError as e:
    await asyncio.sleep(e.seconds)
    return await self.upload_file(local_path, rel_path, progress_callback)
```

The recursive call runs *before* the outer `finally`, so the inner
invocation deletes `local_path`; if it also flood-waits, it recurses
again on a file that is being removed underneath it. **No depth limit,
no backoff cap.** Contrast anasty17's `stop_after_attempt(3)`.

### What is genuinely good

`_process_incoming_file` is a real zero-byte path — a file arriving in
the Telegram chat is indexed by metadata alone and surfaced in the
virtual drive without ever being fetched. That is the correct VFS
instinct, and it is the only part of this file that lives up to the
product's name.

---

## Running corrections to the scorecard

| Repo | Published | Corrected |
|---|---|---|
| **Ns-Bots** | "DB-only, FR-008 fail" | **Server-side copy, 0 bytes; no file DB exists** |
| **GopalSaraf** | "n/a (leech)" | **Time-based video split, 3 s overlap, irreversible** |
| **CyDrive** | "≈2× both ways" | **≈3× both ways; deletes source in a `finally`** |

### New rules earned

1. **Round-trip conformance must assert byte-exact equality** of the
   reassembled file — and must run **on every branch of the splitter**,
   because GopalSaraf branches on file extension.
2. **A source file may only be deleted from a success path**, never a
   `finally`. CyDrive is the proof; Mahmud0808 is the near-miss.
3. **The inverted size guard is a recurring species**, not a one-off:
   Mahmud0808 skips hashing above 512 MiB, CyDrive above 100 MB. Both
   drop the safety check exactly where the data is most expensive. Our
   guard must be a required argument, not a conditional.
4. **Treat a project's own claims as evidence of nothing.** "Zero-Disk
   Storage" is printed by the worst disk offender in the inventory.

## Still outstanding in this deep pass

- `sachinmandawi/telegramBrowser.ts` — the **actual** Telegram layer;
  my RAM findings came from the browser-fallback shim.
- `MondalLabs/folder_service.py` (15 KB) — hierarchy, which I ruled on
  without reading.
- `ali-abdollahzadeh` uploader — judged so far on compression and UI
  strings only.
- `mxvsh/drivegram` upload path — never seen; `files.ts` is pure CRUD.
- `anasty17` beyond `telegram_uploader.py`.
- CyDrive `webdav_server.py` (14 KB).
- TDrive's own four gaps: `mountdav/`, rendition pipeline,
  `photobackup/engine.go`, `ImportOptionsModal.svelte` encrypt default.
