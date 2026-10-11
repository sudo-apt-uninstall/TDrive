# TDrive deep read — correcting my own central claim

Read at **`bbb96469757254f9f08f2e082187c9bb3d09c70b`** — note this is
**not** the `0e8ef556` I forked from. Upstream has moved; the fork is
behind.

I have been telling the owner that TDrive "stages the full ciphertext
to disk before transfer" and that the fix is to build a streaming
encrypting reader. **Reading the upload path properly, that is
substantially wrong.**

---

## Correction 1 — the multipart path already streams

```go
// Encrypted data is staged one part at a time, which makes retries rewind-safe
// without materializing a complete multi-gigabyte ciphertext file.
```

That is TDrive's own comment on `uploadMultipart`. The implementation:

```go
reader, writer := io.Pipe()
go func() {
    err := s.encryptStoredStream(&contextReader{ctx: ctx, source: plainFile}, writer, producerKey, plaintextSize)
    ...
}()
encryptedStream = reader
```

then, per part:

```go
stagedPart, err = stageUploadPart(ctx, stagingDir, encryptedStream, partLen)
...
cleanupPart()   // closed and removed before the next part is built
```

**Peak staging is one part, not the file.** Encryption runs
concurrently in a goroutine, feeding a pipe; each part is drained from
that pipe, sent, and deleted before the next one is staged.

This is *exactly* the pattern I wrote up as worth copying from
Mahmud0808 — "bound peak staging to one part; delete before building
the next." **TDrive already does it**, and does it better, because the
encryption is streaming rather than per-part re-entrant.

## Correction 2 — the streaming encryptor already exists

`DISK-WRITE-AUDIT.md` §3 proposed building a streaming encrypting
reader. The signature in the repo:

```go
s.encryptStoredStream(src, writer, producerKey, plaintextSize)
```

It takes a source and an `io.Writer`. **It is already a streaming
encryptor**, already in production on the multipart path. I proposed
building a thing that was already there, because I had read
`writeCiphertextTemp` and not the function 200 lines below it.

## Correction 3 — the remaining write is bounded, and it has a reason

`writeCiphertextTemp` survives on exactly one path: **single-part
encrypted uploads**.

```go
if encrypted {
    tempCipher, err := s.writeCiphertextTemp(&contextReader{ctx: ctx, source: source}, plaintextSize, masterKey)
    defer func() { _ = tempCipher.Close(); _ = os.Remove(tempCipher.Name()) }()
    uploadSource = tempCipher
    uploadSize = ciphInfo.Size()
}
```

Single-part means the file fits in one Telegram message — **below one
part (~2 GB)**. So the write was never the unbounded
"full ciphertext of a multi-gigabyte file" I described. It is bounded
by `maxPartBytes` by construction.

**And it is not laziness.** The retry wrapper requires rewind:

```go
err = s.retryVisibleSend(ctx, idempotentSend, func() error {
    // A retried attempt must resend the whole body from its start.
    if _, ok := rewindSeeker(uploadSource, 0); !ok {
        return fmt.Errorf("staged upload source is not rewindable")
    }
```

An `io.Pipe` is not rewindable. **The temp file *is* the rewind
buffer.** Replacing it with a pipe would mean a transport retry could
not resend the body — trading a bounded disk write for a correctness
hole on a flaky connection.

That is a real engineering trade-off, made deliberately, and my
characterisation of it as an oversight was unfair to the code.

### What the actual fix would be

Not "build a streaming encryptor" — that exists. The options are:

1. **Re-encrypt on retry.** Rewind the *plaintext* source (which is
   seekable) and re-run `encryptStoredStream` into a fresh pipe.
   Costs CPU on the rare retry path, zero disk always. This is the
   caamer20 replay model: *"Re-reading the unchanged plaintext from
   the beginning reproduces exactly the same ciphertext."*
2. **Route single-part encrypted uploads through `uploadMultipart`
   with `partCount == 1`.** Reuses the already-correct streaming path;
   peak becomes one part — which for a single-part file is the whole
   file, so this is **not** an improvement.

