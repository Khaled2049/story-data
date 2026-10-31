package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	storyVoiceNoteLimit    = 12
	storyVoiceNoteChars    = 2000
	storyVoiceTotalChars   = 4000
	storyVoiceHistoryLimit = 50
)

type StoryVoiceNote struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Origin string `json:"origin"`
	Pinned bool   `json:"pinned"`
}

type StoryVoiceInput struct {
	PointOfView      string                `json:"pointOfView"`
	Tense            string                `json:"tense"`
	Notes            []StoryVoiceNote      `json:"notes"`
	LearnFromWriting *bool                 `json:"learnFromWriting,omitempty"`
	References       []StoryVoiceReference `json:"references"`
}

type StoryVoiceReference struct {
	ChapterID      string `json:"chapterId"`
	SourceRevision int64  `json:"sourceRevision"`
	Start          int    `json:"start"`
	End            int    `json:"end"`
}

// Revision zero means no profile has been saved. The first save uses If-Match: 0.
type StoryVoiceProfile struct {
	StoryID          string                `json:"storyId"`
	PointOfView      string                `json:"pointOfView"`
	Tense            string                `json:"tense"`
	Notes            []StoryVoiceNote      `json:"notes"`
	LearnFromWriting bool                  `json:"learnFromWriting"`
	References       []StoryVoiceReference `json:"references"`
	Revision         int64                 `json:"revision"`
	UpdatedAt        *time.Time            `json:"updatedAt,omitempty"`
}

type StoryVoiceChange struct {
	ID             string                `json:"id"`
	RevisionBefore int64                 `json:"revisionBefore"`
	RevisionAfter  int64                 `json:"revisionAfter"`
	Action         string                `json:"action"`
	Before         StoryVoiceInput       `json:"before"`
	After          StoryVoiceInput       `json:"after"`
	UndoOf         *string               `json:"undoOf,omitempty"`
	Reason         string                `json:"reason,omitempty"`
	Evidence       []StoryVoiceReference `json:"evidence,omitempty"`
	CreatedAt      time.Time             `json:"createdAt"`
}

type AgentVoiceOperation struct {
	Operation      string                `json:"operation"`
	NoteID         string                `json:"noteId,omitempty"`
	Text           string                `json:"text,omitempty"`
	Reason         string                `json:"reason"`
	Source         string                `json:"source"`
	Evidence       []StoryVoiceReference `json:"evidence"`
	IdempotencyKey string                `json:"idempotencyKey"`
}

type AgentVoiceResult struct {
	ChangeID string `json:"changeId,omitempty"`
	Revision int64  `json:"revision"`
	Applied  bool   `json:"applied"`
}

func defaultStoryVoice(storyID string) StoryVoiceProfile {
	return StoryVoiceProfile{StoryID: storyID, PointOfView: "unspecified", Tense: "unspecified", Notes: []StoryVoiceNote{}, LearnFromWriting: true, References: []StoryVoiceReference{}}
}

func voiceInput(x StoryVoiceProfile) StoryVoiceInput {
	return StoryVoiceInput{PointOfView: x.PointOfView, Tense: x.Tense, Notes: x.Notes, LearnFromWriting: &x.LearnFromWriting, References: x.References}
}

