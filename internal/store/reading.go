package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const defaultReadingHistoryLimit = 5
const maxReadingHistoryLimit = 50

type ReadingProgress struct {
	StoryID       string    `json:"storyId"`
	ChapterID     string    `json:"chapterId,omitempty"`
	ScrollPercent float64   `json:"scrollPercent"`
	LastReadAt    time.Time `json:"lastReadAt,omitempty"`
}

type ReadingHistoryItem struct {
	ReadingProgress
	ChapterIndex  int    `json:"chapterIndex"`
	StoryTitle    string `json:"storyTitle"`
	StoryAuthor   string `json:"storyAuthor"`
	CoverImageURL string `json:"coverImageUrl"`
	ThumbnailURL  string `json:"thumbnailUrl"`
	TotalChapters int    `json:"totalChapters"`
}

type ReadingProgressInput struct {
	ChapterID     string  `json:"chapterId"`
	ScrollPercent float64 `json:"scrollPercent"`
}

func (s *Store) GetReadingProgress(ctx context.Context, userID, storyID string) (ReadingProgress, error) {
	if err := s.publicStoryExists(ctx, storyID); err != nil {
		return ReadingProgress{}, err
	}
	var x ReadingProgress
	var chapterID uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT story_id, chapter_id, scroll_percent, last_read_at FROM reading_progress WHERE user_id=$1 AND story_id=$2`, userID, storyID).Scan(&x.StoryID, &chapterID, &x.ScrollPercent, &x.LastReadAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReadingProgress{StoryID: storyID, ScrollPercent: 0}, nil
	}
	x.ChapterID = chapterID.String()
	return x, err
}

func (s *Store) PutReadingProgress(ctx context.Context, userID, storyID string, in ReadingProgressInput) (ReadingProgress, error) {
	if _, err := uuid.Parse(storyID); err != nil || in.ScrollPercent < 0 || in.ScrollPercent > 1 {
		return ReadingProgress{}, ErrValidation
	}
	chapterID, err := uuid.Parse(in.ChapterID)
	if err != nil {
		return ReadingProgress{}, ErrValidation
	}
	var x ReadingProgress
	var storedChapterID uuid.UUID
	err = s.db.QueryRow(ctx, `INSERT INTO reading_progress (user_id, story_id, chapter_id, scroll_percent, last_read_at)
SELECT $1, c.story_id, c.id, $3, now() FROM chapters c JOIN stories st ON st.id=c.story_id
WHERE c.story_id=$2 AND c.id=$4 AND st.is_published
ON CONFLICT (user_id, story_id) DO UPDATE SET chapter_id=EXCLUDED.chapter_id, scroll_percent=EXCLUDED.scroll_percent, last_read_at=EXCLUDED.last_read_at
RETURNING story_id, chapter_id, scroll_percent, last_read_at`, userID, storyID, in.ScrollPercent, chapterID).Scan(&x.StoryID, &storedChapterID, &x.ScrollPercent, &x.LastReadAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReadingProgress{}, ErrNotFound
	}
	x.ChapterID = storedChapterID.String()
	return x, err
}

func (s *Store) ListReadingHistory(ctx context.Context, userID string, limit int) ([]ReadingHistoryItem, error) {
	if limit <= 0 {
		limit = defaultReadingHistoryLimit
	}
	if limit > maxReadingHistoryLimit {
		limit = maxReadingHistoryLimit
	}
	rows, err := s.db.Query(ctx, `SELECT rp.story_id, rp.chapter_id, rp.scroll_percent, rp.last_read_at,
  (SELECT count(*) FROM chapters earlier WHERE earlier.story_id=st.id AND earlier.position < saved_ch.position),
  st.title, COALESCE(pp.username, st.author_name), COALESCE(st.cover_image_url,''), COALESCE(st.thumbnail_url,''), COUNT(c.id)
FROM reading_progress rp
JOIN stories st ON st.id=rp.story_id AND st.is_published
JOIN chapters saved_ch ON saved_ch.id=rp.chapter_id
JOIN chapters c ON c.story_id=st.id
LEFT JOIN public_profiles pp ON pp.user_id=st.owner_id
WHERE rp.user_id=$1
GROUP BY rp.user_id, rp.story_id, rp.chapter_id, rp.scroll_percent, rp.last_read_at, st.id, pp.username, saved_ch.position
ORDER BY rp.last_read_at DESC, rp.story_id DESC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ReadingHistoryItem{}
	for rows.Next() {
		var x ReadingHistoryItem
		var storyID, chapterID uuid.UUID
		if err := rows.Scan(&storyID, &chapterID, &x.ScrollPercent, &x.LastReadAt, &x.ChapterIndex, &x.StoryTitle, &x.StoryAuthor, &x.CoverImageURL, &x.ThumbnailURL, &x.TotalChapters); err != nil {
			return nil, err
		}
		x.StoryID, x.ChapterID = storyID.String(), chapterID.String()
		items = append(items, x)
	}
	return items, rows.Err()
}

func (s *Store) ClearReadingHistory(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM reading_progress WHERE user_id=$1`, userID)
	return err
}
