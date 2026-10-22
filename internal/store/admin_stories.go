package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type AdminStoryItem struct {
	ID               string    `json:"id"`
	OwnerID          string    `json:"ownerId"`
	Title            string    `json:"title"`
	AuthorName       string    `json:"authorName"`
	Published        bool      `json:"published"`
	CompletionStatus string    `json:"completionStatus"`
	Revision         int64     `json:"revision"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	ChapterCount     int       `json:"chapterCount"`
}

type AdminStoryPage struct {
	Stories    []AdminStoryItem `json:"stories"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func (s *Store) ListAdminStories(ctx context.Context, admin bool, search, status, cursor string, limit int) (AdminStoryPage, error) {
	if !admin {
		return AdminStoryPage{}, ErrForbidden
	}
	search = strings.TrimSpace(search)
	if len(search) > 500 {
		return AdminStoryPage{}, ErrValidation
	}
	if status != "" && status != "draft" && status != "ongoing" && status != "completed" {
		return AdminStoryPage{}, ErrValidation
	}
	if limit <= 0 {
		limit = 24
	}
	if limit > 50 {
		limit = 50
	}
	var afterTime *time.Time
	var afterID *string
	if cursor != "" {
		after, err := decodePublicStoryCursor(cursor)
		if err != nil {
			return AdminStoryPage{}, ErrValidation
		}
		afterTime, afterID = &after.UpdatedAt, &after.ID
	}
	// Bound the story selection before counting chapters. No chapter bodies are
	// transferred until an admin explicitly opens one for review.
	rows, err := s.db.Query(ctx, `WITH page AS (
		SELECT id, owner_id, title, author_name, is_published, completion_status, revision, created_at, updated_at
		FROM stories WHERE
		($1='' OR strpos(lower(title),lower($1))>0 OR strpos(lower(author_name),lower($1))>0 OR owner_id=$1 OR id::text=$1)
		AND ($2='' OR ($2='draft' AND NOT is_published) OR (is_published AND completion_status=$2))
		AND ($3::timestamptz IS NULL OR (updated_at,id)<($3,$4::uuid))
		ORDER BY updated_at DESC,id DESC LIMIT $5
	) SELECT page.*, (SELECT count(*) FROM chapters c WHERE c.story_id=page.id)
	FROM page ORDER BY updated_at DESC,id DESC`, search, status, afterTime, afterID, limit+1)
	if err != nil {
		return AdminStoryPage{}, err
	}
	defer rows.Close()
	page := AdminStoryPage{Stories: []AdminStoryItem{}}
	for rows.Next() {
		var x AdminStoryItem
		if err := rows.Scan(&x.ID, &x.OwnerID, &x.Title, &x.AuthorName, &x.Published, &x.CompletionStatus, &x.Revision, &x.CreatedAt, &x.UpdatedAt, &x.ChapterCount); err != nil {
			return AdminStoryPage{}, err
		}
		page.Stories = append(page.Stories, x)
	}
	if err := rows.Err(); err != nil {
		return AdminStoryPage{}, err
	}
	if len(page.Stories) > limit {
		last := page.Stories[limit-1]
		page.NextCursor, err = encodeStoryListCursor(last.UpdatedAt, last.ID)
		page.Stories = page.Stories[:limit]
	}
	return page, err
}

// Only the verified moderation path may resolve an owner on someone else's
// behalf. Reusing owner operations preserves deletion constraints and revision
// checks without relaxing access on any existing API.
func (s *Store) adminStoryOwner(ctx context.Context, admin bool, id string) (string, error) {
	if !admin {
		return "", ErrForbidden
	}
	var owner string
	err := s.db.QueryRow(ctx, `SELECT owner_id FROM stories WHERE id=$1`, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return owner, err
}

// Every admin read of a story or chapter body is recorded; the content is not
// returned when the record cannot be written.
func (s *Store) GetAdminStory(ctx context.Context, admin bool, actor, id string) (Story, error) {
	owner, err := s.adminStoryOwner(ctx, admin, id)
	if err != nil {
		return Story{}, err
	}
	story, err := s.GetStory(ctx, id, owner)
	if err != nil {
		return Story{}, err
	}
	return story, writeAdminAudit(ctx, s.db, actor, "story.view", id, map[string]any{"ownerId": owner, "published": story.Published})
}

func (s *Store) ListAdminChapterIndex(ctx context.Context, admin bool, id string) ([]Chapter, error) {
	owner, err := s.adminStoryOwner(ctx, admin, id)
	if err != nil {
		return nil, err
	}
	return s.ListChapterIndex(ctx, id, owner)
}

func (s *Store) GetAdminChapter(ctx context.Context, admin bool, actor, storyID, id string) (Chapter, error) {
	owner, err := s.adminStoryOwner(ctx, admin, storyID)
	if err != nil {
		return Chapter{}, err
	}
	chapter, err := s.GetChapter(ctx, storyID, id, owner)
	if err != nil {
		return Chapter{}, err
	}
	return chapter, writeAdminAudit(ctx, s.db, actor, "chapter.view", id, map[string]any{"storyId": storyID, "ownerId": owner, "released": chapter.PublishedAt != nil})
}

func (s *Store) DeleteAdminStory(ctx context.Context, admin bool, actor, id string, rev int64) error {
	owner, err := s.adminStoryOwner(ctx, admin, id)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.deleteStory(ctx, tx, id, owner, rev); err != nil {
		return err
	}
	if err := writeAdminAudit(ctx, tx, actor, "story.delete", id, map[string]any{"ownerId": owner, "revision": rev}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