func validStoryVoice(in StoryVoiceInput, existing []StoryVoiceNote) (StoryVoiceInput, error) {
	switch in.PointOfView {
	case "unspecified", "first", "close_third", "omniscient", "varies":
	default:
		return in, sentinelErrf(ErrValidation, "invalid point of view")
	}
	switch in.Tense {
	case "unspecified", "past", "present", "varies":
	default:
		return in, sentinelErrf(ErrValidation, "invalid tense")
	}
	if in.Notes == nil || len(in.Notes) > storyVoiceNoteLimit {
		return in, sentinelErrf(ErrValidation, "notes must be a list of at most %d items", storyVoiceNoteLimit)
	}
	known := make(map[string]string, len(existing))
	for _, note := range existing {
		known[note.ID] = note.Origin
	}
	seen := make(map[string]bool, len(in.Notes))
	total := 0
	for i := range in.Notes {
		note := &in.Notes[i]
		parsed, err := uuid.Parse(note.ID)
		if err != nil {
			return in, sentinelErrf(ErrValidation, "each note needs a distinct UUID")
		}
		note.ID = parsed.String()
		if seen[note.ID] {
			return in, sentinelErrf(ErrValidation, "each note needs a distinct UUID")
		}
		seen[note.ID] = true
		note.Text = strings.TrimSpace(note.Text)
		length := len([]rune(note.Text))
		if length == 0 || length > storyVoiceNoteChars {
			return in, sentinelErrf(ErrValidation, "each note must contain 1 to %d characters", storyVoiceNoteChars)
		}
		total += length
		if previousOrigin, found := known[note.ID]; found {
			if note.Origin != previousOrigin {
				return in, sentinelErrf(ErrValidation, "note origin cannot change")
			}
		} else if note.Origin != "writer" {
			return in, sentinelErrf(ErrValidation, "new notes must be writer notes")
		}
	}
	if total > storyVoiceTotalChars {
		return in, sentinelErrf(ErrValidation, "voice notes are too long together")
	}
	if in.LearnFromWriting == nil {
		yes := true
		in.LearnFromWriting = &yes
	}
	if in.References == nil {
		in.References = []StoryVoiceReference{}
	}
	if len(in.References) > 3 {
		return in, sentinelErrf(ErrValidation, "at most three voice references")
	}
	seenReferences := map[string]bool{}
	for i := range in.References {
		ref := &in.References[i]
		id, err := uuid.Parse(ref.ChapterID)
		if err != nil || ref.SourceRevision < 1 || ref.Start < 0 || ref.End <= ref.Start || ref.End-ref.Start > 1600 {
			return in, sentinelErrf(ErrValidation, "invalid voice reference")
		}
		ref.ChapterID = id.String()
		if seenReferences[ref.ChapterID] {
			return in, sentinelErrf(ErrValidation, "duplicate voice reference")
		}
		seenReferences[ref.ChapterID] = true
	}
	return in, nil
}

func scanStoryVoice(row pgx.Row, storyID string) (StoryVoiceProfile, error) {
	x := defaultStoryVoice(storyID)
	var notes []byte
	var references []byte
	err := row.Scan(&x.PointOfView, &x.Tense, &notes, &x.LearnFromWriting, &references, &x.Revision, &x.UpdatedAt)
	if err != nil {
		return x, err
	}
	err = json.Unmarshal(notes, &x.Notes)
	if x.Notes == nil {
		x.Notes = []StoryVoiceNote{}
	}
	if err == nil {
		err = json.Unmarshal(references, &x.References)
	}
	if x.References == nil {
		x.References = []StoryVoiceReference{}
	}
	return x, err
}

func (s *Store) GetStoryVoice(ctx context.Context, storyID, owner string) (StoryVoiceProfile, error) {
	if err := s.requireOwnedAssistantStory(ctx, storyID, owner); err != nil {
		return StoryVoiceProfile{}, err
	}
	x, err := scanStoryVoice(s.db.QueryRow(ctx,
		`SELECT point_of_view, tense, voice_notes, learn_from_writing, reference_ranges, revision, updated_at FROM story_voice_profiles WHERE story_id=$1`, storyID), storyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultStoryVoice(storyID), nil
	}
	return x, err
}

