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

const guestbookPageSize = 10

type GuestbookEntry struct {
	ID             string    `json:"id"`
	OwnerID        string    `json:"ownerId"`
	AuthorID       string    `json:"authorId"`
	AuthorUsername string    `json:"authorUsername"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"createdAt"`
	CommentCount   int       `json:"commentCount"`
	UpvoteCount    int       `json:"upvoteCount"`
	DownvoteCount  int       `json:"downvoteCount"`
	UserVote       string    `json:"userVote,omitempty"`
}
type GuestbookReply struct {
	ID             string    `json:"id"`
	EntryID        string    `json:"entryId"`
	ParentID       *string   `json:"parentId"`
	AuthorID       string    `json:"authorId"`
	AuthorUsername string    `json:"authorUsername"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	UpvoteCount    int       `json:"upvoteCount"`
	DownvoteCount  int       `json:"downvoteCount"`
	UserVote       string    `json:"userVote,omitempty"`
}
type GuestbookPage struct {
	Entries    []GuestbookEntry `json:"entries"`
	NextCursor string           `json:"nextCursor,omitempty"`
}
type guestbookCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

func (s *Store) ListGuestbookEntries(ctx context.Context, ownerID, viewerID, cursor string, limit int) (GuestbookPage, error) {
	if limit <= 0 {
		limit = guestbookPageSize
	}
	if limit > 50 {
		limit = 50
	}
	args := []any{ownerID, viewerID}
	where := "WHERE e.owner_id=$1"
	if cursor != "" {
		c, err := decodeGuestbookCursor(cursor)
		if err != nil {
			return GuestbookPage{}, ErrValidation
		}
		args = append(args, c.CreatedAt, c.ID)
		where += " AND (e.created_at,e.id) < ($3,$4)"
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx, `SELECT e.id,e.owner_id,e.author_id,COALESCE(p.username,'unknown'),e.content,e.created_at,
 (SELECT count(*) FROM guestbook_replies r WHERE r.entry_id=e.id),
 (SELECT count(*) FROM guestbook_entry_votes v WHERE v.entry_id=e.id AND v.vote='up'),
 (SELECT count(*) FROM guestbook_entry_votes v WHERE v.entry_id=e.id AND v.vote='down'),
 COALESCE((SELECT v.vote FROM guestbook_entry_votes v WHERE v.entry_id=e.id AND v.user_id=$2),'')
 FROM guestbook_entries e LEFT JOIN public_profiles p ON p.user_id=e.author_id `+where+` ORDER BY e.created_at DESC,e.id DESC LIMIT $`+strconvArg(len(args)), args...)
	if err != nil {
		return GuestbookPage{}, err
	}
	defer rows.Close()
	items := []GuestbookEntry{}
	for rows.Next() {
		x, err := scanGuestbookEntry(rows)
		if err != nil {
			return GuestbookPage{}, err
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		return GuestbookPage{}, err
	}
	page := GuestbookPage{Entries: items}
	if len(items) > limit {
		page.Entries = items[:limit]
		page.NextCursor, _ = encodeGuestbookCursor(page.Entries[limit-1])
	}
	return page, nil
}

func (s *Store) ListGuestbookReplies(ctx context.Context, ownerID, entryID, viewerID string) ([]GuestbookReply, error) {
	if err := s.entryForOwner(ctx, ownerID, entryID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT r.id,r.entry_id,r.parent_id,r.author_id,COALESCE(p.username,'unknown'),r.content,r.created_at,r.updated_at,
 (SELECT count(*) FROM guestbook_reply_votes v WHERE v.reply_id=r.id AND v.vote='up'),
 (SELECT count(*) FROM guestbook_reply_votes v WHERE v.reply_id=r.id AND v.vote='down'),
 COALESCE((SELECT v.vote FROM guestbook_reply_votes v WHERE v.reply_id=r.id AND v.user_id=$2),'')
 FROM guestbook_replies r LEFT JOIN public_profiles p ON p.user_id=r.author_id WHERE r.entry_id=$1 ORDER BY r.created_at DESC,r.id DESC`, entryID, viewerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GuestbookReply{}
	for rows.Next() {
		x, e := scanGuestbookReply(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) CreateGuestbookEntry(ctx context.Context, ownerID, authorID, content string) (GuestbookEntry, error) {
	if err := s.canPostGuestbook(ctx, ownerID, authorID); err != nil {
		return GuestbookEntry{}, err
	}
	content = strings.TrimSpace(content)
	if len(content) == 0 || len(content) > 10000 {
		return GuestbookEntry{}, ErrValidation
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return GuestbookEntry{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.consumeGuestbookQuota(ctx, tx, authorID, "entry_count", 10); err != nil {
		return GuestbookEntry{}, err
	}
	id := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO guestbook_entries(id,owner_id,author_id,content) VALUES($1,$2,$3,$4)`, id, ownerID, authorID, content)
	if err != nil {
		return GuestbookEntry{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return GuestbookEntry{}, err
	}
	return s.guestbookEntry(ctx, id.String(), authorID)
}
func (s *Store) DeleteGuestbookEntry(ctx context.Context, ownerID, entryID, callerID string) error {
	cmd, err := s.db.Exec(ctx, `DELETE FROM guestbook_entries WHERE id=$1 AND owner_id=$2 AND (author_id=$3 OR owner_id=$3)`, entryID, ownerID, callerID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrForbidden
	}
	return nil
}
func (s *Store) CreateGuestbookReply(ctx context.Context, ownerID, entryID, authorID, parentID, content string) (GuestbookReply, error) {
	if err := s.canPostGuestbook(ctx, ownerID, authorID); err != nil {
		return GuestbookReply{}, err
	}
	content = strings.TrimSpace(content)
	if len(content) == 0 || len(content) > 10000 {
		return GuestbookReply{}, ErrValidation
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return GuestbookReply{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.consumeGuestbookQuota(ctx, tx, authorID, "reply_count", 10); err != nil {
		return GuestbookReply{}, err
	}
	id := uuid.New()
	var parent any = nil
	if parentID != "" {
		parent = parentID
	}
	cmd, err := tx.Exec(ctx, `INSERT INTO guestbook_replies(id,entry_id,parent_id,author_id,content) SELECT $1,$2,$3,$4,$5 WHERE EXISTS(SELECT 1 FROM guestbook_entries WHERE id=$2 AND owner_id=$6) AND ($3::uuid IS NULL OR EXISTS(SELECT 1 FROM guestbook_replies WHERE id=$3 AND entry_id=$2))`, id, entryID, parent, authorID, content, ownerID)
	if err != nil {
		return GuestbookReply{}, err
	}
	if cmd.RowsAffected() == 0 {
		return GuestbookReply{}, ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return GuestbookReply{}, err
	}
	return s.guestbookReply(ctx, id.String(), authorID)
}
func (s *Store) UpdateGuestbookReply(ctx context.Context, ownerID, entryID, replyID, callerID, content string) (GuestbookReply, error) {
	content = strings.TrimSpace(content)
	if len(content) == 0 || len(content) > 10000 {
		return GuestbookReply{}, ErrValidation
	}
	cmd, err := s.db.Exec(ctx, `UPDATE guestbook_replies r SET content=$1,updated_at=now() FROM guestbook_entries e WHERE r.id=$2 AND r.entry_id=$3 AND e.id=r.entry_id AND e.owner_id=$4 AND r.author_id=$5`, content, replyID, entryID, ownerID, callerID)
	if err != nil {
		return GuestbookReply{}, err
	}
	if cmd.RowsAffected() == 0 {
		return GuestbookReply{}, ErrForbidden
	}
	return s.guestbookReply(ctx, replyID, callerID)
}
func (s *Store) DeleteGuestbookReply(ctx context.Context, ownerID, entryID, replyID, callerID string) error {
	cmd, err := s.db.Exec(ctx, `DELETE FROM guestbook_replies r USING guestbook_entries e WHERE r.id=$1 AND r.entry_id=$2 AND e.id=r.entry_id AND e.owner_id=$3 AND (r.author_id=$4 OR e.owner_id=$4 OR e.author_id=$4)`, replyID, entryID, ownerID, callerID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrForbidden
	}
	return nil
}
func (s *Store) SetGuestbookVote(ctx context.Context, ownerID, entryID, replyID, callerID, vote string) error {
	if vote != "" && vote != "up" && vote != "down" {
		return ErrValidation
	}
	if replyID == "" {
		if err := s.entryForOwner(ctx, ownerID, entryID); err != nil {
			return err
		}
		if vote == "" {
			_, e := s.db.Exec(ctx, `DELETE FROM guestbook_entry_votes WHERE entry_id=$1 AND user_id=$2`, entryID, callerID)
			return e
		}
		_, e := s.db.Exec(ctx, `INSERT INTO guestbook_entry_votes(entry_id,user_id,vote) VALUES($1,$2,$3) ON CONFLICT(entry_id,user_id) DO UPDATE SET vote=EXCLUDED.vote,updated_at=now()`, entryID, callerID, vote)
		return e
	}
	if err := s.replyForOwner(ctx, ownerID, entryID, replyID); err != nil {
		return err
	}
	if vote == "" {
		_, e := s.db.Exec(ctx, `DELETE FROM guestbook_reply_votes WHERE reply_id=$1 AND user_id=$2`, replyID, callerID)
		return e
	}
	_, e := s.db.Exec(ctx, `INSERT INTO guestbook_reply_votes(reply_id,user_id,vote) VALUES($1,$2,$3) ON CONFLICT(reply_id,user_id) DO UPDATE SET vote=EXCLUDED.vote,updated_at=now()`, replyID, callerID, vote)
	return e
}

func (s *Store) Follow(ctx context.Context, follower, followed string, follow bool) error {
	if follower == followed {
		return ErrValidation
	}
	if follow {
		_, e := s.db.Exec(ctx, `INSERT INTO user_follows(follower_id,followed_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, follower, followed)
		return e
	}
	_, e := s.db.Exec(ctx, `DELETE FROM user_follows WHERE follower_id=$1 AND followed_id=$2`, follower, followed)
	return e
}
func (s *Store) ListFollowing(ctx context.Context, user string) ([]string, error) {
	rows, e := s.db.Query(ctx, `SELECT followed_id FROM user_follows WHERE follower_id=$1 ORDER BY followed_id`, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (s *Store) ListFollowers(ctx context.Context, user string) ([]string, error) {
	rows, e := s.db.Query(ctx, `SELECT follower_id FROM user_follows WHERE followed_id=$1 ORDER BY follower_id`, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) canPostGuestbook(ctx context.Context, owner, caller string) error {
	if owner == caller {
		return nil
	}
	var policy string
	if e := s.db.QueryRow(ctx, `SELECT guestbook_policy FROM public_profiles WHERE user_id=$1`, owner).Scan(&policy); errors.Is(e, pgx.ErrNoRows) {
		policy = "everyone"
	} else if e != nil {
		return e
	}
	if policy == "everyone" {
		return nil
	}
	if policy == "nobody" {
		return ErrForbidden
	}
	var followsOwner, ownerFollows bool
	if e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_follows WHERE follower_id=$1 AND followed_id=$2),EXISTS(SELECT 1 FROM user_follows WHERE follower_id=$2 AND followed_id=$1)`, caller, owner).Scan(&followsOwner, &ownerFollows); e != nil {
		return e
	}
	if (policy == "followers" && followsOwner) || (policy == "following" && ownerFollows) || (policy == "mutuals" && followsOwner && ownerFollows) {
		return nil
	}
	return ErrForbidden
}
func (s *Store) consumeGuestbookQuota(ctx context.Context, tx pgx.Tx, user, column string, max int) error {
	q := `INSERT INTO guestbook_daily_usage(user_id,day,` + column + `) VALUES($1,current_date,1) ON CONFLICT(user_id,day) DO UPDATE SET ` + column + `=guestbook_daily_usage.` + column + `+1 WHERE guestbook_daily_usage.` + column + ` < $2 RETURNING ` + column
	var n int
	e := tx.QueryRow(ctx, q, user, max).Scan(&n)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrRateLimit
	}
	return e
}
func (s *Store) entryForOwner(ctx context.Context, owner, id string) error {
	var ok bool
	e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guestbook_entries WHERE id=$1 AND owner_id=$2)`, id, owner).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}
func (s *Store) replyForOwner(ctx context.Context, owner, entry, reply string) error {
	var ok bool
	e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guestbook_replies r JOIN guestbook_entries e ON e.id=r.entry_id WHERE r.id=$1 AND r.entry_id=$2 AND e.owner_id=$3)`, reply, entry, owner).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}
func (s *Store) guestbookEntry(ctx context.Context, id, viewer string) (GuestbookEntry, error) {
	var x GuestbookEntry
	var uid uuid.UUID
	e := s.db.QueryRow(ctx, `SELECT e.id,e.owner_id,e.author_id,COALESCE(p.username,'unknown'),e.content,e.created_at,(SELECT count(*) FROM guestbook_replies r WHERE r.entry_id=e.id),(SELECT count(*) FROM guestbook_entry_votes v WHERE v.entry_id=e.id AND v.vote='up'),(SELECT count(*) FROM guestbook_entry_votes v WHERE v.entry_id=e.id AND v.vote='down'),COALESCE((SELECT v.vote FROM guestbook_entry_votes v WHERE v.entry_id=e.id AND v.user_id=$2),'') FROM guestbook_entries e LEFT JOIN public_profiles p ON p.user_id=e.author_id WHERE e.id=$1`, id, viewer).Scan(&uid, &x.OwnerID, &x.AuthorID, &x.AuthorUsername, &x.Content, &x.CreatedAt, &x.CommentCount, &x.UpvoteCount, &x.DownvoteCount, &x.UserVote)
	x.ID = uid.String()
	if errors.Is(e, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, e
}
func (s *Store) guestbookReply(ctx context.Context, id, viewer string) (GuestbookReply, error) {
	var x GuestbookReply
	var uid, entry uuid.UUID
	var parent *uuid.UUID
	e := s.db.QueryRow(ctx, `SELECT r.id,r.entry_id,r.parent_id,r.author_id,COALESCE(p.username,'unknown'),r.content,r.created_at,r.updated_at,(SELECT count(*) FROM guestbook_reply_votes v WHERE v.reply_id=r.id AND v.vote='up'),(SELECT count(*) FROM guestbook_reply_votes v WHERE v.reply_id=r.id AND v.vote='down'),COALESCE((SELECT v.vote FROM guestbook_reply_votes v WHERE v.reply_id=r.id AND v.user_id=$2),'') FROM guestbook_replies r LEFT JOIN public_profiles p ON p.user_id=r.author_id WHERE r.id=$1`, id, viewer).Scan(&uid, &entry, &parent, &x.AuthorID, &x.AuthorUsername, &x.Content, &x.CreatedAt, &x.UpdatedAt, &x.UpvoteCount, &x.DownvoteCount, &x.UserVote)
	if parent != nil {
		v := parent.String()
		x.ParentID = &v
	}
	x.ID, x.EntryID = uid.String(), entry.String()
	if errors.Is(e, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, e
}
func scanGuestbookEntry(row pgx.Row) (GuestbookEntry, error) {
	var x GuestbookEntry
	var id uuid.UUID
	e := row.Scan(&id, &x.OwnerID, &x.AuthorID, &x.AuthorUsername, &x.Content, &x.CreatedAt, &x.CommentCount, &x.UpvoteCount, &x.DownvoteCount, &x.UserVote)
	x.ID = id.String()
	return x, e
}
func scanGuestbookReply(row pgx.Row) (GuestbookReply, error) {
	var x GuestbookReply
	var id, entry uuid.UUID
	var parent *uuid.UUID
	e := row.Scan(&id, &entry, &parent, &x.AuthorID, &x.AuthorUsername, &x.Content, &x.CreatedAt, &x.UpdatedAt, &x.UpvoteCount, &x.DownvoteCount, &x.UserVote)
	if parent != nil {
		v := parent.String()
		x.ParentID = &v
	}
	x.ID, x.EntryID = id.String(), entry.String()
	return x, e
}
func encodeGuestbookCursor(x GuestbookEntry) (string, error) {
	b, e := json.Marshal(guestbookCursor{CreatedAt: x.CreatedAt, ID: x.ID})
	return base64.RawURLEncoding.EncodeToString(b), e
}
func decodeGuestbookCursor(v string) (guestbookCursor, error) {
	var x guestbookCursor
	b, e := base64.RawURLEncoding.DecodeString(v)
	if e != nil {
		return x, e
	}
	e = json.Unmarshal(b, &x)
	if e != nil || x.ID == "" {
		return x, ErrValidation
	}
	// The id goes into a uuid-typed comparison. Without this a cursor that is
	// well-formed base64 and well-formed JSON but carries a non-uuid id
	// reached PostgreSQL and came back as a 500, which is a client error
	// dressed as a server fault.
	if _, e = uuid.Parse(x.ID); e != nil {
		return guestbookCursor{}, ErrValidation
	}
	return x, nil
}
func strconvArg(n int) string { return fmt.Sprint(n) }
