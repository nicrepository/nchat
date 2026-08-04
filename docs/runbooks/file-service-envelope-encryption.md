# file-service envelope encryption (RF-33 / RNF-17)

Operational runbook for the key ring that protects attachment content: how it is
provisioned, how it is rotated, what it fails closed on, and how the large-file
guarantee is validated.

The design itself — algorithm, chunk format, associated data — is documented in
`docs/api/file-attachments.md` and in `services/file-service/internal/crypto/envelope.go`.

## What is where

| Thing                   | Lives in                             | Never in                    |
| ----------------------- | ------------------------------------ | --------------------------- |
| Attachment plaintext    | nowhere at rest                      | SeaweedFS, PostgreSQL, disk |
| Ciphertext              | SeaweedFS                            | PostgreSQL                  |
| Per-file data key (DEK) | process memory, for one request      | PostgreSQL, SeaweedFS, logs |
| Wrapped DEK             | `files.attachments.wrapped_dek`      | SeaweedFS, HTTP responses   |
| KEK key id (not secret) | `files.attachments.kek_key_id`       | —                           |
| Key wrap version        | `files.attachments.dek_wrap_version` | —                           |

`dek_wrap_version` is `NOT NULL` with no default and is written when the pending
row is created. That is deliberate: see "Two mechanisms" below.
| KEK (key-encryption key) | `nchat-file-encryption` Secret → env | Git, PostgreSQL, SeaweedFS |

Stealing SeaweedFS yields ciphertext. Stealing PostgreSQL yields wrapped data
keys and a key **label**. Neither yields a KEK; both together still do not.

## Provisioning

1. Generate the active key and choose a dated label:

   ```bash
   openssl rand -base64 32     # FILE_ENCRYPTION_MASTER_KEY, exactly 32 bytes
   # FILE_ENCRYPTION_MASTER_KEY_ID, e.g. kek-2026-08
   ```

2. Write the unsealed Secret from
   `infra/k8s/secrets/templates/nchat-file-encryption.template.yaml`
   (or the `nchat-dev` variant) into `infra/k8s/secrets/unsealed/`, which is
   git-ignored. Leave `FILE_ENCRYPTION_PREVIOUS_KEYS` empty.

3. Seal it with strict scope and commit only the sealed output, exactly as
   `docs/runbooks/sealed-secrets-rotation.md` describes:

   ```bash
   scripts/secrets/sealed-secrets-seal.sh
   ```

   A SealedSecret is bound to its name **and** namespace, so `nchat` and
   `nchat-dev` each need their own.

4. Restart file-service. Only its Deployment references this Secret; it is
   deliberately not part of `nchat-secrets`, which every service mounts.

### Start-up behaviour

With `FILE_UPLOADS_ENABLED=false` the service is health-only and needs no key at
all. With uploads enabled, `Config.Validate` refuses to start on: a missing key,
a key that is not standard base64, a key that does not decode to exactly 32
bytes, a missing or malformed key id, a malformed `FILE_ENCRYPTION_PREVIOUS_KEYS`
entry, a duplicate id, or a previous entry that shadows the active id. No value
is echoed into the error, the log or `/readyz`. Nothing is ever generated or
defaulted.

## Rotation

Rotation replaces the KEK. It never re-encrypts an object: only the 32-byte data
key is re-wrapped, so a 500 MiB attachment costs a few hundred bytes of work.

1. Generate a new key and a new id. Move the current pair into
   `FILE_ENCRYPTION_PREVIOUS_KEYS` and put the new pair in
   `FILE_ENCRYPTION_MASTER_KEY` / `FILE_ENCRYPTION_MASTER_KEY_ID`:

   ```yaml
   FILE_ENCRYPTION_MASTER_KEY: <new base64 key>
   FILE_ENCRYPTION_MASTER_KEY_ID: kek-2026-11
   FILE_ENCRYPTION_PREVIOUS_KEYS: "kek-2026-08:<old base64 key>"
   ```

2. Re-seal, apply, restart. From this point every new upload is wrapped under
   the new id, and everything already stored still opens under the old one.

3. Rewrap the existing rows: for each row whose `kek_key_id` is the old id,
   unwrap under the old key, wrap under the new one, and write back
   `wrapped_dek` and `kek_key_id` in the same statement. The object in SeaweedFS
   is not read and not touched.

4. Only once

   ```sql
   SELECT count(*) FROM files.attachments
    WHERE kek_key_id = 'kek-2026-08' AND deleted_at IS NULL;
   ```

   returns zero may the old entry be removed from
   `FILE_ENCRYPTION_PREVIOUS_KEYS`. Removing it earlier makes every remaining
   attachment permanently unreadable — the service refuses an unknown key id
   rather than trying the keys it has, which is the intended failure but is not
   recoverable without the key.

