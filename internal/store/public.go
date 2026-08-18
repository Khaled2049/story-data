package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const defaultPublicStoryPageSize = 24
const maxPublicStoryPageSize = 50

// PublicStory contains only data intentionally exposed to anonymous readers.
type PublicStory struct {
	ID             string    `json:"id"`
	AuthorID       string    `json:"authorId"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	AuthorName     string    `json:"authorName"`
	Category       string    `json:"category"`
	TargetAudience string    `json:"targetAudience"`
	Language       string    `json:"language"`
	Copyright      string    `json:"copyright"`
	CoverImageURL  string    `json:"coverImageUrl"`
	ThumbnailURL   string    `json:"thumbnailUrl"`
	Tags           []string  `json:"tags"`
	ChapterCount   int       `json:"chapterCount"`
	Views          int64     `json:"views"`
	LikeCount      int64     `json:"likeCount"`
	AverageRating  *float64  `json:"averageRating,omitempty"`
	RatingsCount   int       `json:"ratingsCount"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type PublicChapter struct {
	ID        string    `json:"id"`
	StoryID   string    `json:"storyId"`
	Title     string    `json:"title"`
	Content   string    `json:"content,omitempty"`
	Position  float64   `json:"position"`
	WordCount int       `json:"wordCount"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type PublicStoryDetail struct {
	Story    PublicStory     `json:"story"`
	Chapters []PublicChapter `json:"chapters"`
}

type PublicStoryPage struct {
	Stories    []PublicStory `json:"stories"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type publicStoryCursor struct {
	UpdatedAt time.Time `json:"updatedAt"`
	ID        string    `json:"id"`
}

func (s *Store) ListPublicStories(ctx context.Context, category, cursor string, pageSize int) (PublicStoryPage, error) {
	if pageSize <= 0 {
		pageSize = defaultPublicStoryPageSize
	}
	if pageSize > maxPublicStoryPageSize {
		pageSize = maxPublicStoryPageSize
	}
	var after *publicStoryCursor
	if cursor != "" {
		parsed, err := decodePublicStoryCursor(cursor)
		if err != nil {
			return PublicStoryPage{}, ErrValidation
		}
		after = &parsed
	}

	args := []any{}
	where := "WHERE s.is_published"
	if category = strings.TrimSpace(category); category != "" {
		args = append(args, category)
		where += fmt.Sprintf(" AND s.category=$%d", len(args))
	}
	if after != nil {
		args = append(args, after.UpdatedAt, mustUUID(after.ID))
		where += fmt.Sprintf(" AND (s.updated_at, s.id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, pageSize+1)
	query := `SELECT s.id, s.owner_id, s.title, s.description, s.author_name,
  COALESCE(s.category,''), COALESCE(s.target_audience,''), COALESCE(s.language,''),
  COALESCE(s.copyright,''), COALESCE(s.cover_image_url,''), COALESCE(s.thumbnail_url,''),
  s.views, s.created_at, s.updated_at,
  (SELECT count(*) FROM story_likes sl WHERE sl.story_id=s.id),
  (SELECT round(avg(sr.rating)::numeric, 1) FROM story_ratings sr WHERE sr.story_id=s.id),
  (SELECT count(*) FROM story_ratings sr WHERE sr.story_id=s.id),
  COALESCE(array_agg(DISTINCT st.tag ORDER BY st.tag) FILTER (WHERE st.tag IS NOT NULL), '{}'),
  COUNT(DISTINCT c.id)
FROM stories s
LEFT JOIN story_tags st ON st.story_id=s.id
LEFT JOIN chapters c ON c.story_id=s.id
` + where + `
GROUP BY s.id
ORDER BY s.updated_at DESC, s.id DESC
LIMIT $` + fmt.Sprint(len(args))
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return PublicStoryPage{}, err
	}
	defer rows.Close()
	stories := []PublicStory{}
	for rows.Next() {
		story, err := scanPublicStory(rows)
		if err != nil {
			return PublicStoryPage{}, err
		}
		stories = append(stories, story)
	}
	if err := rows.Err(); err != nil {
		return PublicStoryPage{}, err
	}
	page := PublicStoryPage{Stories: stories}
	if len(stories) > pageSize {
		stories = stories[:pageSize]
		page.Stories = stories
		page.NextCursor, _ = encodePublicStoryCursor(stories[len(stories)-1])
	}
	return page, nil
}

func (s *Store) GetPublicStory(ctx context.Context, storyID string) (PublicStoryDetail, error) {
	story, err := s.publicStory(ctx, storyID)
	if err != nil {
		return PublicStoryDetail{}, err
	}
	chapters, err := s.publicChapters(ctx, storyID, false)
	if err != nil {
		return PublicStoryDetail{}, err
	}
	return PublicStoryDetail{Story: story, Chapters: chapters}, nil
}

func (s *Store) GetPublicChapter(ctx context.Context, storyID, chapterID string) (PublicChapter, error) {
	if err := s.publicStoryExists(ctx, storyID); err != nil {
		return PublicChapter{}, err
	}
	row := s.db.QueryRow(ctx, `SELECT id, story_id, title, content, position, word_count, created_at, updated_at FROM chapters WHERE story_id=$1 AND id=$2`, storyID, chapterID)
	chapter, err := scanPublicChapter(row, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicChapter{}, ErrNotFound
	}
	return chapter, err
}

func (s *Store) publicStoryExists(ctx context.Context, storyID string) error {
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stories WHERE id=$1 AND is_published)`, storyID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// IncrementPublicStoryViews counts one reader once a day. viewerKey identifies
// the reader — a uid when signed in, a salted hash of their address otherwise
// — and the insert is what decides whether the counter moves: repeats collide
// on the primary key and are no-ops. Views drive discovery ranking, and this
// endpoint takes no credential, so an uncounted duplicate is the entire point
// rather than an optimization.
//
// A story that exists but has already been counted for this reader still
// returns nil: the caller asked for their view to be recorded, and it is.
func (s *Store) IncrementPublicStoryViews(ctx context.Context, storyID, viewerKey string) error {
	if viewerKey == "" {
		return ErrValidation
	}
	if err := s.publicStoryExists(ctx, storyID); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	hit, err := tx.Exec(ctx, `INSERT INTO public_story_view_hits(story_id,viewer_key,day)
VALUES($1,$2,current_date) ON CONFLICT DO NOTHING`, storyID, viewerKey)
	if err != nil {
		return err
	}
	if hit.RowsAffected() == 0 {
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE stories SET views=views+1 WHERE id=$1 AND is_published`, storyID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) publicStory(ctx context.Context, storyID string) (PublicStory, error) {
	row := s.db.QueryRow(ctx, `SELECT s.id, s.owner_id, s.title, s.description, s.author_name,
  COALESCE(s.category,''), COALESCE(s.target_audience,''), COALESCE(s.language,''),
  COALESCE(s.copyright,''), COALESCE(s.cover_image_url,''), COALESCE(s.thumbnail_url,''),
  s.views, s.created_at, s.updated_at,
  (SELECT count(*) FROM story_likes sl WHERE sl.story_id=s.id),
  (SELECT round(avg(sr.rating)::numeric, 1) FROM story_ratings sr WHERE sr.story_id=s.id),
  (SELECT count(*) FROM story_ratings sr WHERE sr.story_id=s.id),
  COALESCE(array_agg(DISTINCT st.tag ORDER BY st.tag) FILTER (WHERE st.tag IS NOT NULL), '{}'),
  COUNT(DISTINCT c.id)
FROM stories s
LEFT JOIN story_tags st ON st.story_id=s.id
LEFT JOIN chapters c ON c.story_id=s.id
WHERE s.id=$1 AND s.is_published
GROUP BY s.id`, storyID)
	story, err := scanPublicStory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicStory{}, ErrNotFound
	}
	return story, err
}

func (s *Store) publicChapters(ctx context.Context, storyID string, content bool) ([]PublicChapter, error) {
	selectContent := "''"
	if content {
		selectContent = "content"
	}
	rows, err := s.db.Query(ctx, `SELECT id, story_id, title, `+selectContent+`, position, word_count, created_at, updated_at FROM chapters WHERE story_id=$1 ORDER BY position`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chapters := []PublicChapter{}
	for rows.Next() {
		chapter, err := scanPublicChapter(rows, content)
		if err != nil {
			return nil, err
		}
		chapters = append(chapters, chapter)
	}
	return chapters, rows.Err()
}

func scanPublicStory(row pgx.Row) (PublicStory, error) {
	var x PublicStory
	var id uuid.UUID
	err := row.Scan(&id, &x.AuthorID, &x.Title, &x.Description, &x.AuthorName, &x.Category, &x.TargetAudience, &x.Language, &x.Copyright, &x.CoverImageURL, &x.ThumbnailURL, &x.Views, &x.CreatedAt, &x.UpdatedAt, &x.LikeCount, &x.AverageRating, &x.RatingsCount, &x.Tags, &x.ChapterCount)
	x.ID = id.String()
	return x, err
}

func scanPublicChapter(row pgx.Row, includeContent bool) (PublicChapter, error) {
	var x PublicChapter
	var id, storyID uuid.UUID
	err := row.Scan(&id, &storyID, &x.Title, &x.Content, &x.Position, &x.WordCount, &x.CreatedAt, &x.UpdatedAt)
	x.ID, x.StoryID = id.String(), storyID.String()
	if !includeContent {
		x.Content = ""
	}
	return x, err
}

func encodePublicStoryCursor(story PublicStory) (string, error) {
	bytes, err := json.Marshal(publicStoryCursor{UpdatedAt: story.UpdatedAt, ID: story.ID})
	return base64.RawURLEncoding.EncodeToString(bytes), err
}

func decodePublicStoryCursor(value string) (publicStoryCursor, error) {
	bytes, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return publicStoryCursor{}, err
	}
	var cursor publicStoryCursor
	if err := json.Unmarshal(bytes, &cursor); err != nil || cursor.UpdatedAt.IsZero() {
		return publicStoryCursor{}, ErrValidation
	}
	if _, err := uuid.Parse(cursor.ID); err != nil {
		return publicStoryCursor{}, err
	}
	return cursor, nil
}
