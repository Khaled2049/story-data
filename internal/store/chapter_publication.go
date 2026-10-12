package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const chapterColumns = `id, story_id, title, content, position, word_count, revision, created_at, updated_at, published_at`

// SetChapterPublication releases a draft chapter to readers or pulls a
// released one back. rev is the chapter revision the author is looking at, so
// text changed elsewhere since is not released unseen; the revision itself
// does not move, because the content did not.
//
// Releasing a chapter that is already out keeps its original published_at.
func (s *Store) SetChapterPublication(ctx context.Context, storyID, id, owner string, rev int64, published bool) (Chapter, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Chapter{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockOwnedStory(ctx, tx, storyID, owner); err != nil {
		return Chapter{}, err
	}
	chapter, err := scanChapter(tx.QueryRow(ctx, `SELECT `+chapterColumns+` FROM chapters WHERE id=$1 AND story_id=$2`, id, storyID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Chapter{}, ErrNotFound
	}
	if err != nil {
		return Chapter{}, err
	}
	if chapter.Revision != rev {
		return Chapter{}, ErrConflict
	}
	if published == (chapter.PublishedAt != nil) {
		return chapter, nil
	}
	if published && chapter.WordCount == 0 {
		return Chapter{}, sentinelErrf(ErrValidation, "write something in this chapter before releasing it")
	}
	chapter, err = scanChapter(tx.QueryRow(ctx, `UPDATE chapters SET published_at=CASE WHEN $3::boolean THEN now() END WHERE id=$1 AND story_id=$2 RETURNING `+chapterColumns, id, storyID, published))
	if err != nil {
		return Chapter{}, err
	}
	// A release is the next chapter the author promised, so the promise is spent.
	if _, err = tx.Exec(ctx, `UPDATE stories SET public_updated_at=now(), next_chapter_on=CASE WHEN $2::boolean THEN NULL ELSE next_chapter_on END WHERE id=$1`, storyID, published); err != nil {
		return Chapter{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Chapter{}, err
	}
	return chapter, nil
}

// releaseWrittenChapters runs when a story is first published. If the author
// has not chosen which chapters are ready, every chapter with text goes out,
// so publishing a finished manuscript stays a single action.
func releaseWrittenChapters(ctx context.Context, tx pgx.Tx, storyID string) error {
	_, err := tx.Exec(ctx, `UPDATE chapters SET published_at=now() WHERE story_id=$1 AND word_count>0
  AND NOT EXISTS (SELECT 1 FROM chapters r WHERE r.story_id=$1 AND r.published_at IS NOT NULL)`, storyID)
	return err
}