**Limitation, stated plainly:** step 3 has no tool in this repository. The
format, the storage and the key ring support rewrap — a data key can be unwrapped
under one id and wrapped under another with no access to the object — but the
batch job that walks `files.attachments` is not implemented. Until it is, a
rotation leaves old rows on the old id and the old key must stay in
`FILE_ENCRYPTION_PREVIOUS_KEYS`. Do not describe rotation as automated.

## Failure modes

| Situation                                                     | Result                                                               |
| ------------------------------------------------------------- | -------------------------------------------------------------------- |
| `kek_key_id` names a key not configured                       | Download fails closed (`ErrUnknownKey`); no key is tried             |
| `kek_key_id` NULL                                             | Row is still `pending_upload`; treated as unknown, never defaulted   |
| `dek_wrap_version` absent from an INSERT                      | Rejected by the database: the fence against an older build           |
| `dek_wrap_version` is a version this build does not implement | Refused; no other version is attempted                               |
| `size_bytes` edited in the database                           | Unwrap fails **before any header is written**; see below             |
| `wrapped_dek` swapped between attachments                     | Unwrap fails: the binding covers attachment, workspace, key id, size |
| Row moved to another workspace                                | Unwrap fails, for the same reason                                    |
| Object modified, truncated, reordered                         | Fails mid-stream on the GCM tag; no partial success                  |
| Key id edited in the database                                 | Unwrap fails: the id is authenticated, not merely looked up          |

There is **no plaintext fallback anywhere**. A failed decryption is an error, and
the client sees the handler's generic failure — never which of KEK, DEK, tag,
nonce, header, size or ciphertext was at fault.

### Why the recorded size is part of the key

`size_bytes` is authenticated as part of the wrapped data key's associated data,
so it cannot be edited independently of the key.

Without that, an attacker with write access to PostgreSQL — and no access to the
KEK or to SeaweedFS — could lower `size_bytes`. The handler publishes that number
as `Content-Length`, so the response would terminate cleanly at the shorter
length and the client would accept a prefix of the file as the whole file. No
integrity check would have fired: every chunk served was genuine.

The order that closes it:

1. the plaintext length is **counted** from the bytes actually read off the body
   — never taken from `Content-Length`, which is not trusted anywhere;
2. the object is streamed to SeaweedFS and its NCF1 envelope closed;
3. only then is the data key wrapped, with that counted length inside its
   associated data;
4. the length, the wrapped key, the key id and the wrap version are written by
   one `UPDATE`, so no row is ever downloadable with a length its key does not
   cover;
5. on download the unwrap happens before the object is opened and before any
   status line or header, so a tampered length fails as a plain internal error
   with no body at all.

The decrypting reader carries the same length as a second, independent check. It
does **not** stop reading when it reaches that many bytes — that would recreate
the truncation — it keeps consuming to the authenticated final frame and then
requires the totals to match.

While an upload is in flight the row is `pending_upload` with `wrapped_dek` and
`kek_key_id` NULL. That state is legal and undownloadable;
`attachments_dek_binding_complete_check` makes both columns mandatory in every
state an upload can finish in. `dek_wrap_version` is not part of that check — it
is `NOT NULL` from creation, because it also serves as the schema fence.

## Legacy objects: the migration enforces their absence

The pre-`000002` wrapping format did not authenticate the plaintext length. This
build implements the new binding only — a parser for the old one would be a
downgrade path — so any row written before the migration would be permanently
unopenable afterwards, and no backfill can invent a binding that was never
sealed.

Migration `000002` therefore **refuses to run against a non-empty
`files.attachments`**, in its first statement, before any schema change. This is
not a documentation claim about the deployment; it is a query the migration runs
itself, and the transaction aborts with nothing applied.

### Preflight

Run this before deploying, against every environment:

```sql
SELECT count(*) AS attachment_count FROM files.attachments;
```

- The required result is **zero**.
- The migration repeats and enforces the same condition under an exclusive lock,
  so a stale preflight cannot let a bad deploy through.
- Any non-zero value **blocks the deploy**. That is the correct outcome.
- Do **not** `TRUNCATE`, `DELETE` or otherwise empty the table to get past it:
  that destroys user files to make a migration pass. A separate
  compatibility/rewrap task is the prerequisite, and it is not implemented.
