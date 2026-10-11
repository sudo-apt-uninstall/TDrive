# Deep pass 7 — 2026-10-11 — noob-mnar/TGDrive `utils/directoryHandler.py`

Read in full. Blob `c990881305664ce11572a013bc2c8896c4cd6811`, HEAD
`88e2590edd6cbcf2246a7eaded19e62258d373aa`.

This is the file containing the `pickle.load()` RCE reported in deep pass 4.
Reading the whole file found **two further defects, one of which I consider
worse than the RCE** because it needs no attacker.

---

## 1. 🔴🔴 A transient network failure at startup destroys the entire index

```python
async def loadDriveData():
    try:
        try:
            msg = await client.get_messages(config.STORAGE_CHANNEL, config.DATABASE_BACKUP_MSG_ID)
        except Exception as e:
            raise Exception("Failed to get DATABASE_BACKUP_MSG_ID on telegram")

        if msg.document.file_name == "drive.data":
            dl_path = await msg.download()
            with open(dl_path, "rb") as f:
                DRIVE_DATA = pickle.load(f)
        else:
            raise Exception("Backup drive.data file not found on telegram")
    except Exception as e:
        logger.warning(e)
        logger.info("Creating new drive.data file")
        DRIVE_DATA = NewDriveData({"/": Folder("/", "/")}, [])
        DRIVE_DATA.save()
```

The `except Exception` arm cannot distinguish *"this is a first run"* from
*"the read failed"*. Everything lands in the same branch:

- Telegram unreachable / timed out / flood-waited at startup
- `get_messages` returns a message whose `document` is `None` → `AttributeError`
- the download fails or is truncated
- `pickle.load` raises on a partial file

In every case the result is the same: **a brand-new empty drive is created and
written to the local cache**, with no prompt, no distinct exit code, and
severity `warning`.

Now follow `save()`:

```python
def save(self) -> None:
    with open(drive_cache_path, "wb") as f:
        pickle.dump(self, f)
    self.isUpdated = True
```

`isUpdated` is set to `True`. And the backup task is:

```python
async def backup_drive_data():
    while True:
        await asyncio.sleep(config.DATABASE_BACKUP_TIME)   # 24 h
        if DRIVE_DATA.isUpdated == False:
            continue
        msg = await client.edit_message_media(
            config.STORAGE_CHANNEL, config.DATABASE_BACKUP_MSG_ID,
            media=InputMediaDocument(drive_cache_path, ...), file_name="drive.data")
```

`edit_message_media` **replaces the content of the one pinned backup message**.
There is no versioning, no second copy, no retention.

**Chain: a transient failure to read one message at startup → an empty index is
created → marked updated → 24 hours later the only remote copy of the index is
overwritten with it.** The file bodies survive in the channel, but the names,
hierarchy, and `file_id`s that address them are gone. The drive is
unrecoverable by its own code.

The RCE requires an attacker with edit rights on the storage channel. This
requires a flaky network for the few seconds of startup.

### Why this validates D2 directly
Our metadata model is an **append-only operation log inside the channel**. An
append-only log has no overwrite operation, so this failure mode does not
exist in it: a bootstrap that reads zero operations produces an empty
*projection*, never an empty *log*, and the next append is additive. The
single-mutable-blob design is what makes a read failure load-bearing.

I had been describing the op-log choice mostly in terms of reconstructibility
(FR-008). This is the sharper argument: **it removes a class of total-loss bug,
not just a recovery inconvenience.**

### Rules earned
- **(11) A bootstrap failure must never be representable as an empty state.**
  "Could not read the index" and "there is no index yet" are different outcomes
  and must take different code paths. First-run creation requires positive
  evidence of first run, never the absence of a successful read.
- **(12) Never overwrite the sole remote copy of the index from a state derived
  from a failed read.** A write-back must carry proof that the state it is
  writing descends from a state that was successfully read.

---

## 2. 🔴 Share tokens are 31 bits from a non-cryptographic PRNG, drawn from the
   same stream as public object IDs

```python
def getRandomID():
    while True:
        id = "".join(random.choices(string.ascii_uppercase + string.digits, k=6))
        if id not in DRIVE_DATA.used_ids:
            DRIVE_DATA.used_ids.append(id)
            return id
```

and

```python
def get_folder_auth(self, path: str) -> None:
    auth = getRandomID()          # ← the share secret
    ...
    folder_data.auth_hashes.append(auth)
    self.save()
    return auth
```

Three separate problems compound:

1. **Entropy.** 36⁶ ≈ 2.18 × 10⁹ ≈ **2³¹**. That is an enumerable keyspace for
   an HTTP endpoint, and the check is a plain membership test with no rate
   limit, lockout, or delay in this file.