// Locking the story serializes creation, saves, undo, and deletion. Ownership
// is checked before reading any private profile or history row.
func (s *Store) lockVoiceStory(ctx context.Context, tx pgx.Tx, storyID, owner string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM stories WHERE id=$1 AND owner_id=$2 FOR UPDATE`, storyID, owner).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func currentStoryVoice(ctx context.Context, tx pgx.Tx, storyID string) (StoryVoiceProfile, error) {
	x, err := scanStoryVoice(tx.QueryRow(ctx,
		`SELECT point_of_view, tense, voice_notes, learn_from_writing, reference_ranges, revision, updated_at FROM story_voice_profiles WHERE story_id=$1 FOR UPDATE`, storyID), storyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultStoryVoice(storyID), nil
	}
	return x, err
}

func saveStoryVoice(ctx context.Context, tx pgx.Tx, previous StoryVoiceProfile, in StoryVoiceInput) (StoryVoiceProfile, error) {
	raw, err := json.Marshal(in.Notes)
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	refs, err := json.Marshal(in.References)
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	var row pgx.Row
	if previous.Revision == 0 {
		row = tx.QueryRow(ctx, `INSERT INTO story_voice_profiles (story_id, point_of_view, tense, notes, voice_notes, learn_from_writing, reference_ranges)
			VALUES ($1,$2,$3,'',$4::jsonb,$5,$6::jsonb)
			RETURNING point_of_view, tense, voice_notes, learn_from_writing, reference_ranges, revision, updated_at`,
			previous.StoryID, in.PointOfView, in.Tense, raw, *in.LearnFromWriting, refs)
	} else {
		row = tx.QueryRow(ctx, `UPDATE story_voice_profiles SET point_of_view=$2, tense=$3,
			notes='', voice_notes=$4::jsonb, learn_from_writing=$5, reference_ranges=$6::jsonb, revision=revision+1, updated_at=now()
			WHERE story_id=$1 RETURNING point_of_view, tense, voice_notes, learn_from_writing, reference_ranges, revision, updated_at`,
			previous.StoryID, in.PointOfView, in.Tense, raw, *in.LearnFromWriting, refs)
	}
	return scanStoryVoice(row, previous.StoryID)
}

func recordStoryVoiceChange(ctx context.Context, tx pgx.Tx, before, after StoryVoiceProfile, action string, undoOf *string, reason string, evidence []StoryVoiceReference) (string, error) {
	oldState, err := json.Marshal(voiceInput(before))
	if err != nil {
		return "", err
	}
	newState, err := json.Marshal(voiceInput(after))
	if err != nil {
		return "", err
	}
	if evidence == nil {
		evidence = []StoryVoiceReference{}
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	var changeID string
	err = tx.QueryRow(ctx, `INSERT INTO story_voice_profile_changes
		(story_id,revision_before,revision_after,action,before_state,after_state,undo_of,reason,evidence)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$8,$9::jsonb) RETURNING id`,
		before.StoryID, before.Revision, after.Revision, action, oldState, newState, undoOf, reason, evidenceJSON).Scan(&changeID)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `DELETE FROM story_voice_profile_changes WHERE story_id=$1 AND id IN (
		SELECT id FROM story_voice_profile_changes WHERE story_id=$1
		ORDER BY revision_after DESC OFFSET $2)`, before.StoryID, storyVoiceHistoryLimit)
	return changeID, err
}

func (s *Store) PutStoryVoice(ctx context.Context, storyID, owner string, revision int64, in StoryVoiceInput) (StoryVoiceProfile, error) {
	if revision < 0 {
		return StoryVoiceProfile{}, ErrValidation
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.lockVoiceStory(ctx, tx, storyID, owner); err != nil {
		return StoryVoiceProfile{}, err
	}
	before, err := currentStoryVoice(ctx, tx, storyID)
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	if before.Revision != revision {
		return StoryVoiceProfile{}, ErrConflict
	}
	in, err = validStoryVoice(in, before.Notes)
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	if err = validateVoiceReferences(ctx, tx, storyID, in.References); err != nil {
		return StoryVoiceProfile{}, err
	}
	after, err := saveStoryVoice(ctx, tx, before, in)
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	if _, err = recordStoryVoiceChange(ctx, tx, before, after, "save", nil, "", nil); err != nil {
		return StoryVoiceProfile{}, err
	}
	return after, tx.Commit(ctx)
}

func validateVoiceReferences(ctx context.Context, tx pgx.Tx, storyID string, refs []StoryVoiceReference) error {
	for _, ref := range refs {
		var revision int64
		var content string
		err := tx.QueryRow(ctx, `SELECT revision,content FROM chapters WHERE story_id=$1 AND id=$2`, storyID, ref.ChapterID).Scan(&revision, &content)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if revision != ref.SourceRevision {
			return ErrConflict
		}
		if ref.End > len([]rune(content)) {
			return sentinelErrf(ErrValidation, "voice reference is outside its chapter")
		}
	}
	return nil
}

// AgentVoiceNote is the only automatic write surface. It cannot alter writer
// notes, narration settings, references, or the learning switch.
func (s *Store) AgentVoiceNote(ctx context.Context, storyID, owner string, revision int64, op AgentVoiceOperation) (AgentVoiceResult, error) {
	key, err := uuid.Parse(op.IdempotencyKey)
	if err != nil || revision < 0 {
		return AgentVoiceResult{}, ErrValidation
	}
	op.IdempotencyKey = key.String()
	op.Text = strings.TrimSpace(op.Text)
	op.Reason = strings.TrimSpace(op.Reason)
	if op.Source != "explicit" && op.Source != "passive" {
		return AgentVoiceResult{}, ErrValidation
	}
	if op.Operation != "add" && op.Operation != "revise" && op.Operation != "retire" {
		return AgentVoiceResult{}, ErrValidation
	}
	if len([]rune(op.Text)) > 240 || len([]rune(op.Reason)) < 8 || len([]rune(op.Reason)) > 500 {
		return AgentVoiceResult{}, ErrValidation
	}
	if op.Operation != "retire" && op.Text == "" {
		return AgentVoiceResult{}, ErrValidation
	}
	if len(op.Evidence) > 6 {
		return AgentVoiceResult{}, ErrValidation
	}
	for i := range op.Evidence {
		ref := &op.Evidence[i]
		id, parseErr := uuid.Parse(ref.ChapterID)
		if parseErr != nil || ref.SourceRevision < 1 || ref.Start < 0 || ref.End <= ref.Start || ref.End-ref.Start > 1600 {
			return AgentVoiceResult{}, sentinelErrf(ErrValidation, "invalid evidence range")
		}
		ref.ChapterID = id.String()
		for j := 0; j < i; j++ {
			prior := op.Evidence[j]
			if prior.ChapterID == ref.ChapterID && prior.Start < ref.End && ref.Start < prior.End {
				return AgentVoiceResult{}, sentinelErrf(ErrValidation, "evidence ranges must be independent")
			}
		}
	}
	if op.Source == "passive" {
		if len(op.Evidence) < 3 || len(op.Evidence) > 6 {
			return AgentVoiceResult{}, ErrValidation
		}
		chapters := map[string]bool{}
		for _, ref := range op.Evidence {
			chapters[ref.ChapterID] = true
		}
		if len(chapters) < 2 {
			return AgentVoiceResult{}, sentinelErrf(ErrValidation, "passive learning needs two chapters")
		}
	} else if len(op.Evidence) > 0 {
		return AgentVoiceResult{}, ErrValidation
	}
	payload, err := json.Marshal(op)
	if err != nil {
		return AgentVoiceResult{}, err
	}
	hash := sha256.Sum256(payload)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AgentVoiceResult{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.lockVoiceStory(ctx, tx, storyID, owner); err != nil {
		return AgentVoiceResult{}, err
	}
	var priorHash, priorChange string
	var priorRevision int64
	err = tx.QueryRow(ctx, `SELECT request_hash,change_id,revision_after FROM story_voice_agent_requests WHERE story_id=$1 AND idempotency_key=$2`, storyID, key).Scan(&priorHash, &priorChange, &priorRevision)
	if err == nil {
		if priorHash != fmt.Sprintf("%x", hash) {
			return AgentVoiceResult{}, ErrConflict
		}
		return AgentVoiceResult{ChangeID: priorChange, Revision: priorRevision, Applied: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return AgentVoiceResult{}, err
	}
	before, err := currentStoryVoice(ctx, tx, storyID)
	if err != nil {
		return AgentVoiceResult{}, err
	}
	if before.Revision != revision {
		return AgentVoiceResult{}, ErrConflict
	}
	if op.Source == "passive" && !before.LearnFromWriting {
		return AgentVoiceResult{}, sentinelErrf(ErrValidation, "passive learning is disabled")
	}
	if err = validateVoiceReferences(ctx, tx, storyID, op.Evidence); err != nil {
		return AgentVoiceResult{}, err
	}
	in := voiceInput(before)
	switch op.Operation {
	case "add":
		if op.NoteID != "" {
			return AgentVoiceResult{}, ErrValidation
		}
		for _, note := range in.Notes {
			if strings.EqualFold(note.Text, op.Text) {
				return AgentVoiceResult{Revision: revision}, nil
			}
		}
		in.Notes = append(in.Notes, StoryVoiceNote{ID: uuid.NewString(), Text: op.Text, Origin: "agent"})
	case "revise", "retire":
		found := false
		for i, note := range in.Notes {
			if note.ID != op.NoteID {
				continue
			}
			if note.Origin != "agent" || note.Pinned {
				return AgentVoiceResult{}, ErrForbidden
			}
			found = true
			if op.Operation == "retire" {
				in.Notes = append(in.Notes[:i], in.Notes[i+1:]...)
			} else {
				in.Notes[i].Text = op.Text
			}
			break
		}
		if !found {
			return AgentVoiceResult{}, ErrNotFound
		}
	}
	// Existing origins are authoritative; only the new note may be agent-owned.
	known := append([]StoryVoiceNote{}, before.Notes...)
	if op.Operation == "add" {
		known = append(known, in.Notes[len(in.Notes)-1])
	}
	in, err = validStoryVoice(in, known)
	if err != nil {
		return AgentVoiceResult{}, err
	}
	after, err := saveStoryVoice(ctx, tx, before, in)
	if err != nil {
		return AgentVoiceResult{}, err
	}
	changeID, err := recordStoryVoiceChange(ctx, tx, before, after, "agent", nil, op.Reason, op.Evidence)
	if err != nil {
		return AgentVoiceResult{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO story_voice_agent_requests(story_id,idempotency_key,request_hash,change_id,revision_after) VALUES ($1,$2,$3,$4,$5)`, storyID, key, fmt.Sprintf("%x", hash), changeID, after.Revision)
	if err != nil {
		return AgentVoiceResult{}, err
	}
	_, err = tx.Exec(ctx, `DELETE FROM story_voice_agent_requests WHERE story_id=$1 AND idempotency_key IN (SELECT idempotency_key FROM story_voice_agent_requests WHERE story_id=$1 ORDER BY created_at DESC OFFSET 100)`, storyID)
	if err != nil {
		return AgentVoiceResult{}, err
	}
	return AgentVoiceResult{ChangeID: changeID, Revision: after.Revision, Applied: true}, tx.Commit(ctx)
}