- Do **not** add a `DEFAULT` to `dek_wrap_version` to let an older build keep
  writing. That column having no default is the fence described below.
- The absence of the KEK Secret is _not_ proof that no rows exist. It shows
  uploads could not have been configured; only the query above answers the
  question, and only the guard enforces it.

An operator who hits the guard should stop and escalate: reaching it means real
attachments exist under a format this build cannot read, which is a data
migration problem, not a deployment obstacle.

### Two mechanisms, because one is not enough

**The lock.** `000002` takes `ACCESS EXCLUSIVE` on `files.attachments` as its
first statement — before the count, before any `ALTER` — and the surrounding
transaction holds it until `COMMIT`. Without it the guard races: an instance
running the previous build can insert between "the table is empty" and the
schema change.

**The fence.** The lock does not help _after_ the commit. A concurrent `INSERT`
is not cancelled, it is queued, and it runs the instant the migration commits; an
old instance that was never stopped keeps accepting uploads regardless. So
`000002` also adds:

```sql
ADD COLUMN dek_wrap_version SMALLINT NOT NULL   -- no DEFAULT, by design
```

The previous build's `INSERT` does not name that column, so PostgreSQL rejects it
with a not-null violation — queued behind the lock or started an hour later, it
cannot create a legacy row. The current build names it on the **pending** insert,
not at finalisation, precisely so the fence is up from the first statement of an
upload.

A `DEFAULT` of any kind removes the fence. Do not add one to ease a rollout.

### Deploy procedure

The fence is defence in depth, not a substitute for draining. The order is:

1. disable new uploads (`FILE_UPLOADS_ENABLED=false`, or take the route out of
   the gateway);
2. drain or stop **every** old file-service instance;
3. confirm no upload is in flight —
   `SELECT count(*) FROM files.attachments WHERE status = 'pending_upload';` must
   be zero, and no pod may still be serving;
4. run the preflight count above;
5. apply the migration;
6. deploy the new version only — never run both side by side;
7. check `/readyz`;
8. re-enable uploads.

Expected during step 5: a statement from an instance that was missed blocks on
the lock and then **fails** after the commit. That error is the fence working. It
means step 2 was incomplete, not that the migration went wrong.

## Rollback of migration 000002

**A rollback is only possible while `files.attachments` is empty, and the
migration enforces that itself.**

Dropping `kek_key_id` and `dek_wrap_version` reverses nothing. The wrapped data
keys are untouched by the rollback and stay sealed under a binding that
authenticates the workspace, the key id, both format versions and the plaintext
length. The previous build's format authenticated none of those and cannot
rebuild the associated data, so it cannot open a single one of those keys — and
the columns that said which key and which format applied are exactly what the
rollback removes.

With rows present the down migration would therefore **succeed at the schema
level and break every download**: the objects still in SeaweedFS, the KEKs still
in the process environment, and nothing able to decrypt anything. Reverse rewrap
is not implemented and is out of scope; implementing it would first require
reimplementing the format that was removed.

So the down migration fails closed, in the same shape as the up migration: it
takes `ACCESS EXCLUSIVE` first, counts the rows under that lock, and raises
before the first `DROP` if it finds any. The transaction rolls back whole — no
column, no constraint and no row is changed by a refused rollback.

Every row blocks it, whatever its status. A `pending_upload` row is an upload in
flight whose finalisation would fail against the old schema; a `clean` row is a
file someone can download right now. Neither survives, so neither is filtered out
of the check.

### Procedure

1. disable new uploads (`FILE_UPLOADS_ENABLED=false`, or take the route out of
   the gateway);
2. drain or stop **every** instance of the new file-service — the lock closes the
   window between the check and the `DROP`s, it does not stop a service that is
   still running;
3. confirm no upload is in flight;
4. run the preflight:

   ```sql
   SELECT count(*) AS attachment_count FROM files.attachments;
   ```

5. proceed **only** if `attachment_count = 0`;
6. apply the down migration;
7. deploy the previous binary;
8. check `/readyz`;
9. re-enable traffic only after that check passes.

If the count is not zero, stop. Rolling back is not available for this data, and
none of the following is an acceptable way around it:

- do **not** `TRUNCATE` or `DELETE` attachments to unblock the rollback;
- do **not** drop or null `kek_key_id` or `dek_wrap_version` by hand;
- do **not** edit `wrapped_dek`, or re-use the KEK under a different key id, to
  try to make the old format read the data;
- do **not** roll the **binary** back while leaving the schema in place either:
  the previous build cannot open rows written in the current format, so that is
  the same outage by another route;
- restoring a backup is not a substitute for validating that the keys still open
  the objects.

