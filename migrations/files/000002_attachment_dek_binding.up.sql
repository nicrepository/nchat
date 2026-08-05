-- 000002_attachment_dek_binding.up.sql
-- RF-33 / RNF-17 (issue #113): persist the full binding of each attachment's
-- wrapped data key — which key-encryption key sealed it, and under which version
-- of the wrapping format — and allow that material to be written at the moment
-- the upload finishes rather than when it starts.
--
-- Why the emptiness guard comes first:
--
--   Migration 000001 shipped a wrapping format whose associated data did not
--   authenticate the plaintext length. This migration is part of the change that
--   fixes that, and the new code implements the new binding only: there is no
--   parser for the old one, deliberately, because a fallback is a downgrade path.
--   Any row written before this migration would therefore be undecryptable
--   afterwards, and no amount of backfill can invent a binding that was never
--   sealed. Rather than leaving such rows to fail at download time, the migration
--   refuses to run at all. Batch rewrap of legacy rows is not implemented; if
--   this guard ever fires, that work is a prerequisite, not an obstacle to route
--   around by emptying the table.
--
--   The check is a real query against files.attachments, not an inference from
--   the deployment's configuration. An absent KEK Secret is evidence that uploads
--   were never possible; it is not proof that no row exists, and only the table
--   can answer that.
--
-- Design decisions:
--   - kek_key_id is a non-secret label ('kek-2026-08'), never key material. It
--     names the key; it does not help open anything. The KEK itself lives only
--     in the file-service process environment, injected from a SealedSecret, and
--     has no column here and no object in SeaweedFS.
--   - dek_wrap_version versions the wrapped key's own binding, separately from
--     envelope_version, which versions the NCF1 content stream. They change for
--     different reasons: adding a field to the key's associated data does not
--     alter a single byte of any stored object.
--   - wrapped_dek becomes nullable so a pending_upload row can exist before its
--     key is sealed. That ordering is required, not cosmetic: the binding
--     authenticates the plaintext length, which is unknown until the last byte
--     has been read. The CHECK below then makes the material mandatory in every
--     state an upload can finish in, so "pending without a key" is legal and
--     "finished without a key" is not.
--   - dek_wrap_version is NOT NULL with no DEFAULT, and it is written by the
--     pending INSERT rather than at finalisation. That is what makes it a schema
--     fence: see below.
--   - No column gets a DEFAULT and nothing is backfilled. A default would make a
--     row claim a key or a format version that never protected it, and it would
--     also destroy the fence by letting the old INSERT succeed.
--
-- Why a lock AND a fence:
--
--   The emptiness check alone races. A file-service instance running the
--   previous build can begin an INSERT at any moment, and "the table was empty
--   when I looked" says nothing about the instant after. ACCESS EXCLUSIVE, taken
--   before the count and held to COMMIT by the surrounding transaction, closes
--   the window *during* the migration: a concurrent INSERT blocks on the lock
--   instead of slipping in between the count and the ALTER.
--
--   It does not close the window *after* the migration. A blocked INSERT is not
--   cancelled; it is queued. The moment this transaction commits, that statement
--   is released and runs against the new schema — and an old instance keeps
--   serving uploads until it is actually stopped. So the lock is necessary and
--   insufficient on its own.
--
--   The fence is what makes the release safe. dek_wrap_version is NOT NULL with
--   no DEFAULT, so an INSERT that does not name it — which is every INSERT the
--   previous build emits — fails with a not-null violation. The old writer
--   cannot create a row in the legacy format, whether it was queued behind the
--   lock or started minutes later. Draining the old instances is still the
--   documented procedure; the fence is what keeps a missed one from corrupting
--   the table rather than merely erroring.

BEGIN;

-- First statement, before the count and before any schema change, and held for
-- the rest of the transaction. Everything below is therefore decided against a
-- table no other session can write to.
LOCK TABLE files.attachments IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    existing_rows BIGINT;
BEGIN
    SELECT count(*) INTO existing_rows FROM files.attachments;

    IF existing_rows > 0 THEN
        RAISE EXCEPTION
            'migration 000002 requires files.attachments to be empty, found % row(s)',
            existing_rows
            USING HINT =
                'These rows were wrapped under the pre-000002 binding, which this build '
                'cannot open, and batch DEK rewrap is not implemented. Do not TRUNCATE to '
                'proceed: run the compatibility/rewrap task first. See '
                'docs/runbooks/file-service-envelope-encryption.md.';
    END IF;
END
$$;

ALTER TABLE files.attachments
    ALTER COLUMN wrapped_dek DROP NOT NULL,
    ADD COLUMN kek_key_id TEXT,
    -- The fence. NOT NULL with no DEFAULT, which is only possible because the
    -- guard above proved the table empty, and which is the whole point: an
    -- INSERT that does not name this column cannot succeed. Adding a DEFAULT
    -- here — for any reason, including easing a rollout — would silently let the
    -- previous build keep writing legacy rows.
    ADD COLUMN dek_wrap_version SMALLINT NOT NULL;

ALTER TABLE files.attachments
    ADD CONSTRAINT attachments_kek_key_id_length_check CHECK (
        kek_key_id IS NULL OR char_length(kek_key_id) BETWEEN 1 AND 64
    ),
    -- Pinned to the one version this build implements, not an open range. A
    -- future format needs its own migration, which is the point: the column
    -- must never be able to hold a version the service would refuse anyway.
    -- Zero is not a compatibility value; it is simply not allowed.
    ADD CONSTRAINT attachments_dek_wrap_version_check CHECK (dek_wrap_version = 2),
    -- The key material, all of it or none of it, and none of it only while the
    -- upload has not finished. pending_upload has not sealed a key yet and
    -- failed never will; every other state is reachable only through
    -- finalisation, which writes both columns in one statement.
    --
    -- dek_wrap_version is deliberately not part of this check: it is NOT NULL
    -- from the pending INSERT onwards, because a fence that only engaged at
    -- finalisation would let an old writer create the row first.
    ADD CONSTRAINT attachments_dek_binding_complete_check CHECK (
        status IN ('pending_upload', 'failed')
        OR (wrapped_dek IS NOT NULL AND kek_key_id IS NOT NULL)
    );

COMMIT;