2. **`random`, not `secrets`.** `random.choices` is Mersenne Twister. MT19937
   state is recoverable from roughly 624 consecutive 32-bit outputs.
3. **The same generator feeds public identifiers.** Every `Folder.id` and
   `File.id` is `getRandomID()`, and those IDs are *displayed in the UI and
   embedded in URLs*. So the attacker does not have to guess tokens — they can
   collect IDs the application hands them, reconstruct the PRNG state, and then
   **predict every share token the instance will ever issue.**

Point 3 is the interesting one and I have not seen it elsewhere in the
inventory: the secret and the public identifier are drawn from one stream, so
publishing identifiers leaks the secret generator.

Also: the field is named `auth_hashes` but stores the token **verbatim** —
nothing is hashed. And `append` is the only operation: **there is no
revocation**. A share link, once issued, is permanent.

### Rule earned
- **(13) Secrets and public identifiers must come from different generators,
  and secrets from a CSPRNG only.** Our share/auth values: `secrets.token_urlsafe`
  (≥128 bits), stored hashed, with an explicit revocation list — and a test that
  asserts the identifier generator and the secret generator are not the same
  object.

---

## 3. Confirmations and smaller findings

- **Whole-tree pickle on navigation — confirmed at the source.** `NewBotMode.set_folder`
  ends with `self.drive_data.save()`. Changing which folder the bot is *looking
  at* rewrites the entire index to disk. Deep pass 4 reported this; it is now
  read, not inferred.
- **`init_drive_data()` runs on every startup**, walks the whole tree to
  backfill `auth_hashes`, and calls `save()` **unconditionally** — a full
  pickle of the index on every launch even when nothing changed. Combined with
  the above, `isUpdated` is effectively always `True`.
- **Deletion never reaches Telegram.** `delete_file_folder` does
  `del folder_data.contents[file_id]` and saves. The document message stays in
  the channel forever; the ID stays in `used_ids` forever. "Delete" means
  "forget the pointer". Trash (`trash_file_folder`) only flips a boolean and
  has no purge path at all.
- **Auth token matching happens during descent.** In `get_directory`, the
  `auth in folder_data.auth_hashes` test runs for each segment as the path is
  walked, so a token matching any ancestor on the requested path authorises the
  whole request. Inheritance is presumably intended; the part that is not
  obviously intended is that `auth_home_path` is reassigned on each match, so
  with nested shares the **deepest** match defines the user's home.
- **Unhandled `KeyError` on any bad path.** `get_directory`, `get_file`,
  `rename_file_folder`, `trash_file_folder` and `delete_file_folder` all index
  `folder_data.contents[...]` with no guard; a stale or crafted path is a 500,
  not a 404.
- **Shadowed loop variable.** In `new_folder`/`new_file`,
  `for path in paths:` rebinds the `path` parameter mid-function. Harmless today
  only because `Folder(name, path)` is constructed before the loop.
- `Folder.path`/`File.path` do `path[:-1] if path[-1] == "/"` — an
  **IndexError on an empty string**, with no caller validation.
- `File.__init__` sets `self.type = type` (the builtin) and then immediately
  overwrites it with `"file"`. Dead, but it is the kind of line that says how
  carefully the rest was reviewed.
- `search_file_folder` and `get_trashed_files_folders` recurse with no depth
  limit over a structure that `pickle` will happily restore with a cycle in it.

---

## Conformance rules — cumulative
1. Round-trip = byte-exact, on every splitter branch.
2. Source deletion structurally impossible off the success path.
3. Flood-wait proven non-blocking under concurrency.
4. No code-executing deserialiser on remote bytes.
5. Index writes incremental, never whole-tree-per-mutation.
6. AST chokepoint guard bans `os.TempDir` / `os.MkdirTemp("")`.
7. Junk-path short-circuit (`.DS_Store` → success, body to `io.Discard`).
8. Network listeners bind loopback and carry a credential by default.
9. A read path never substitutes empty content for an error.
10. A claim that code runs requires reading the call path from its entry point.
11. **A bootstrap failure must never be representable as an empty state.**
12. **Never overwrite the sole remote copy of the index from state derived from
    a failed read.**
13. **Secrets and public identifiers come from different generators; secrets
    from a CSPRNG, stored hashed, with revocation.**

## Revised verdict on noob-mnar/TGDrive
FR-008 **pass** (it does back up to a pinned message) stands — but with the
qualification that the backup mechanism is itself the deletion mechanism. As
prior art it is now **negative prior art in three independent ways**: remote
pickle, destructive bootstrap fallback, and guessable/predictable share tokens.
It remains in the catalogue, unpruned, as the requirements file demands.
