-- +goose Up
CREATE TABLE token_accounts (
  account_id TEXT PRIMARY KEY,
  owner_id TEXT,
  kind TEXT NOT NULL CHECK (kind IN ('user','escrow','platform')),
  asset_id TEXT NOT NULL DEFAULT 'TALE',
  balance NUMERIC(78,0) NOT NULL DEFAULT 0 CHECK (balance >= 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE ledger_transfers (
  idempotency_key TEXT PRIMARY KEY,
  reason TEXT NOT NULL,
  competition_id UUID,
  metadata JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE ledger_postings (
  transfer_key TEXT NOT NULL REFERENCES ledger_transfers(idempotency_key) ON DELETE CASCADE,
  account_id TEXT NOT NULL,
  delta NUMERIC(78,0) NOT NULL CHECK (delta <> 0),
  PRIMARY KEY (transfer_key, account_id)
);
CREATE INDEX ledger_postings_account_idx ON ledger_postings(account_id, transfer_key);

CREATE TABLE competitions (
  id UUID PRIMARY KEY,
  creator_id TEXT NOT NULL,
  creator_name TEXT NOT NULL DEFAULT 'Admin',
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '', category TEXT NOT NULL DEFAULT '', tags TEXT[] NOT NULL DEFAULT '{}',
  max_participants INTEGER CHECK (max_participants IS NULL OR max_participants > 0),
  start_at TIMESTAMPTZ, deadline_at TIMESTAMPTZ, voting_deadline_at TIMESTAMPTZ,
  phase TEXT NOT NULL CHECK (phase IN ('draft','scheduled','open','voting','settling','settled','cancelled')) DEFAULT 'draft',
  prize_amount NUMERIC(78,0) NOT NULL DEFAULT 0 CHECK (prize_amount >= 0),
  entry_fee NUMERIC(78,0) NOT NULL DEFAULT 0 CHECK (entry_fee >= 0),
  fee_bps INTEGER NOT NULL DEFAULT 1000 CHECK (fee_bps BETWEEN 0 AND 10000),
  entry_fees_held NUMERIC(78,0) NOT NULL DEFAULT 0 CHECK (entry_fees_held >= 0),
  participants_count INTEGER NOT NULL DEFAULT 0, submission_count INTEGER NOT NULL DEFAULT 0, ballot_count INTEGER NOT NULL DEFAULT 0,
  max_votes_per_user INTEGER NOT NULL DEFAULT 3 CHECK (max_votes_per_user BETWEEN 1 AND 20),
  cancellation_reason TEXT, settlement_claimed_at TIMESTAMPTZ, settled_at TIMESTAMPTZ,
  results_digest TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX competitions_public_idx ON competitions(created_at DESC) WHERE phase <> 'draft';
CREATE INDEX competitions_due_idx ON competitions(phase, start_at, deadline_at, voting_deadline_at) WHERE phase IN ('scheduled','open','voting');
CREATE TABLE competition_participants (competition_id UUID NOT NULL REFERENCES competitions(id) ON DELETE CASCADE, user_id TEXT NOT NULL, joined_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(competition_id,user_id));
CREATE TABLE competition_submissions (
  competition_id UUID NOT NULL REFERENCES competitions(id) ON DELETE CASCADE, user_id TEXT NOT NULL, story_id UUID NOT NULL REFERENCES stories(id),
  story_title TEXT NOT NULL, story_author_name TEXT, cover_image_url TEXT, status TEXT NOT NULL CHECK(status IN ('submitted','withdrawn','disqualified')) DEFAULT 'submitted', submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(competition_id,user_id)
);
CREATE INDEX competition_submissions_gallery_idx ON competition_submissions(competition_id, submitted_at) WHERE status='submitted';
CREATE TABLE competition_contributions (competition_id UUID NOT NULL REFERENCES competitions(id) ON DELETE CASCADE, user_id TEXT NOT NULL, amount NUMERIC(78,0) NOT NULL CHECK(amount > 0), state TEXT NOT NULL CHECK(state IN ('held','refunded','settled')) DEFAULT 'held', created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(competition_id,user_id));
CREATE TABLE competition_ballots (competition_id UUID NOT NULL REFERENCES competitions(id) ON DELETE CASCADE, voter_id TEXT NOT NULL, cast_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(competition_id,voter_id));
CREATE TABLE competition_ballot_choices (competition_id UUID NOT NULL, voter_id TEXT NOT NULL, submission_user_id TEXT NOT NULL, PRIMARY KEY(competition_id,voter_id,submission_user_id), FOREIGN KEY(competition_id,voter_id) REFERENCES competition_ballots(competition_id,voter_id) ON DELETE CASCADE);
CREATE TABLE competition_results (competition_id UUID NOT NULL REFERENCES competitions(id) ON DELETE CASCADE, rank INTEGER NOT NULL, user_id TEXT NOT NULL, submission_id TEXT NOT NULL, votes INTEGER NOT NULL, amount NUMERIC(78,0) NOT NULL, PRIMARY KEY(competition_id,rank));

-- +goose Down
DROP TABLE IF EXISTS competition_results;
DROP TABLE IF EXISTS competition_ballot_choices;
DROP TABLE IF EXISTS competition_ballots;
DROP TABLE IF EXISTS competition_contributions;
DROP TABLE IF EXISTS competition_submissions;
DROP TABLE IF EXISTS competition_participants;
DROP TABLE IF EXISTS competitions;
DROP TABLE IF EXISTS ledger_postings;
DROP TABLE IF EXISTS ledger_transfers;
DROP TABLE IF EXISTS token_accounts;
