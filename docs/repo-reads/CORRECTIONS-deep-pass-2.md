# Deep-pass corrections, part 2

Three more repositories read properly. **One of the corrections is to a
claim I made in a previous correction document.**

---

## 1. MondalLabs — the best hierarchy in the inventory

I ruled on this repo's hierarchy having read only `file_service.py`.
`folder_service.py` (15 KB) is the strongest folder implementation in
all nineteen.

### Unlimited depth, and subtree operations in constant round-trips

```python
"$graphLookup": {
    "from": "folders",
    "startWith": "$_id",
    "connectFromField": "_id",
    "connectToField": "parent_id",
    "as": "descendants",
}
```

> *"Total: 3 MongoDB round-trips regardless of tree depth."*

Delete a folder twelve levels deep with ten thousand descendants: one
aggregation to collect every descendant id, one bulk file delete, one
bulk folder delete. No recursion, no N+1.

Compare the rest of the inventory: **caamer20, Sam8r, and TDrive's
shared-drive model all cap hierarchy at two levels** because a folder
*is* a channel. MondalLabs stores `parent_id` as an ordinary field and
gets arbitrary depth for free.

**This directly refutes the two-level ceiling** and is the model our
folder layer should follow. It also independently validates TDrive's
folder-in-metadata choice, which is the only other approach in the set
that scales.

Also well built: `move_folder` uniquifies sibling names on collision;
`update_folder_size_hierarchy` does an atomic `$inc` walking up the
ancestor chain, with `recalculate_all_folder_sizes()` as an in-memory
repair path for when the incremental counter drifts. **Having a repair
path for a derived value at all** puts it ahead of most of the set.

### The defect — guarded in one function, not the other

`move_folder` blocks the self-nesting case explicitly:

```python
curr_id = target_parent_id
while curr_id is not None:
    if curr_id == folder_id:
        raise ValueError("Cannot move a folder into itself or one of its subfolders.")
```

`copy_folder` has **no such guard**, and its ordering makes that fatal:

```python
new_folder = Folder(name=name, parent_id=target_parent_id, created_by=created_by)
await new_folder.insert()                                    # 1. insert FIRST

child_folders = await Folder.find(Folder.parent_id == folder_id).to_list()   # 2. THEN query children
for child in child_folders:
    await copy_folder(child.id, new_folder.id, created_by)
```

Copy a folder into itself: the new copy is inserted as a child of the
source, *then* the children are queried — so the fresh copy is in the
result set, gets recursed into, produces another copy, and so on. **An
unbounded recursion that writes a row on every cycle.**

The hazard is known to the author. It is defended in `move_folder` and
left open in `copy_folder` twenty lines away. That is the governing
pattern of this entire audit, now at instance #11: *the protection
exists, is correct, and is absent from one path.*

**FR-008 verdict unchanged:** folders and files are Mongo documents.
Nothing but the bytes lives in Telegram. Lose the database and the
hierarchy is gone — however good the hierarchy was.

## 2. ali-abdollahzadeh — disqualified, not merely weak

I had been scoring this on its folder-to-ZIP write. The first line of
its service interface settles it before that matters:

```dart
abstract class TelegramFileService {
  /// Fetch all messages with files from Saved Messages
  Future<List<Map<String, dynamic>>> getSavedMessages();
```

`must-read-repos.md` states as a hard requirement: **"must not use
Saved Messages."** Saved Messages is not a fallback here, it is the
first-class storage concept in the abstraction.

`uploadFile({required String filePath, ...})` also confirms the
path-based upload — the ZIP must exist on disk before anything is
sent, as recorded.

**Two independent disqualifiers.** The ZIP write was the less
important one.

## 3. A correction to my own published claim about sachinmandawi

In `sachinmandawi-and-sauravsultane.md` I wrote:

> *"`const checksum = await sha256Blob(file);` … This runs on **every
> upload**, before anything is sent."*

