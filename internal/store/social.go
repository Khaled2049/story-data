package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type StorySocial struct {
	LikeCount     int64    `json:"likeCount"`
	AverageRating *float64 `json:"averageRating,omitempty"`
	RatingsCount  int      `json:"ratingsCount"`
}

type StorySocialMe struct {
	Liked  bool `json:"liked"`
	Rating *int `json:"rating,omitempty"`
}

type Comment struct {
	ID        string    `json:"id"`
	StoryID   string    `json:"storyId"`
	ChapterID string    `json:"chapterId"`
	Message   string    `json:"message"`
	UserID    string    `json:"userId"`
	ParentID  *string   `json:"parentId,omitempty"`
	LikeCount int64     `json:"likeCount"`
	LikedByMe bool      `json:"likedByMe"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type CommentInput struct {
	Message  string `json:"message"`
	ParentID string `json:"parentId"`
}

func (s *Store) StorySocialSummary(ctx context.Context, storyID string) (StorySocial, error) {
	if err := s.publicStoryExists(ctx, storyID); err != nil {
		return StorySocial{}, err
	}
	return s.storySocialSummary(ctx, storyID)
}

func (s *Store) StorySocialMe(ctx context.Context, storyID, userID string) (StorySocialMe, error) {
	if err := s.publicStoryExists(ctx, storyID); err != nil {
		return StorySocialMe{}, err
	}
	var out StorySocialMe
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM story_likes WHERE story_id=$1 AND user_id=$2)`, storyID, userID).Scan(&out.Liked); err != nil {
		return StorySocialMe{}, err
	}
	var rating int
	err := s.db.QueryRow(ctx, `SELECT rating FROM story_ratings WHERE story_id=$1 AND user_id=$2`, storyID, userID).Scan(&rating)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return StorySocialMe{}, err
	}
	out.Rating = &rating
	return out, nil
}

func (s *Store) SetStoryLike(ctx context.Context, storyID, userID string, liked bool) (StorySocial, error) {
	if err := s.publicStoryExists(ctx, storyID); err != nil {
		return StorySocial{}, err
	}
	if liked {
		_, err := s.db.Exec(ctx, `INSERT INTO story_likes (story_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, storyID, userID)
		if err != nil {
			return StorySocial{}, err
		}
	} else if _, err := s.db.Exec(ctx, `DELETE FROM story_likes WHERE story_id=$1 AND user_id=$2`, storyID, userID); err != nil {
		return StorySocial{}, err
	}
	return s.storySocialSummary(ctx, storyID)
}

func (s *Store) CreateStoryRating(ctx context.Context, storyID, userID string, rating int) (StorySocial, error) {
	if rating < 1 || rating > 5 {
		return StorySocial{}, ErrValidation
	}
	if err := s.publicStoryExists(ctx, storyID); err != nil {
		return StorySocial{}, err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO story_ratings (story_id, user_id, rating) VALUES ($1,$2,$3)`, storyID, userID, rating)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return StorySocial{}, ErrConflict
		}
		return StorySocial{}, err
	}
	return s.storySocialSummary(ctx, storyID)
}

// viewer may be empty for an anonymous reader, in which case likedByMe is
// false for every row. One join and aggregate rather than a count per comment:
// a chapter thread is the highest fan-out read in the app.
func (s *Store) ListPublicComments(ctx context.Context, storyID, chapterID, viewer string) ([]Comment, error) {
	if err := s.publicChapterExists(ctx, storyID, chapterID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		SELECT c.id, c.chapter_id, c.message, c.user_id, c.parent_id, c.created_at, c.updated_at,
		       count(l.user_id), COALESCE(bool_or(l.user_id = $2), false)
		FROM chapter_comments c
		LEFT JOIN chapter_comment_likes l ON l.comment_id = c.id
		WHERE c.chapter_id = $1
		GROUP BY c.id
		ORDER BY c.created_at, c.id`, chapterID, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	comments := []Comment{}
	for rows.Next() {
		comment, err := scanComment(rows, storyID)
		if err != nil {
			return nil, err
		}
		comments = append(comments, comment)
	}
	return comments, rows.Err()
}

// SetCommentLike is idempotent in both directions so a double-tap cannot 409.
func (s *Store) SetCommentLike(ctx context.Context, storyID, chapterID, commentID, userID string, liked bool) (Comment, error) {
	if err := s.publicChapterExists(ctx, storyID, chapterID); err != nil {
		return Comment{}, err
	}
	id, err := uuid.Parse(commentID)
	if err != nil {
		return Comment{}, ErrNotFound
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chapter_comments WHERE id=$1 AND chapter_id=$2)`, id, chapterID).Scan(&exists); err != nil {
		return Comment{}, err
	}
	if !exists {
		return Comment{}, ErrNotFound
	}
	if liked {
		_, err = s.db.Exec(ctx, `INSERT INTO chapter_comment_likes (comment_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, userID)
	} else {
		_, err = s.db.Exec(ctx, `DELETE FROM chapter_comment_likes WHERE comment_id=$1 AND user_id=$2`, id, userID)
	}
	if err != nil {
		return Comment{}, err
	}
	row := s.db.QueryRow(ctx, `
		SELECT c.id, c.chapter_id, c.message, c.user_id, c.parent_id, c.created_at, c.updated_at,
		       count(l.user_id), COALESCE(bool_or(l.user_id = $2), false)
		FROM chapter_comments c
		LEFT JOIN chapter_comment_likes l ON l.comment_id = c.id
		WHERE c.id = $1
		GROUP BY c.id`, id, userID)
	return scanComment(row, storyID)
}

func (s *Store) CreateComment(ctx context.Context, storyID, chapterID, userID string, input CommentInput) (Comment, error) {
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" || len(input.Message) > 10000 {
		return Comment{}, ErrValidation
	}
	if err := s.publicChapterExists(ctx, storyID, chapterID); err != nil {
		return Comment{}, err
	}
	var parent any
	if input.ParentID != "" {
		parentID, err := uuid.Parse(input.ParentID)
		if err != nil {
			return Comment{}, ErrNotFound
		}
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chapter_comments WHERE id=$1 AND chapter_id=$2)`, parentID, chapterID).Scan(&exists); err != nil {
			return Comment{}, err
		}
		if !exists {
			return Comment{}, ErrNotFound
		}
		parent = parentID
	}
	row := s.db.QueryRow(ctx, `INSERT INTO chapter_comments (id, chapter_id, user_id, parent_id, message) VALUES ($1,$2,$3,$4,$5) RETURNING id, chapter_id, message, user_id, parent_id, created_at, updated_at, 0::bigint, false`, uuid.New(), chapterID, userID, parent, input.Message)
	return scanComment(row, storyID)
}