**Option 1 is the only real win**, and it is small: make
`rewindSeeker` able to rebuild the encrypted stream rather than seek
it. Worth doing, but it is a refinement of a working design, not a
rescue.

## The constraint, stated by TDrive itself

`backend/mountdav/put_body_buffer.go` explains why size must be known
before encryption starts:

> *"TDE1's encrypted stream header embeds the plaintext size and
> cannot be back-filled after streaming starts, so encrypted writes
> hard-require a known length; buffering first is what makes that
> requirement compatible with a real client that does not always
> provide one."*

This **confirms** the recorded finding that `uploadByteSize()` /
`planUpload()` are load-bearing — and explains the WebDAV buffer,
which exists only because macOS Finder sends chunked PUTs with no
`Content-Length`:

> *"Observed in practice, not hypothetical: macOS Finder's copy engine
> sends the real-content PUT … via chunked Transfer-Encoding (no
> Content-Length at all)."*

Bounded at `8 << 30`, into `datadir.CreateCacheTemp` (so it honours
portable mode), removed via `closeAndRemoveTempFile`. **A documented
workaround for a named client bug, not a design flaw.**

## The owner's original complaint — settled at the source

`frontend/src/ui/modals/ImportOptionsModal.svelte`:

```svelte
let extract = $state(false);
let encrypt = $state(false);
```

```svelte
$effect(() => {
    if ($view.open && !wasOpen) {
        extract = false;
        encrypt = false;
    }
    wasOpen = $view.open;
});
```

**`encrypt` initialises to `false` and is reset to `false` every time
the modal opens.** The checkbox is additionally gated behind
`{#if $view.payload?.personal}`.

> **Folder import is not forced-encrypt, and never was.** The default
> is plain, re-asserted on every open.

The owner's grievance — *"it forcefully encrypts folder uploads, and
to encrypt it causes disk write"* — does not apply to folder import on
this codebase. It applies to **photo backup**, where `controller.ts`
writes `{ ...defaultSettings, ...settings, encrypt: true }` with
`encrypt: true` **after** the spread, discarding a user `false`. That
remains the one genuinely forced path, and it is the one worth fixing.

## Rendition preparation — bounded and honest

```go
const maxPreparationSource = 30 << 20
...
stored, err := datadir.CreateCacheTemp("tdrive-rendition-source-*")
defer func() { stored.Close(); os.Remove(stored.Name()) }()
```

Images only, **30 MB hard cap**, `datadir`-scoped, removed on every
path. The `preparationWriter` even refuses bytes beyond the declared
size — the same self-verifying discipline as caamer20's
`CheckingEof`. Not a violation.

---

## Revised position on the fork

| Claim I published | Reality |
|---|---|
| "Stages full ciphertext before transfer" | **Multipart streams; peak = one part** |
| "Need to build a streaming encrypting reader" | **`encryptStoredStream` already exists** |
| "The staging write is the central violation" | **Bounded by part size; exists as a rewind buffer for retries** |
| "WebDAV staging unexamined" | **Bounded 8 GiB, datadir-scoped, documented Finder workaround** |
| "Folder import may be forced-encrypt" | **Default `false`, reset on every open** |

**TDrive's disk-write discipline is far better than I reported.** The
remaining items are:

1. `writeCiphertextTemp` on the single-part encrypted path — bounded,
   deliberate, improvable via re-encrypt-on-retry.
2. `os.MkdirTemp("")` in `RunImport` for archive extraction — **system
   temp, ignores portable mode**. This is now the *largest* real
   violation, and it is the one I under-weighted.
3. Photo backup's forced `encrypt: true`.

**None of these justify the scale of rework I had been planning.** The
honest next step is the portable-mode wiring (4 lines), then the
`RunImport` temp-dir fix, then photo backup. The ciphertext staging is
last, not first.

**Also: the fork is behind upstream** (`0e8ef556` → `bbb96469`) and
must be rebased before any of this is attempted.
