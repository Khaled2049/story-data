-- +goose Up
CREATE TABLE book_clubs (
  id UUID PRIMARY KEY,
  owner_id TEXT NOT NULL,
  name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
  description TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT '',
  activity TEXT NOT NULL DEFAULT '',
  meetup TEXT NOT NULL DEFAULT '',
  book_of_the_month JSONB,
  reading_schedule JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX book_clubs_updated_idx ON book_clubs (updated_at DESC);

CREATE TABLE book_club_members (
  club_id UUID NOT NULL REFERENCES book_clubs(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner','member')),
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (club_id, user_id)
);
CREATE INDEX book_club_members_user_idx ON book_club_members (user_id, club_id);

CREATE TABLE book_club_member_progress (
  club_id UUID NOT NULL REFERENCES book_clubs(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  current_chapter INTEGER NOT NULL DEFAULT 0 CHECK (current_chapter >= 0),
  notes TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (club_id, user_id)
);
CREATE INDEX book_club_progress_rank_idx ON book_club_member_progress (club_id, current_chapter DESC);

CREATE TABLE book_club_prompts (
  id UUID PRIMARY KEY,
  club_id UUID NOT NULL REFERENCES book_clubs(id) ON DELETE CASCADE,
  chapter_number INTEGER NOT NULL CHECK (chapter_number > 0),
  question TEXT NOT NULL CHECK (char_length(question) BETWEEN 1 AND 500),
  description TEXT NOT NULL DEFAULT '' CHECK (char_length(description) <= 1000),
  creator_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX book_club_prompts_club_idx ON book_club_prompts (club_id, chapter_number, created_at);
CREATE TABLE book_club_prompt_responses (
  id UUID PRIMARY KEY,
  prompt_id UUID NOT NULL REFERENCES book_club_prompts(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  content TEXT NOT NULL CHECK (char_length(content) BETWEEN 1 AND 2000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX book_club_prompt_responses_prompt_idx ON book_club_prompt_responses (prompt_id, created_at);

CREATE TABLE book_club_polls (
  id UUID PRIMARY KEY,
  club_id UUID NOT NULL REFERENCES book_clubs(id) ON DELETE CASCADE,
  type TEXT NOT NULL DEFAULT 'book-selection',
  question TEXT NOT NULL CHECK (char_length(question) BETWEEN 1 AND 500),
  end_date TEXT NOT NULL DEFAULT '',
  creator_id TEXT NOT NULL,
  is_active BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX book_club_polls_club_idx ON book_club_polls (club_id, created_at DESC);
CREATE TABLE book_club_poll_options (
  id UUID PRIMARY KEY,
  poll_id UUID NOT NULL REFERENCES book_club_polls(id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (position >= 0),
  text TEXT NOT NULL CHECK (char_length(text) BETWEEN 1 AND 500),
  book_data JSONB,
  UNIQUE (poll_id, position)
);
CREATE TABLE book_club_poll_votes (
  poll_id UUID NOT NULL REFERENCES book_club_polls(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  option_position INTEGER NOT NULL CHECK (option_position >= 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (poll_id, user_id)
);

CREATE TABLE book_club_usage (
  user_id TEXT NOT NULL,
  action TEXT NOT NULL,
  window_start TIMESTAMPTZ NOT NULL,
  count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, action, window_start)
);

-- +goose Down
DROP TABLE IF EXISTS book_club_poll_votes;
DROP TABLE IF EXISTS book_club_usage;
DROP TABLE IF EXISTS book_club_poll_options;
DROP TABLE IF EXISTS book_club_polls;
DROP TABLE IF EXISTS book_club_prompt_responses;
DROP TABLE IF EXISTS book_club_prompts;
DROP TABLE IF EXISTS book_club_member_progress;
DROP TABLE IF EXISTS book_club_members;
DROP TABLE IF EXISTS book_clubs;
