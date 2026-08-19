-- 000012_link_reconcile_leases.down.sql
-- Removes the cross-service reconciliation cooldown.
--
-- Nothing becomes unsafe: a lease is a spending limit on provider calls, not a
-- fetch clearance and not a verdict. After this runs, chat-service and
-- file-service can once again each read the same URL at the provider within the
-- same minute — wasteful and rate-limit-visible, which is the finding this
-- table exists for, and nothing more than that.
--
-- Roll the application code back first: the acquisition is part of the claim
-- statement, so a pod still running the new code against a missing table fails
-- its claim and stops reconciling until it is replaced.

BEGIN;

DROP INDEX IF EXISTS files.link_reconcile_leases_expiry_idx;
DROP TABLE IF EXISTS files.link_reconcile_leases;

COMMIT;
