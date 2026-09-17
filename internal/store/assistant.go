package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	assistantThreadPageSize   = 20
	assistantThreadPageLimit  = 100
	assistantMessagePageSize  = 50
	assistantMessagePageLimit = 100
	assistantTitleLimit       = 200
	assistantKeyLimit         = 200
	assistantRunIDLimit       = 200
	assistantPartsLimit       = 256 * 1024
	assistantMetadataLimit    = 64 * 1024
	assistantPartCountLimit   = 64
)

type AssistantThread struct {
	ID           string     `json:"id"`
	StoryID      string     `json:"storyId"`
	Title        string     `json:"title"`
	Revision     int64      `json:"revision"`
	ArchivedAt   *time.Time `json:"archivedAt,omitempty"`
	MessageCount int        `json:"messageCount"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

type AssistantThreadInput struct {
	Title string `json:"title"`
}

type AssistantThreadPatch struct {
	Title    *string `json:"title,omitempty"`
	Archived *bool   `json:"archived,omitempty"`
}

type AssistantThreadPage struct {
	Threads    []AssistantThread `json:"threads"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

type AssistantMessage struct {
	ID               string          `json:"id"`
	ThreadID         string          `json:"threadId"`
	Sequence         int64           `json:"sequence"`
	Role             string          `json:"role"`
	Parts            json.RawMessage `json:"parts"`
	Status           string          `json:"status"`
	Metadata         json.RawMessage `json:"metadata"`
	IdempotencyKey   string          `json:"idempotencyKey"`
	RunID            string          `json:"runId,omitempty"`
	Revision         int64           `json:"revision"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
	IdempotentReplay bool            `json:"idempotentReplay,omitempty"`
	idempotencyHash  string
}

type AssistantMessageInput struct {
	Role           string          `json:"role"`
	Parts          json.RawMessage `json:"parts"`
	Status         string          `json:"status"`
	Metadata       json.RawMessage `json:"metadata"`
	IdempotencyKey string          `json:"idempotencyKey"`
	RunID          string          `json:"runId"`
}

type AssistantMessagePatch struct {
	Parts    json.RawMessage `json:"parts"`
	Status   string          `json:"status"`
	Metadata json.RawMessage `json:"metadata"`
}

type AssistantMessagePage struct {
	Messages   []AssistantMessage `json:"messages"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

type assistantThreadCursor struct {
	UpdatedAt time.Time `json:"updatedAt"`
	ID        string    `json:"id"`
}

func normalizeAssistantTitle(title string, useDefault bool) (string, bool) {
	title = strings.TrimSpace(title)
	if title == "" && useDefault {
		title = "New conversation"
	}
	return title, title != "" && len([]rune(title)) <= assistantTitleLimit
}

func validAssistantParts(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > assistantPartsLimit || !json.Valid(raw) {
		return false
	}
	var parts []json.RawMessage
	return json.Unmarshal(raw, &parts) == nil && len(parts) > 0 && len(parts) <= assistantPartCountLimit
}

func normalizeAssistantMetadata(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), true
	}
	if len(raw) > assistantMetadataLimit || !json.Valid(raw) {
		return nil, false
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(raw, &metadata) != nil {
		return nil, false
	}
	return raw, true
}

func validAssistantMessageInput(in AssistantMessageInput) (AssistantMessageInput, bool) {
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	in.RunID = strings.TrimSpace(in.RunID)
	if len(in.IdempotencyKey) == 0 || len(in.IdempotencyKey) > assistantKeyLimit || len(in.RunID) > assistantRunIDLimit {
		return AssistantMessageInput{}, false
	}
	if in.Role != "user" && in.Role != "assistant" {
		return AssistantMessageInput{}, false
	}
	if in.Status == "" {
		in.Status = "complete"
	}
	switch in.Status {
	case "incomplete", "complete", "requires_action", "failed", "cancelled":
	default:
		return AssistantMessageInput{}, false
	}
	if in.Role == "user" && in.Status != "complete" {
		return AssistantMessageInput{}, false
	}
	if !validAssistantParts(in.Parts) {
		return AssistantMessageInput{}, false
	}
	var ok bool
	in.Metadata, ok = normalizeAssistantMetadata(in.Metadata)
	return in, ok
}

func assistantMessageFingerprint(in AssistantMessageInput) string {
	var parts, metadata any
	_ = json.Unmarshal(in.Parts, &parts)
	_ = json.Unmarshal(in.Metadata, &metadata)
	payload, _ := json.Marshal(struct {
		Role     string `json:"role"`
		Parts    any    `json:"parts"`
		Status   string `json:"status"`
		Metadata any    `json:"metadata"`
		RunID    string `json:"runId"`
	}{in.Role, parts, in.Status, metadata, in.RunID})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func scanAssistantThread(row pgx.Row) (AssistantThread, error) {
	var x AssistantThread
	var id, storyID uuid.UUID
	err := row.Scan(&id, &storyID, &x.Title, &x.Revision, &x.ArchivedAt, &x.MessageCount, &x.CreatedAt, &x.UpdatedAt)
	x.ID, x.StoryID = id.String(), storyID.String()
	return x, err
}

func scanAssistantMessage(row pgx.Row) (AssistantMessage, error) {
	var x AssistantMessage
	var id, threadID uuid.UUID
	err := row.Scan(&id, &threadID, &x.Sequence, &x.Role, &x.Parts, &x.Status, &x.Metadata, &x.IdempotencyKey, &x.idempotencyHash, &x.RunID, &x.Revision, &x.CreatedAt, &x.UpdatedAt)
	x.ID, x.ThreadID = id.String(), threadID.String()
	return x, err
}

const assistantThreadSelect = `SELECT t.id,t.story_id,t.title,t.revision,t.archived_at,
  (SELECT count(*) FROM assistant_messages m WHERE m.thread_id=t.id),t.created_at,t.updated_at
  FROM assistant_threads t JOIN stories s ON s.id=t.story_id`

const assistantMessageSelect = `SELECT m.id,m.thread_id,m.sequence,m.role,m.parts,m.status,m.metadata,
  m.idempotency_key,m.idempotency_fingerprint,COALESCE(m.run_id,''),m.revision,m.created_at,m.updated_at
  FROM assistant_messages m
  JOIN assistant_threads t ON t.id=m.thread_id
  JOIN stories s ON s.id=t.story_id`

func (s *Store) CreateAssistantThread(ctx context.Context, storyID, owner string, in AssistantThreadInput) (AssistantThread, error) {
	title, ok := normalizeAssistantTitle(in.Title, true)
	if !ok {
		return AssistantThread{}, ErrValidation
	}
	id := uuid.New()
	row := s.db.QueryRow(ctx, `INSERT INTO assistant_threads (id,story_id,title)
  SELECT $1,s.id,$2 FROM stories s WHERE s.id=$3 AND s.owner_id=$4
  RETURNING id,story_id,title,revision,archived_at,0,created_at,updated_at`, id, title, storyID, owner)
	x, err := scanAssistantThread(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssistantThread{}, ErrNotFound
	}
	return x, err
}

func (s *Store) ListAssistantThreads(ctx context.Context, storyID, owner, cursor string, limit int) (AssistantThreadPage, error) {
	if err := s.requireOwnedAssistantStory(ctx, storyID, owner); err != nil {
		return AssistantThreadPage{}, err
	}
	if limit <= 0 || limit > assistantThreadPageLimit {
		limit = assistantThreadPageSize
	}
	where := ` WHERE t.story_id=$1 AND s.owner_id=$2`
	args := []any{storyID, owner}
	if cursor != "" {
		c, err := decodeAssistantThreadCursor(cursor)
		if err != nil {
			return AssistantThreadPage{}, ErrValidation
		}
		where += ` AND (t.updated_at,t.id) < ($3,$4)`
		args = append(args, c.UpdatedAt, c.ID)
	}
	args = append(args, limit+1)
	query := assistantThreadSelect + where + ` ORDER BY t.updated_at DESC,t.id DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return AssistantThreadPage{}, err
	}
	defer rows.Close()
	items := []AssistantThread{}
	for rows.Next() {
		x, err := scanAssistantThread(rows)
		if err != nil {
			return AssistantThreadPage{}, err
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		return AssistantThreadPage{}, err
	}
	page := AssistantThreadPage{Threads: items}
	if len(items) > limit {
		page.Threads = items[:limit]
		page.NextCursor, _ = encodeAssistantThreadCursor(page.Threads[limit-1])
	}
	return page, nil
}

func (s *Store) requireOwnedAssistantStory(ctx context.Context, storyID, owner string) error {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stories WHERE id=$1 AND owner_id=$2)`, storyID, owner).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetAssistantThread(ctx context.Context, storyID, threadID, owner string) (AssistantThread, error) {
	x, err := scanAssistantThread(s.db.QueryRow(ctx, assistantThreadSelect+` WHERE t.id=$1 AND t.story_id=$2 AND s.owner_id=$3`, threadID, storyID, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return AssistantThread{}, ErrNotFound
	}
	return x, err
}

func (s *Store) UpdateAssistantThread(ctx context.Context, storyID, threadID, owner string, revision int64, in AssistantThreadPatch) (AssistantThread, error) {
	if in.Title == nil && in.Archived == nil {
		return AssistantThread{}, ErrValidation
	}
	var title *string
	if in.Title != nil {
		normalized, ok := normalizeAssistantTitle(*in.Title, false)
		if !ok {
			return AssistantThread{}, ErrValidation
		}
		title = &normalized
	}
	row := s.db.QueryRow(ctx, `UPDATE assistant_threads t SET
  title=COALESCE($1,t.title),
  archived_at=CASE WHEN $2::boolean IS NULL THEN t.archived_at WHEN $2 THEN COALESCE(t.archived_at,now()) ELSE NULL END,
  revision=t.revision+1,updated_at=now()
  FROM stories s
  WHERE t.id=$3 AND t.story_id=$4 AND t.story_id=s.id AND s.owner_id=$5 AND t.revision=$6
  RETURNING t.id,t.story_id,t.title,t.revision,t.archived_at,
    (SELECT count(*) FROM assistant_messages m WHERE m.thread_id=t.id),t.created_at,t.updated_at`, title, in.Archived, threadID, storyID, owner, revision)
	x, err := scanAssistantThread(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssistantThread{}, s.classifyAssistantThreadWrite(ctx, storyID, threadID, owner)
	}
	return x, err
}

func (s *Store) DeleteAssistantThread(ctx context.Context, storyID, threadID, owner string, revision int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM assistant_threads t USING stories s
  WHERE t.id=$1 AND t.story_id=$2 AND t.story_id=s.id AND s.owner_id=$3 AND t.revision=$4`, threadID, storyID, owner, revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return s.classifyAssistantThreadWrite(ctx, storyID, threadID, owner)
	}
	return nil
}

func (s *Store) classifyAssistantThreadWrite(ctx context.Context, storyID, threadID, owner string) error {
	var actualOwner string
	var actualRevision int64
	err := s.db.QueryRow(ctx, `SELECT s.owner_id,t.revision FROM assistant_threads t JOIN stories s ON s.id=t.story_id WHERE t.id=$1 AND t.story_id=$2`, threadID, storyID).Scan(&actualOwner, &actualRevision)
	if errors.Is(err, pgx.ErrNoRows) || actualOwner != owner {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return ErrConflict
}

func (s *Store) CreateAssistantMessage(ctx context.Context, storyID, threadID, owner string, in AssistantMessageInput) (AssistantMessage, error) {
	var ok bool
	in, ok = validAssistantMessageInput(in)
	if !ok {
		return AssistantMessage{}, ErrValidation
	}
	fingerprint := assistantMessageFingerprint(in)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AssistantMessage{}, err
	}
	defer tx.Rollback(ctx)
	var lockedID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT t.id FROM assistant_threads t JOIN stories s ON s.id=t.story_id
  WHERE t.id=$1 AND t.story_id=$2 AND s.owner_id=$3 FOR UPDATE OF t`, threadID, storyID, owner).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssistantMessage{}, ErrNotFound
	}
	if err != nil {
		return AssistantMessage{}, err
	}
	existing, err := scanAssistantMessage(tx.QueryRow(ctx, assistantMessageSelect+` WHERE m.thread_id=$1 AND m.idempotency_key=$2 AND t.story_id=$3 AND s.owner_id=$4`, threadID, in.IdempotencyKey, storyID, owner))
	if err == nil {
		if existing.idempotencyHash != fingerprint {
			return AssistantMessage{}, conflictErrf("idempotency key was already used for a different assistant message")
		}
		existing.IdempotentReplay = true
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return AssistantMessage{}, err
	}
	var sequence int64
	if err := tx.QueryRow(ctx, `UPDATE assistant_threads SET next_message_sequence=next_message_sequence+1,updated_at=now()
  WHERE id=$1 RETURNING next_message_sequence-1`, threadID).Scan(&sequence); err != nil {
		return AssistantMessage{}, err
	}
	id := uuid.New()
	x, err := scanAssistantMessage(tx.QueryRow(ctx, `INSERT INTO assistant_messages
	  (id,thread_id,sequence,role,parts,status,metadata,idempotency_key,idempotency_fingerprint,run_id)
	  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''))
	  RETURNING id,thread_id,sequence,role,parts,status,metadata,idempotency_key,idempotency_fingerprint,COALESCE(run_id,''),revision,created_at,updated_at`,
		id, threadID, sequence, in.Role, in.Parts, in.Status, in.Metadata, in.IdempotencyKey, fingerprint, in.RunID))
	if err != nil {
		return AssistantMessage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AssistantMessage{}, err
	}
	return x, nil
}

func (s *Store) ListAssistantMessages(ctx context.Context, storyID, threadID, owner, cursor string, limit int) (AssistantMessagePage, error) {
	if limit <= 0 || limit > assistantMessagePageLimit {
		limit = assistantMessagePageSize
	}
	after := int64(0)
	if cursor != "" {
		var err error
		after, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || after < 0 {
			return AssistantMessagePage{}, ErrValidation
		}
	}
	rows, err := s.db.Query(ctx, assistantMessageSelect+` WHERE m.thread_id=$1 AND t.story_id=$2 AND s.owner_id=$3 AND m.sequence>$4 ORDER BY m.sequence ASC LIMIT $5`, threadID, storyID, owner, after, limit+1)
	if err != nil {
		return AssistantMessagePage{}, err
	}
	defer rows.Close()
	items := []AssistantMessage{}
	for rows.Next() {
		x, err := scanAssistantMessage(rows)
		if err != nil {
			return AssistantMessagePage{}, err
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		return AssistantMessagePage{}, err
	}
	if len(items) == 0 {
		if _, err := s.GetAssistantThread(ctx, storyID, threadID, owner); err != nil {
			return AssistantMessagePage{}, err
		}
	}
	page := AssistantMessagePage{Messages: items}
	if len(items) > limit {
		page.Messages = items[:limit]
		page.NextCursor = strconv.FormatInt(page.Messages[limit-1].Sequence, 10)
	}
	return page, nil
}

func (s *Store) UpdateAssistantMessage(ctx context.Context, storyID, threadID, messageID, owner string, revision int64, in AssistantMessagePatch) (AssistantMessage, error) {
	if !validAssistantParts(in.Parts) {
		return AssistantMessage{}, ErrValidation
	}
	metadata, ok := normalizeAssistantMetadata(in.Metadata)
	if !ok {
		return AssistantMessage{}, ErrValidation
	}
	switch in.Status {
	case "incomplete", "complete", "requires_action", "failed", "cancelled":
	default:
		return AssistantMessage{}, ErrValidation
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AssistantMessage{}, err
	}
	defer tx.Rollback(ctx)
	row := tx.QueryRow(ctx, `UPDATE assistant_messages m SET parts=$1,status=$2,metadata=$3,revision=m.revision+1,updated_at=now()
  FROM assistant_threads t,stories s
  WHERE m.id=$4 AND m.thread_id=$5 AND m.thread_id=t.id AND t.story_id=$6 AND t.story_id=s.id
    AND s.owner_id=$7 AND m.role='assistant' AND m.revision=$8
	  RETURNING m.id,m.thread_id,m.sequence,m.role,m.parts,m.status,m.metadata,m.idempotency_key,
	    m.idempotency_fingerprint,COALESCE(m.run_id,''),m.revision,m.created_at,m.updated_at`, in.Parts, in.Status, metadata, messageID, threadID, storyID, owner, revision)
	x, err := scanAssistantMessage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssistantMessage{}, s.classifyAssistantMessageWrite(ctx, storyID, threadID, messageID, owner)
	}
	if err != nil {
		return AssistantMessage{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE assistant_threads SET updated_at=now() WHERE id=$1`, threadID); err != nil {
		return AssistantMessage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AssistantMessage{}, err
	}
	return x, nil
}

func (s *Store) classifyAssistantMessageWrite(ctx context.Context, storyID, threadID, messageID, owner string) error {
	var actualOwner, role string
	var actualRevision int64
	err := s.db.QueryRow(ctx, `SELECT s.owner_id,m.role,m.revision FROM assistant_messages m
  JOIN assistant_threads t ON t.id=m.thread_id JOIN stories s ON s.id=t.story_id
  WHERE m.id=$1 AND m.thread_id=$2 AND t.story_id=$3`, messageID, threadID, storyID).Scan(&actualOwner, &role, &actualRevision)
	if errors.Is(err, pgx.ErrNoRows) || actualOwner != owner {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if role != "assistant" {
		return ErrValidation
	}
	return ErrConflict
}

func encodeAssistantThreadCursor(x AssistantThread) (string, error) {
	b, err := json.Marshal(assistantThreadCursor{UpdatedAt: x.UpdatedAt, ID: x.ID})
	return base64.RawURLEncoding.EncodeToString(b), err
}

func decodeAssistantThreadCursor(v string) (assistantThreadCursor, error) {
	var c assistantThreadCursor
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil || c.ID == "" || c.UpdatedAt.IsZero() {
		return assistantThreadCursor{}, ErrValidation
	}
	return c, nil
}
