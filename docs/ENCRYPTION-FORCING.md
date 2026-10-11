# Where encryption is actually forced

The working assumption behind `DISK-WRITE-AUDIT.md` was that folder
uploads are forcibly encrypted. Reading the frontend — which the two
previous documents listed as "next" and did not do — **corrects that.**

---

## Folder import: a real checkbox, not forced

`frontend/src/ui/modals/ImportOptionsModal.svelte`:

```svelte
{#if $view.payload?.personal}
    <label id="import-options-encrypt-row" class="modal-choice-row">
        <input id="import-options-encrypt" type="checkbox"
               bind:checked={encrypt} onchange={toggled} />
        <span><strong>Encrypt before upload</strong>
```

A two-way bound checkbox. And `transfers.android-folder.dom.test.ts`
drives the unencrypted path explicitly:

```ts
mocks.openImportOptionsModal.mockResolvedValue({ encrypt: false, extract: false });
```

So `encrypt: false` is a supported, tested outcome of the folder import
dialog.

## Single-file upload: stronger still

`frontend/src/ui/modals/UploadOptionsModal.svelte`:

```ts
if ($view.open && !wasOpen) {
    // Default to plain upload every time. Encryption is explicit per batch.
    uploadMode = 'plain';
}
```

It **resets to plain on every open**, deliberately, with the reasoning in
the comment. Encryption is opt-in per batch and never sticky.

## Photo backup: genuinely forced, at three layers

`internal/app/photo_backup.go`:

```go
const (
    photoBackupPolicyTTL = 2 * time.Minute
    photoBackupEncrypted = true
)
```

`internal/app/photo_backup_destination.go`:

```go
Encrypt: photoBackupEncrypted, ManualPaused: current.ManualPaused,
```

`frontend/src/modules/photo-backup/controller.ts`:

```ts
publish(await savePhotoBackupSettings({ ...defaultSettings, ...settings, encrypt: true }));
```

**That last line is the one that matters.** `encrypt: true` sits *after*
the spread of `settings`, so a user value of `false` is discarded on its
way to the backend. A caller cannot turn it off even by passing the flag
explicitly — it is overwritten in transit.

`frontend/src/api/photo-backup.ts` agrees:

```ts
const defaultSettings: PhotoBackupSettings = { ..., encrypt: true };
```

This is consistent with the README, which states photo backup is always
encrypted. It is a deliberate product decision, not an accident. It is
also **the only forced-encryption path in the product.**

## And photo backup is the heaviest writer

`internal/app/photo_backup_pipeline.go` names its phases:

```go
[...]string{"budget_wait", "materialize", "destination",
            "original_upload_and_projection", "ledger", "encrypt",
            "transfer", "manifest_commit", "projection",
            "preview_prepare", "preview_send", "preview_admission"}
```

`"encrypt"` and `"transfer"` are **separate, sequential phases**. The
ciphertext is produced in full before transfer begins — the staging write
from `DISK-WRITE-AUDIT.md` §2, on a path that cannot be turned off, for
every photo and video backed up.

`"materialize"` is a second write on mobile: the README states the source
is copied into app storage until the upload finishes.

So: **forced encryption and the staging write are the same feature, and
it is photo backup.**

---

## What this changes

| Previous assumption | Finding |
|---|---|
| Folder upload forces encryption | **No** — checkbox, tested both ways |
| The forcing is a UI bug | **No** — it is photo backup, by design |
| The staging write is the main cost | **Yes, and it is worst where it cannot be disabled** |

The streaming encrypting reader (`DISK-WRITE-AUDIT.md` §3) therefore
matters **more** than previously argued, not less: it is the only way to
remove the write on the one path where the user has no choice.

## Still unread, and it decides whether the checkbox is a real choice

**The initial value of `encrypt` in `ImportOptionsModal.svelte`.** A
checkbox that defaults to checked is "forceful" in practice to anyone who
does not notice it, and would fully explain the original report. The
single-file modal defaults to plain and says so; whether the import modal
does the same is not established.

Also still unread: `backend/mountdav/` staging, the rendition pipeline,
and `backend/photobackup/engine.go` in full (32 KB).