**That is wrong.** `sha256Blob` lives in `platform.ts` and is called
from `uploadBrowserFile` — but only *after* this early return:

```ts
if (isSavedMessagesDefaultStorage()) {
    return await telegramUploadFile(file, folderId, onProgress, conflictStrategy);
}
ensureBrowserMode();
```

`isSavedMessagesDefaultStorage()` defaults to **true**
(`VITE_DEFAULT_STORAGE || 'saved_messages'`). So the real Telegram
upload path is `telegramUploadFile` in `telegramBrowser.ts`, which
does **not** call `sha256Blob`. The whole-file hash I complained about
runs only on the **browser/IndexedDB fallback** — the mode that never
contacts Telegram at all.

I read one file and generalised a claim about a path that file does not
contain. Exactly the failure I have been documenting in other people's
code.

**What does stand**, in `platform.ts`, on the live Tauri + Saved
Messages path:

```ts
const { blob, name } = await telegramDownloadBlob(messageId);
const bytes = new Uint8Array(await blob.arrayBuffer());
await invoke('cmd_save_download_bytes', {
    bytesBase64: uint8ArrayToBase64(bytes),
});
```

Whole file → `ArrayBuffer` → `Uint8Array` → base64 **string** → Tauri
IPC. That is real, it is on the default path, and ~2.3× the file in
RAM for a download that is going to disk anyway.

**And the repo is disqualified regardless** — Saved Messages is its
default storage, by the same hard requirement that disqualifies
ali-abdollahzadeh. My earlier praise of its manifest design was
doubly misplaced.

---

## Corrections ledger

| Repo | Previously published | Corrected |
|---|---|---|
| Ns-Bots | "DB-only, FR-008 fail" | **Server-side copy; no file DB exists** |
| GopalSaraf | "n/a (leech)" | **Time-split, 3 s overlap, irreversible** |
| CyDrive | "≈2× both ways" | **≈3×; deletes source in a `finally`** |
| **MondalLabs** | hierarchy unexamined | **Best hierarchy in the set; `copy_folder` recursion bug** |
| **ali-abdollahzadeh** | "ZIP write, disqualified by design" | **Disqualified by Saved Messages, independently** |
| **sachinmandawi** | "hashes whole file on every upload" | **Wrong — fallback path only. Download claim stands.** |

## What this changes for the fork

1. **Folder model: copy MondalLabs.** `parent_id` + `$graphLookup`
   (or the SQL recursive-CTE equivalent) gives unlimited depth and
   constant-round-trip subtree operations. Never folder-as-channel.
2. **Conformance test:** *every* destructive or recursive tree
   operation must be tested with the destination inside the source.
   MondalLabs guards `move` and not `copy`; a single shared
   `assertNotDescendant(src, dst)` helper called by both is the fix.
3. **Saved Messages is a disqualifier, and it is common** — two of
   nineteen use it as the default store. Any technique borrowed from
   either must be re-verified against a channel-based layout.
4. **Instance #11 of the governing pattern.** It is no longer a
   curiosity. Every safety check we write must be structurally
   unavoidable — a required argument or a shared helper — not a
   conditional that one call site can skip.

## Remaining in this deep pass

- `mxvsh/drivegram` upload path — code search returns nothing for
  upload/sendDocument; needs a directory walk of `app/` and
  `lib/file-manager/`.
- `anasty17` beyond `telegram_uploader.py`.
- CyDrive `webdav_server.py` (14 KB).
- caamer20 `resumable_upload.rs` re-read at current HEAD.
- herliansyah `backup.go`, `limiter.go`, `app/`, `db/`, `crypto/`.
- Mahmud0808 `StreamCrypto.kt`, `PartDownloader`, `SyncRepositoryImpl`.
- mateof `FileService.cs` in full.
- TDrive's own four: `mountdav/`, rendition pipeline,
  `photobackup/engine.go`, `ImportOptionsModal.svelte` encrypt default.
