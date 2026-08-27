-- 000040_message_reactions_emoji_sequence_length.up.sql
-- RF-03 (issue #496): widens the reaction emoji length bound so a complete
-- Unicode sequence fits.
--
-- 000008 allowed 1..8 code points, which was right for the twenty-emoji
-- shortlist it was written for. The full RGI catalogue this issue adopts
-- contains sequences that are longer than that and still one emoji: a couple
-- with two skin tones is ten code points (two people, two modifiers, a heart,
-- its variation selector and three zero-width joiners). Storing that must not
-- depend on how many code points it happens to take, only on it being one
-- catalogued sequence — which the service validates before any write.
--
-- 32 is a bound, not a count: it is comfortably above the longest sequence
-- Unicode 16.0 defines and still small enough that the column cannot be used to
-- carry text.
--
-- The new constraint is strictly weaker than the one it replaces, so every
-- stored row already satisfies it. It is added NOT VALID all the same: ADD
-- CONSTRAINT with validation scans the table under ACCESS EXCLUSIVE, and this
-- table grows with every reaction ever made. Both statements here are
-- catalogue-only and return immediately; 000041 performs the scan under a lock
-- that blocks nothing an application does.
--
-- From the moment this commits, every insert and update is checked against the
-- new bound. There is no instant in which the column is unconstrained: the drop
-- and the add share one transaction.

-- nchat:blue-green contract-phase the DROP takes nothing away from a slot still
-- running the previous release. The constraint it drops is replaced in the same
-- transaction by one that is strictly weaker — 1..8 becomes 1..32 — so every
-- value the old code writes still passes, and every value the new code writes
-- was already impossible for the old code to produce. The gate asks for this
-- declaration because it cannot tell a widening DROP CONSTRAINT from a removal
-- without parsing SQL; the exceptions file beside it records the same reasoning
-- for the migrations that predate the policy.

BEGIN;

ALTER TABLE chat.message_reactions
    DROP CONSTRAINT message_reactions_emoji_check;

ALTER TABLE chat.message_reactions
    ADD CONSTRAINT message_reactions_emoji_check
    CHECK (char_length(emoji) BETWEEN 1 AND 32)
    NOT VALID;

COMMIT;
