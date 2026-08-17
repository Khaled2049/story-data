-- +goose Up
-- A paid entry can be withdrawn (refunded) and then re-entered, which is a
-- genuinely new charge. The ledger keys entry transfers per contribution, so
-- each attempt needs its own number — reusing the first one made the ledger
-- treat the second charge as an already-applied duplicate and let the entry
-- through free while entry_fees_held still climbed.
ALTER TABLE competition_contributions
  ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt > 0);

-- +goose Down
ALTER TABLE competition_contributions DROP COLUMN attempt;
