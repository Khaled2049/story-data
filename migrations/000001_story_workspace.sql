-- +goose Up
CREATE TABLE schema_migrations_guard (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE,
  CHECK (singleton)
);

CREATE TABLE stories (
  id UUID PRIMARY KEY,
  owner_id TEXT NOT NULL,
  title TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 500),
  description TEXT NOT NULL DEFAULT '',
  is_published BOOLEAN NOT NULL DEFAULT FALSE,
  author_name TEXT NOT NULL DEFAULT '',
  cover_image_url TEXT,
  thumbnail_url TEXT,
  category TEXT,
  target_audience TEXT,
  language TEXT,
  copyright TEXT,
  views BIGINT NOT NULL DEFAULT 0 CHECK (views >= 0),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX stories_owner_updated_idx ON stories (owner_id, updated_at DESC);
CREATE INDEX stories_public_updated_idx ON stories (updated_at DESC) WHERE is_published;

CREATE TABLE story_tags (
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  tag TEXT NOT NULL CHECK (char_length(tag) BETWEEN 1 AND 80),
  PRIMARY KEY (story_id, tag)
);

CREATE TABLE chapters (
  id UUID PRIMARY KEY,
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  title TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 500),
  content TEXT NOT NULL DEFAULT '' CHECK (char_length(content) <= 500000),
  position NUMERIC(20,10) NOT NULL,
  word_count INTEGER NOT NULL DEFAULT 0 CHECK (word_count >= 0),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (story_id, position)
);
CREATE INDEX chapters_story_position_idx ON chapters (story_id, position);

CREATE TABLE characters (
  id UUID PRIMARY KEY,
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  age INTEGER,
  art_url TEXT,
  soul TEXT,
  personality TEXT,
  voice TEXT,
  backstory TEXT,
  affiliations TEXT,
  notes TEXT,
  revision BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE character_relationships (
  character_id UUID NOT NULL REFERENCES characters(id) ON DELETE CASCADE,
  related_character_id UUID NOT NULL REFERENCES characters(id) ON DELETE CASCADE,
  relationship_type TEXT NOT NULL,
  description TEXT,
  PRIMARY KEY (character_id, related_character_id),
  CHECK (character_id <> related_character_id)
);

CREATE TABLE places (
  id UUID PRIMARY KEY,
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  image_url TEXT,
  description TEXT,
  atmosphere TEXT,
  geography TEXT,
  history TEXT,
  significance TEXT,
  notes TEXT,
  revision BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE plot_lines (
  id UUID PRIMARY KEY,
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  revision BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE plot_events (
  id UUID PRIMARY KEY,
  plot_line_id UUID NOT NULL REFERENCES plot_lines(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  content TEXT NOT NULL,
  location_id UUID REFERENCES places(id) ON DELETE SET NULL,
  chapter_id UUID REFERENCES chapters(id) ON DELETE SET NULL,
  tension_level SMALLINT NOT NULL DEFAULT 5 CHECK (tension_level BETWEEN 1 AND 10),
  pacing TEXT NOT NULL DEFAULT 'moderate',
  story_beat TEXT NOT NULL DEFAULT 'rising_action',
  emotional_tone TEXT,
  time_constraint JSONB,
  notes TEXT,
  position NUMERIC(20,10) NOT NULL,
  revision BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (plot_line_id, position)
);
CREATE TABLE plot_event_characters (
  plot_event_id UUID NOT NULL REFERENCES plot_events(id) ON DELETE CASCADE,
  character_id UUID NOT NULL REFERENCES characters(id) ON DELETE CASCADE,
  PRIMARY KEY (plot_event_id, character_id)
);
CREATE TABLE plot_event_dependencies (
  plot_event_id UUID NOT NULL REFERENCES plot_events(id) ON DELETE CASCADE,
  depends_on_event_id UUID NOT NULL REFERENCES plot_events(id) ON DELETE CASCADE,
  relationship_type TEXT NOT NULL,
  description TEXT,
  PRIMARY KEY (plot_event_id, depends_on_event_id),
  CHECK (plot_event_id <> depends_on_event_id)
);

CREATE TABLE story_likes (
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (story_id, user_id)
);
CREATE TABLE story_ratings (
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  rating SMALLINT NOT NULL CHECK (rating BETWEEN 1 AND 5),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (story_id, user_id)
);
CREATE TABLE chapter_comments (
  id UUID PRIMARY KEY,
  chapter_id UUID NOT NULL REFERENCES chapters(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  parent_id UUID REFERENCES chapter_comments(id) ON DELETE CASCADE,
  message TEXT NOT NULL CHECK (char_length(message) BETWEEN 1 AND 10000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE chapter_comment_likes (
  comment_id UUID NOT NULL REFERENCES chapter_comments(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  PRIMARY KEY (comment_id, user_id)
);

-- Durable source for Firestore vector indexing. A later scheduled delivery
-- worker claims these rows and calls the private agents indexing endpoint.
CREATE TABLE indexing_outbox (
  id UUID PRIMARY KEY,
  aggregate_type TEXT NOT NULL CHECK (aggregate_type IN ('chapter', 'character', 'place', 'plot_event')),
  aggregate_id UUID NOT NULL,
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  operation TEXT NOT NULL CHECK (operation IN ('upsert', 'delete')),
  revision BIGINT NOT NULL,
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  delivered_at TIMESTAMPTZ,
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX indexing_outbox_pending_idx ON indexing_outbox (available_at, created_at)
  WHERE delivered_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS indexing_outbox;
DROP TABLE IF EXISTS chapter_comment_likes;
DROP TABLE IF EXISTS chapter_comments;
DROP TABLE IF EXISTS story_ratings;
DROP TABLE IF EXISTS story_likes;
DROP TABLE IF EXISTS plot_event_dependencies;
DROP TABLE IF EXISTS plot_event_characters;
DROP TABLE IF EXISTS plot_events;
DROP TABLE IF EXISTS plot_lines;
DROP TABLE IF EXISTS places;
DROP TABLE IF EXISTS character_relationships;
DROP TABLE IF EXISTS characters;
DROP TABLE IF EXISTS chapters;
DROP TABLE IF EXISTS story_tags;
DROP TABLE IF EXISTS stories;
DROP TABLE IF EXISTS schema_migrations_guard;