Expected during step 6: a statement from a new instance that was missed blocks on
the lock and then **fails** after the commit, because it names columns that no
longer exist. That is intentional — it means step 2 was incomplete — and it
leaves no row behind.

### Diagnosis

To see what is blocking a rollback without touching key material:

```sql
SELECT
    status,
    count(*) AS attachment_count
FROM files.attachments
GROUP BY status
ORDER BY status;
```

Counts by status only. Do not select `wrapped_dek`, `kek_key_id`, nonces, object
keys, filenames or attachment ids to investigate this — none of them changes the
answer, which is simply whether the table is empty.

## Malware scan gate and APP_ENV

`FILE_MALWARE_SCAN_REQUIRED=false` makes an upload finalise as `clean` and be
immediately downloadable. SECURITY.md requires the gate wherever it applies, so
the flag is a local-development affordance only.

The exception is granted by a **positive match** on `APP_ENV` against a closed
allowlist:

| `APP_ENV`                                          | `FILE_MALWARE_SCAN_REQUIRED=false` |
| -------------------------------------------------- | ---------------------------------- |
| `development`, `dev`, `local`, `test`, `nchat-dev` | allowed                            |
| absent / unset                                     | **refused**                        |
| empty or whitespace                                | **refused**                        |
| `staging`, `production`, `prod`, anything else     | **refused**                        |
| a typo such as `developmnt`                        | **refused**                        |

Matching ignores case and surrounding whitespace; nothing else is inferred.
`APP_ENV` has **no default** — an absent value stays absent and is treated as a
deployed environment, so forgetting the variable can never hand out the
exception. Startup fails with a message naming the allowlist.

`FILE_MALWARE_SCAN_REQUIRED=true` (the default) needs no `APP_ENV` at all, and
neither does health-only mode with uploads disabled.

Every overlay already sets it: `infra/k8s/base/configmap.yaml` (`development`),
`infra/k8s/overlays/k3s-dev` (`development`), `k3s-staging` (`staging`) and
`nchat-dev-server` (`nchat-dev`). Local `.env` files must declare
`APP_ENV=development` explicitly.

## Large-file validation

Two checks, one automatic and one on demand.

**Every CI run** — `TestUploadStreamsWithoutBufferingTheWholeFile`
(`internal/service/upload_streaming_test.go`) uploads 8 MiB from a generator that
never materialises the file, into an object store that measures how far the
pipeline runs ahead of it. The upload fails the test if more than 4 chunks
(256 KiB) are ever in flight, and the download must hash back to the original
SHA-256.

The 8 MiB case also verifies the authenticated length end to end: the counted
size is what gets sealed, and the download only succeeds because the two agree.

**On demand** — the full-size round trip through the real envelope:

```bash
cd services/file-service
FILE_LARGE_ENVELOPE_MIB=512 go test ./internal/crypto/ -run LargeFile -v
```

It streams the plaintext through the encrypting and decrypting readers with
nothing buffered on either side, and fails if the heap after the round trip
exceeds 32 MiB.

### Recorded run

| Field              | Value                                                              |
| ------------------ | ------------------------------------------------------------------ |
| Date               | 2026-08-04                                                         |
| Payload            | 512 MiB (536870912 B), generated in stream, not on disk            |
| Chunk size         | 65536 B                                                            |
| Envelope size      | 537002012 B (header + one 16-byte tag per chunk)                   |
| SHA-256 original   | `0a5062a5561ddeb4f5180ca4db8bc12735053f9b05859d276af225b4aa4ce2f7` |
| SHA-256 round trip | `0a5062a5561ddeb4f5180ca4db8bc12735053f9b05859d276af225b4aa4ce2f7` |
| Duration           | 4.87 s                                                             |
| Heap after         | 1300112 B (~1.24 MiB) against a 32 MiB ceiling                     |
| Result             | PASS                                                               |

The round trip also runs the authenticated-length check: it is the invariant the
decrypting reader is given, so a 512 MiB object that produced any other total
would fail here rather than being served.

Not covered by that run: SeaweedFS itself. Confirming that the stored object is
ciphertext against a live filer is the opt-in integration suite,
`internal/service/upload_integration_test.go`, which needs
`FILE_TEST_DATABASE_URL` and `FILE_TEST_SEAWEEDFS_URL` and skips otherwise. The
migration guard has its own real-database suite,
`internal/storage/migration_guard_integration_test.go`, which applies 000001 and
000002 into a throwaway schema and proves that a single row of any status aborts
the migration with nothing applied.

Never commit a generated large file.