func scanStoryVoiceChange(row pgx.Row) (StoryVoiceChange, error) {
	var x StoryVoiceChange
	var id uuid.UUID
	var undoOf *uuid.UUID
	var before, after []byte
	var evidence []byte
	err := row.Scan(&id, &x.RevisionBefore, &x.RevisionAfter, &x.Action, &before, &after, &undoOf, &x.Reason, &evidence, &x.CreatedAt)
	if err != nil {
		return x, err
	}
	x.ID = id.String()
	if undoOf != nil {
		s := undoOf.String()
		x.UndoOf = &s
	}
	if err = json.Unmarshal(before, &x.Before); err != nil {
		return x, err
	}
	err = json.Unmarshal(after, &x.After)
	if err == nil {
		err = json.Unmarshal(evidence, &x.Evidence)
	}
	return x, err
}

func (s *Store) ListStoryVoiceChanges(ctx context.Context, storyID, owner string) ([]StoryVoiceChange, error) {
	if err := s.requireOwnedAssistantStory(ctx, storyID, owner); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id,revision_before,revision_after,action,before_state,after_state,undo_of,reason,evidence,created_at
		FROM story_voice_profile_changes WHERE story_id=$1 ORDER BY revision_after DESC LIMIT 20`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := []StoryVoiceChange{}
	for rows.Next() {
		x, err := scanStoryVoiceChange(rows)
		if err != nil {
			return nil, err
		}
		changes = append(changes, x)
	}
	return changes, rows.Err()
}

// Undo applies the latest saved snapshot only. Earlier changes remain visible,
// but cannot be replayed over newer writer decisions.
func (s *Store) UndoStoryVoiceChange(ctx context.Context, storyID, owner, changeID string, revision int64) (StoryVoiceProfile, error) {
	parsed, err := uuid.Parse(changeID)
	if err != nil {
		return StoryVoiceProfile{}, ErrNotFound
	}
	changeID = parsed.String()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.lockVoiceStory(ctx, tx, storyID, owner); err != nil {
		return StoryVoiceProfile{}, err
	}
	before, err := currentStoryVoice(ctx, tx, storyID)
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	if before.Revision != revision {
		return StoryVoiceProfile{}, ErrConflict
	}
	latest, err := scanStoryVoiceChange(tx.QueryRow(ctx, `SELECT id,revision_before,revision_after,action,before_state,after_state,undo_of,reason,evidence,created_at
		FROM story_voice_profile_changes WHERE story_id=$1 ORDER BY revision_after DESC LIMIT 1`, storyID))
	if errors.Is(err, pgx.ErrNoRows) {
		return StoryVoiceProfile{}, ErrNotFound
	}
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	if latest.ID != changeID || latest.RevisionAfter != revision || (latest.Action != "save" && latest.Action != "agent") {
		return StoryVoiceProfile{}, conflictErrf("only the latest voice change can be undone")
	}
	// The snapshot is server-written history, so its original note origins are
	// authoritative even when a later save removed one of those notes.
	in, err := validStoryVoice(latest.Before, latest.Before.Notes)
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	// A stale or removed chapter may invalidate an old reference. Undo should
	// restore the other preferences without reviving that stale style sample.
	validRefs := []StoryVoiceReference{}
	for _, ref := range in.References {
		if validateVoiceReferences(ctx, tx, storyID, []StoryVoiceReference{ref}) == nil {
			validRefs = append(validRefs, ref)
		}
	}
	in.References = validRefs
	after, err := saveStoryVoice(ctx, tx, before, in)
	if err != nil {
		return StoryVoiceProfile{}, err
	}
	if _, err = recordStoryVoiceChange(ctx, tx, before, after, "undo", &changeID, "", nil); err != nil {
		return StoryVoiceProfile{}, err
	}
	return after, tx.Commit(ctx)
}
