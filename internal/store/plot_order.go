package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Two orders apply to plot events and neither stands in for the other:
// `position` within a plot line, and `chapter_position` among a chapter's
// planned events across plot lines.

// lockChapterOrder serializes writes to one chapter's event order, so two
// appends can't both take max+1 and trip the unique index.
func lockChapterOrder(ctx context.Context, tx pgx.Tx, chapterID string) error {
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('plot-chapter:' || $1, 0))`, chapterID)
	return e
}

// appendToChapter returns the position after a chapter's last event, or nil
// for an unassigned event.
func appendToChapter(ctx context.Context, tx pgx.Tx, chapterID *string) (*float64, error) {
	if chapterID == nil {
		return nil, nil
	}
	if e := lockChapterOrder(ctx, tx, *chapterID); e != nil {
		return nil, e
	}
	var next float64
	e := tx.QueryRow(ctx, `SELECT COALESCE(max(chapter_position)+1, 0)::float8 FROM plot_events WHERE chapter_id=$1`, *chapterID).Scan(&next)
	return &next, e
}

func sameChapter(was *uuid.UUID, now *string) bool {
	if was == nil || now == nil {
		return was == nil && now == nil
	}
	return was.String() == *now
}

// ReorderChapterEvents sets the order of a chapter's planned events.
// orderedIDs must name every event already in the chapter, or the caller's
// view is stale and the request is a conflict. It may also name events from
// other chapters or none: those move in, in the same transaction, so a drop
// into a chapter at a given place is one call. A moved-in event's chapterId
// changed, which is content, so its revision is bumped; a pure reorder bumps
// nothing. Returns every plot line, since the moves can span several.
func (s *Store) ReorderChapterEvents(ctx context.Context, storyID, uid, chapterID string, orderedIDs []string) ([]PlotLine, error) {
	if e := s.owner(ctx, storyID, uid); e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	for _, id := range orderedIDs {
		if _, e := uuid.Parse(id); e != nil || seen[id] {
			return nil, fmt.Errorf("%w: orderedIds must be distinct event ids", ErrValidation)
		}
		seen[id] = true
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = eventChapter(ctx, tx, storyID, &chapterID); e != nil {
		return nil, e
	}
	if e = lockChapterOrder(ctx, tx, chapterID); e != nil {
		return nil, e
	}

	rows, e := tx.Query(ctx, `SELECT e.id,e.chapter_id FROM plot_events e JOIN plot_lines l ON l.id=e.plot_line_id WHERE l.story_id=$1 AND (e.chapter_id=$2 OR e.id=ANY($3::uuid[])) FOR UPDATE OF e`, storyID, chapterID, orderedIDs)
	if e != nil {
		return nil, e
	}
	inChapter := map[string]bool{}
	found := map[string]bool{}
	for rows.Next() {
		var id uuid.UUID
		var chapter *uuid.UUID
		if e = rows.Scan(&id, &chapter); e != nil {
			rows.Close()
			return nil, e
		}
		found[id.String()] = true
		if chapter != nil && chapter.String() == chapterID {
			inChapter[id.String()] = true
		}
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	for _, id := range orderedIDs {
		if !found[id] {
			return nil, fmt.Errorf("%w: event is not in this story", ErrValidation)
		}
	}
	for id := range inChapter {
		if !seen[id] {
			return nil, conflictErrf("the chapter's events changed; reload and try again")
		}
	}

	// Two passes around the unique (chapter_id, chapter_position) index.
	for i, id := range orderedIDs {
		movedIn := !inChapter[id]
		if _, e = tx.Exec(ctx, `UPDATE plot_events SET chapter_id=$1,chapter_position=$2,revision=revision+CASE WHEN $3::boolean THEN 1 ELSE 0 END,updated_at=CASE WHEN $3::boolean THEN now() ELSE updated_at END WHERE id=$4`, chapterID, i+1000000, movedIn, id); e != nil {
			return nil, e
		}
	}
	for i, id := range orderedIDs {
		if _, e = tx.Exec(ctx, `UPDATE plot_events SET chapter_position=$1 WHERE id=$2`, i, id); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return s.ListPlots(ctx, storyID, uid)
}

// MoveEvent moves an event to another plot line at toIndex, renumbering both
// lines in one transaction. Its fields, chapter, links and revision are
// untouched: the plot line is not part of an event's content.
func (s *Store) MoveEvent(ctx context.Context, storyID, fromLine, eventID, uid string, rev int64, toLine string, toIndex int) ([]PlotLine, error) {
	if e := s.eventLine(ctx, storyID, fromLine, uid); e != nil {
		return nil, e
	}
	if e := s.eventLine(ctx, storyID, toLine, uid); e != nil {
		if errors.Is(e, ErrNotFound) {
			return nil, fmt.Errorf("%w: target plot line is not in this story", ErrValidation)
		}
		return nil, e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	// Lock both lines in id order, so two moves in opposite directions can't deadlock.
	first, second := fromLine, toLine
	if second < first {
		first, second = second, first
	}
	for _, id := range []string{first, second} {
		if _, e = tx.Exec(ctx, `SELECT 1 FROM plot_lines WHERE id=$1 FOR UPDATE`, id); e != nil {
			return nil, e
		}
	}
	var current int64
	if e = tx.QueryRow(ctx, `SELECT revision FROM plot_events WHERE id=$1 AND plot_line_id=$2 FOR UPDATE`, eventID, fromLine).Scan(&current); errors.Is(e, pgx.ErrNoRows) {
		return nil, s.classifyEventWrite(ctx, storyID, fromLine, eventID, uid)
	} else if e != nil {
		return nil, e
	}
	if current != rev {
		return nil, ErrConflict
	}

	var target []string
	rows, e := tx.Query(ctx, `SELECT id FROM plot_events WHERE plot_line_id=$1 AND id<>$2 ORDER BY position`, toLine, eventID)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id uuid.UUID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		target = append(target, id.String())
	}
	rows.Close()
	if fromLine != toLine && len(target) >= maxEntitiesPerStory {
		return nil, ErrLimit
	}
	if toIndex < 0 {
		toIndex = 0
	}
	if toIndex > len(target) {
		toIndex = len(target)
	}
	ordered := append(append(append([]string{}, target[:toIndex]...), eventID), target[toIndex:]...)

	// A negative position can't collide with renumbering, which uses 0.. and 1000000..
	if _, e = tx.Exec(ctx, `UPDATE plot_events SET plot_line_id=$1,position=-1 WHERE id=$2`, toLine, eventID); e != nil {
		return nil, e
	}
	if e = s.renumberEvents(ctx, tx, toLine, ordered); e != nil {
		return nil, e
	}
	if fromLine != toLine {
		if e = s.renumberEvents(ctx, tx, fromLine, nil); e != nil {
			return nil, e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE plot_lines SET revision=revision+1,updated_at=now() WHERE id=ANY($1::uuid[])`, []string{fromLine, toLine}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return s.ListPlots(ctx, storyID, uid)
}
