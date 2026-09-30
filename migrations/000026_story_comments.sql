-- +goose Up
DROP TABLE chapter_comment_likes;
DROP TABLE chapter_comments;

CREATE TABLE story_comments (
  id UUID PRIMARY KEY,
  story_id UUID NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  parent_id UUID REFERENCES story_comments(id) ON DELETE CASCADE,
  message TEXT NOT NULL CHECK (char_length(message) BETWEEN 1 AND 10000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX story_comments_story_created_idx
  ON story_comments (story_id, created_at, id);

CREATE TABLE story_comment_likes (
  comment_id UUID NOT NULL REFERENCES story_comments(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  PRIMARY KEY (comment_id, user_id)
);

-- +goose Down
DROP TABLE story_comment_likes;
DROP TABLE story_comments;

CREATE TABLE chapter_comments (
  id UUID PRIMARY KEY,
  chapter_id UUID NOT NULL REFERENCES chapters(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  parent_id UUID REFERENCES chapter_comments(id) ON DELETE CASCADE,
  message TEXT NOT NULL CHECK (char_length(message) BETWEEN 1 AND 10000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX chapter_comments_chapter_created_idx
  ON chapter_comments (chapter_id, created_at, id);
CREATE TABLE chapter_comment_likes (
  comment_id UUID NOT NULL REFERENCES chapter_comments(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  PRIMARY KEY (comment_id, user_id)
);
