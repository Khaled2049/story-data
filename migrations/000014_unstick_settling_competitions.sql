-- +goose Up
-- Settlement used to claim phase='settling' on its own connection, before a
-- payout transfer that could fail (duplicate postings for one account). The
-- claim survived the failure, and 'settling' is a phase settle re-entered and
-- cancel refused, so the escrow became unreachable. Settlement is now one
-- transaction, but rows stranded by the old code need releasing.
--
-- A competition with no committed escrow:release transfer never moved money,
-- so it returns to 'voting' and can be settled or cancelled normally. One that
-- does have the transfer was paid out already; it stays in 'settling', where
-- SettleCompetition picks it up idempotently and finishes the results rows.
UPDATE competitions
SET phase = 'voting', settlement_claimed_at = NULL, updated_at = now()
WHERE phase = 'settling'
  AND NOT EXISTS (
    SELECT 1 FROM ledger_transfers
    WHERE idempotency_key = 'escrow:release:' || competitions.id::text
  );

-- +goose Down
SELECT 1;
