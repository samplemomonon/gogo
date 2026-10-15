-- 0003_outbox_lease: make the lease actually exclude a row from a claim.
--
-- The lease column existed from 0002 and was set by the claim, but the claim's
-- WHERE clause never read it. Because the claim commits before the publish
-- happens, FOR UPDATE SKIP LOCKED had no lock left to skip by the time a second
-- relay ran the query, so every relay claimed every pending row and each event was
-- published once per relay.
--
-- The predicate is the fix. It goes here rather than only into the query because
-- the partial index has to match it: the claim is the relay's hot path and runs
-- every few seconds forever, so its cost must stay proportional to the outstanding
-- work rather than to the accumulated history.
--
-- The old index is replaced rather than extended. A second, wider index over the
-- same rows would be redundant once this predicate is in the WHERE clause, and an
-- outbox's history is exactly the kind of data that makes a redundant index on a
-- write-heavy table expensive.

DROP INDEX IF EXISTS idx_outbox_pending;

-- leased_until leads because the lease is what the new predicate filters on, and
-- next_attempt_at follows so the index still serves the ORDER BY id scan of
-- pending work in id order once the lease test passes. id is included so the
-- covering scan can return the candidate ids without a heap visit on every sweep.
CREATE INDEX IF NOT EXISTS idx_outbox_pending
    ON outbox_events (leased_until, next_attempt_at, id)
    WHERE published_at IS NULL AND failed_at IS NULL;

-- A lease that has expired is claimable again, and an operator looking at a stuck
-- outbox needs to find those rows without scanning the whole table. This one is
-- not partial: it exists to answer "what is currently held", which is a question
-- about leases and only about leases.
CREATE INDEX IF NOT EXISTS idx_outbox_leased
    ON outbox_events (leased_until)
    WHERE leased_until IS NOT NULL;

-- Attempts counts failed publishes now, and the relay abandons a row once it
-- reaches MaxAttempts. Rows sitting at the cap are the ones an operator has to
-- look at, so they get their own index rather than being found by scanning the
-- pending set.
CREATE INDEX IF NOT EXISTS idx_outbox_attempts
    ON outbox_events (attempts)
    WHERE published_at IS NULL AND failed_at IS NULL AND attempts > 0;