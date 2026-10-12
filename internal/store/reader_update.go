package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	CompletionOngoing   = "ongoing"
	CompletionCompleted = "completed"

	maxReaderNoteChars = 280
)

// readerUpdateColumns follows the base story columns wherever scanStory reads them.
const readerUpdateColumns = `, completion_status, COALESCE(to_char(next_chapter_on,'YYYY-MM-DD'),''), reader_note, reader_note_updated_at`

// publicNextChapterOn hides a date that has passed, since an overdue promise
// reads as an abandoned story. A date carries no timezone, so it stays visible
// one extra UTC day: that covers an author anywhere west of Greenwich.
const publicNextChapterOn = `CASE WHEN s.next_chapter_on >= (now() AT TIME ZONE 'UTC')::date - 1 THEN to_char(s.next_chapter_on,'YYYY-MM-DD') ELSE '' END`

// ReaderUpdateInput is what an author tells readers about a story's progress.
// It replaces all three values; NextChapterOn is YYYY-MM-DD or empty.
type ReaderUpdateInput struct {
	CompletionStatus string `json:"completionStatus"`
	NextChapterOn    string `json:"nextChapterOn"`
	ReaderNote       string `json:"readerNote"`
}

// ValidCompletionStatus mirrors stories_completion_status_valid.
func ValidCompletionStatus(v string) bool {
	return v == CompletionOngoing || v == CompletionCompleted
}

// PutReaderUpdate deliberately leaves revision and updated_at alone: with no
// revision bump it cannot invalidate a story edit the author has open
// elsewhere. Only a change of status moves public_updated_at, so finishing a
// story resurfaces it in discovery and editing a note does not.
func (s *Store) PutReaderUpdate(ctx context.Context, id, owner string, in ReaderUpdateInput) (Story, error) {
	note := strings.TrimSpace(in.ReaderNote)
	if !ValidCompletionStatus(in.CompletionStatus) || !validText(note, maxReaderNoteChars) {
		return Story{}, ErrValidation
	}
	var nextOn *time.Time
	// A finished story has no next chapter to promise.
	if in.NextChapterOn != "" && in.CompletionStatus != CompletionCompleted {
		parsed, err := time.Parse(time.DateOnly, in.NextChapterOn)
		if err != nil {
			return Story{}, ErrValidation
		}
		nextOn = &parsed
	}
	row := s.db.QueryRow(ctx, `UPDATE stories SET completion_status=$1, next_chapter_on=$2::date,
  reader_note_updated_at=CASE WHEN $3='' THEN NULL WHEN reader_note IS DISTINCT FROM $3 THEN now() ELSE reader_note_updated_at END,
  reader_note=$3,
  public_updated_at=CASE WHEN completion_status IS DISTINCT FROM $1 THEN now() ELSE public_updated_at END
WHERE id=$4 AND owner_id=$5
RETURNING id, owner_id, title, description, author_name, is_published, COALESCE(category,''), COALESCE(target_audience,''), COALESCE(language,''), COALESCE(copyright,''), COALESCE(cover_image_url,''), COALESCE(thumbnail_url,''), paragraph_style, revision, created_at, updated_at`+readerUpdateColumns,
		in.CompletionStatus, nextOn, note, id, owner)
	story, err := scanStory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// classifyWrite's fallthrough is a stale revision, which cannot happen here.
		if err := s.classifyWrite(ctx, "stories", id, owner); !errors.Is(err, ErrConflict) {
			return Story{}, err
		}
		return Story{}, ErrNotFound
	}
	if err != nil {
		return Story{}, err
	}
	if err := s.hydrateTags(ctx, &story); err != nil {
		return Story{}, err
	}
	return story, nil
}