func (s *Store) UpdateComment(ctx context.Context, storyID, chapterID, commentID, userID, message string) (Comment, error) {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > 10000 {
		return Comment{}, ErrValidation
	}
	if err := s.publicChapterExists(ctx, storyID, chapterID); err != nil {
		return Comment{}, err
	}
	row := s.db.QueryRow(ctx, `
		UPDATE chapter_comments SET message=$1, updated_at=now()
		WHERE id=$2 AND chapter_id=$3 AND user_id=$4
		RETURNING id, chapter_id, message, user_id, parent_id, created_at, updated_at,
		          (SELECT count(*) FROM chapter_comment_likes l WHERE l.comment_id = chapter_comments.id),
		          EXISTS(SELECT 1 FROM chapter_comment_likes l WHERE l.comment_id = chapter_comments.id AND l.user_id = $4)`, message, commentID, chapterID, userID)
	comment, err := scanComment(row, storyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Comment{}, s.classifyCommentWrite(ctx, chapterID, commentID, userID)
	}
	return comment, err
}

func (s *Store) DeleteComment(ctx context.Context, storyID, chapterID, commentID, userID string) error {
	if err := s.publicChapterExists(ctx, storyID, chapterID); err != nil {
		return err
	}
	result, err := s.db.Exec(ctx, `DELETE FROM chapter_comments WHERE id=$1 AND chapter_id=$2 AND user_id=$3`, commentID, chapterID, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return s.classifyCommentWrite(ctx, chapterID, commentID, userID)
	}
	return nil
}

func (s *Store) publicChapterExists(ctx context.Context, storyID, chapterID string) error {
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chapters c JOIN stories s ON s.id=c.story_id WHERE c.id=$1 AND c.story_id=$2 AND s.is_published)`, chapterID, storyID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func (s *Store) storySocialSummary(ctx context.Context, storyID string) (StorySocial, error) {
	var out StorySocial
	err := s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM story_likes WHERE story_id=$1), (SELECT round(avg(rating)::numeric, 1) FROM story_ratings WHERE story_id=$1), (SELECT count(*) FROM story_ratings WHERE story_id=$1)`, storyID).Scan(&out.LikeCount, &out.AverageRating, &out.RatingsCount)
	return out, err
}

func (s *Store) classifyCommentWrite(ctx context.Context, chapterID, commentID, userID string) error {
	var owner string
	err := s.db.QueryRow(ctx, `SELECT user_id FROM chapter_comments WHERE id=$1 AND chapter_id=$2`, commentID, chapterID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != userID {
		return ErrForbidden
	}
	return ErrConflict
}

func scanComment(row pgx.Row, storyID string) (Comment, error) {
	var out Comment
	var id, chapterID uuid.UUID
	var parentID *uuid.UUID
	err := row.Scan(&id, &chapterID, &out.Message, &out.UserID, &parentID, &out.CreatedAt, &out.UpdatedAt, &out.LikeCount, &out.LikedByMe)
	out.ID, out.StoryID, out.ChapterID = id.String(), storyID, chapterID.String()
	if parentID != nil {
		value := parentID.String()
		out.ParentID = &value
	}
	return out, err
}
