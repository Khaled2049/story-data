-- +goose Up
-- Defence in depth behind the Go validators in internal/store/validate.go.
-- Until now the only bound on most of these columns was the 1 MB HTTP body
-- cap, and URL-shaped columns accepted any string at all — javascript:,
-- data:text/html and friends included.
--
-- Every constraint is added NOT VALID: PostgreSQL enforces it on insert and
-- update but does not scan the existing table. Migrations run at API startup
-- under an advisory lock, so a full validation pass that failed on one legacy
-- row would take the service down rather than protect it. Existing rows are
-- brought into line the next time they are written.
--
-- Lengths use char_length (runes), matching the Go side, so a limit means the
-- same thing whatever the script.

ALTER TABLE stories
  ADD CONSTRAINT stories_description_len CHECK (char_length(description) <= 5000) NOT VALID,
  ADD CONSTRAINT stories_author_name_len CHECK (char_length(author_name) <= 200) NOT VALID,
  ADD CONSTRAINT stories_category_len CHECK (char_length(category) <= 100) NOT VALID,
  ADD CONSTRAINT stories_target_audience_len CHECK (char_length(target_audience) <= 100) NOT VALID,
  ADD CONSTRAINT stories_language_len CHECK (char_length(language) <= 100) NOT VALID,
  ADD CONSTRAINT stories_copyright_len CHECK (char_length(copyright) <= 100) NOT VALID,
  ADD CONSTRAINT stories_cover_image_url_http CHECK (
    cover_image_url IS NULL OR (octet_length(cover_image_url) <= 2048 AND cover_image_url ~ '^https?://')) NOT VALID,
  ADD CONSTRAINT stories_thumbnail_url_http CHECK (
    thumbnail_url IS NULL OR (octet_length(thumbnail_url) <= 2048 AND thumbnail_url ~ '^https?://')) NOT VALID;

ALTER TABLE story_tags
  ADD CONSTRAINT story_tags_len CHECK (char_length(tag) <= 100) NOT VALID;

ALTER TABLE characters
  ADD CONSTRAINT characters_art_url_http CHECK (
    art_url IS NULL OR (octet_length(art_url) <= 2048 AND art_url ~ '^https?://')) NOT VALID;

ALTER TABLE places
  ADD CONSTRAINT places_image_url_http CHECK (
    image_url IS NULL OR (octet_length(image_url) <= 2048 AND image_url ~ '^https?://')) NOT VALID;

ALTER TABLE public_profiles
  ADD CONSTRAINT public_profiles_photo_url_http CHECK (
    photo_url = '' OR (octet_length(photo_url) <= 2048 AND photo_url ~ '^https?://')) NOT VALID;

ALTER TABLE competitions
  ADD CONSTRAINT competitions_title_len CHECK (char_length(title) BETWEEN 1 AND 500) NOT VALID,
  ADD CONSTRAINT competitions_description_len CHECK (char_length(description) <= 5000) NOT VALID,
  ADD CONSTRAINT competitions_category_len CHECK (char_length(category) <= 100) NOT VALID,
  ADD CONSTRAINT competitions_creator_name_len CHECK (char_length(creator_name) <= 200) NOT VALID;

ALTER TABLE book_clubs
  ADD CONSTRAINT book_clubs_description_len CHECK (char_length(description) <= 5000) NOT VALID,
  ADD CONSTRAINT book_clubs_image_http CHECK (
    image = '' OR (octet_length(image) <= 2048 AND image ~ '^https?://')) NOT VALID,
  ADD CONSTRAINT book_clubs_category_len CHECK (char_length(category) <= 100) NOT VALID,
  ADD CONSTRAINT book_clubs_activity_len CHECK (char_length(activity) <= 100) NOT VALID,
  ADD CONSTRAINT book_clubs_meetup_len CHECK (char_length(meetup) <= 200) NOT VALID;

-- +goose Down
ALTER TABLE book_clubs
  DROP CONSTRAINT IF EXISTS book_clubs_description_len,
  DROP CONSTRAINT IF EXISTS book_clubs_image_http,
  DROP CONSTRAINT IF EXISTS book_clubs_category_len,
  DROP CONSTRAINT IF EXISTS book_clubs_activity_len,
  DROP CONSTRAINT IF EXISTS book_clubs_meetup_len;
ALTER TABLE competitions
  DROP CONSTRAINT IF EXISTS competitions_title_len,
  DROP CONSTRAINT IF EXISTS competitions_description_len,
  DROP CONSTRAINT IF EXISTS competitions_category_len,
  DROP CONSTRAINT IF EXISTS competitions_creator_name_len;
ALTER TABLE public_profiles DROP CONSTRAINT IF EXISTS public_profiles_photo_url_http;
ALTER TABLE places DROP CONSTRAINT IF EXISTS places_image_url_http;
ALTER TABLE characters DROP CONSTRAINT IF EXISTS characters_art_url_http;
ALTER TABLE story_tags DROP CONSTRAINT IF EXISTS story_tags_len;
ALTER TABLE stories
  DROP CONSTRAINT IF EXISTS stories_description_len,
  DROP CONSTRAINT IF EXISTS stories_author_name_len,
  DROP CONSTRAINT IF EXISTS stories_category_len,
  DROP CONSTRAINT IF EXISTS stories_target_audience_len,
  DROP CONSTRAINT IF EXISTS stories_language_len,
  DROP CONSTRAINT IF EXISTS stories_copyright_len,
  DROP CONSTRAINT IF EXISTS stories_cover_image_url_http,
  DROP CONSTRAINT IF EXISTS stories_thumbnail_url_http;
