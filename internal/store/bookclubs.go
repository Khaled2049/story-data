package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const bookClubCapacity = 10

// The club listing is public and unauthenticated, so it is capped rather than
// returning the whole table.
const defaultClubPageSize = 50
const maxClubPageSize = 100

type BookClub struct {
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	Description       string             `json:"description"`
	Image             string             `json:"image"`
	Category          string             `json:"category"`
	Activity          string             `json:"activity"`
	CreatorID         string             `json:"creatorId"`
	Members           []string           `json:"members"`
	MeetUp            string             `json:"meetUp,omitempty"`
	BookOfTheMonth    json.RawMessage    `json:"bookOfTheMonth,omitempty"`
	ReadingSchedule   json.RawMessage    `json:"readingSchedule,omitempty"`
	DiscussionPrompts []DiscussionPrompt `json:"discussionPrompts"`
	Polls             []BookClubPoll     `json:"polls"`
}
type BookClubInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Image       string `json:"image"`
	Category    string `json:"category"`
	Activity    string `json:"activity"`
	MeetUp      string `json:"meetUp"`
}
type BookClubSettings struct {
	MeetUp          *string         `json:"meetUp"`
	BookOfTheMonth  json.RawMessage `json:"bookOfTheMonth"`
	ReadingSchedule json.RawMessage `json:"readingSchedule"`
}
type ClubProgress struct {
	UserID         string    `json:"userId"`
	Username       string    `json:"username"`
	CurrentChapter int       `json:"currentChapter"`
	LastUpdated    time.Time `json:"lastUpdated"`
	Notes          *string   `json:"notes,omitempty"`
}
type ProgressInput struct {
	CurrentChapter int     `json:"currentChapter"`
	Notes          *string `json:"notes"`
}
type DiscussionPrompt struct {
	ID            string           `json:"id"`
	ChapterNumber int              `json:"chapterNumber"`
	Question      string           `json:"question"`
	Description   string           `json:"description"`
	CreatedAt     time.Time        `json:"createdAt"`
	CreatorID     string           `json:"creatorId"`
	Responses     []PromptResponse `json:"responses"`
}
type PromptInput struct {
	ChapterNumber int    `json:"chapterNumber"`
	Question      string `json:"question"`
	Description   string `json:"description"`
}
type PromptResponse struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId"`
	Username  string    `json:"username"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}
type PromptResponseInput struct {
	Content string `json:"content"`
}
type BookClubPoll struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Question  string         `json:"question"`
	Options   []PollOption   `json:"options"`
	Votes     map[string]int `json:"votes"`
	CreatedAt time.Time      `json:"createdAt"`
	CreatorID string         `json:"creatorId"`
	EndDate   string         `json:"endDate,omitempty"`
	IsActive  bool           `json:"isActive"`
}
type PollOption struct {
	Text     string          `json:"text"`
	BookData json.RawMessage `json:"bookData,omitempty"`
}
type PollInput struct {
	Type     string       `json:"type"`
	Question string       `json:"question"`
	Options  []PollOption `json:"options"`
	EndDate  string       `json:"endDate"`
}

const clubBookSelect = `CASE WHEN book_of_the_month->>'source' = 'novelsync'
  AND NOT EXISTS (SELECT 1 FROM stories st WHERE st.id::text = book_of_the_month->>'storyId' AND st.is_published)
  THEN 'null'::jsonb ELSE COALESCE(book_of_the_month,'null'::jsonb) END`

const clubSelect = `SELECT id,name,description,image,category,activity,owner_id,meetup,` + clubBookSelect + `,COALESCE(reading_schedule,'null'::jsonb) FROM book_clubs`

func (s *Store) ListBookClubs(ctx context.Context, limit int) ([]BookClub, error) {
	if limit <= 0 {
		limit = defaultClubPageSize
	}
	if limit > maxClubPageSize {
		limit = maxClubPageSize
	}
	rows, err := s.db.Query(ctx, clubSelect+` ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	x := []BookClub{}
	for rows.Next() {
		c, e := scanClubRow(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		x = append(x, c)
	}
	// Closed explicitly rather than deferred: hydrateClubs issues further
	// queries, and holding this cursor open would make the pool hand each of
	// them a second connection.
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = s.hydrateClubs(ctx, x); err != nil {
		return nil, err
	}
	return x, nil
}
func (s *Store) GetBookClub(ctx context.Context, id string) (BookClub, error) {
	if _, e := uuid.Parse(id); e != nil {
		return BookClub{}, ErrNotFound
	}
	x, e := scanClubRow(s.db.QueryRow(ctx, clubSelect+` WHERE id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return BookClub{}, ErrNotFound
	}
	if e != nil {
		return BookClub{}, e
	}
	clubs := []BookClub{x}
	if e = s.hydrateClubs(ctx, clubs); e != nil {
		return BookClub{}, e
	}
	return clubs[0], nil
}

func scanClubRow(row pgx.Row) (BookClub, error) {
	var x BookClub
	var uid uuid.UUID
	e := row.Scan(&uid, &x.Name, &x.Description, &x.Image, &x.Category, &x.Activity, &x.CreatorID, &x.MeetUp, &x.BookOfTheMonth, &x.ReadingSchedule)
	x.ID = uid.String()
	x.Members = []string{}
	x.DiscussionPrompts = []DiscussionPrompt{}
	x.Polls = []BookClubPoll{}
	return x, e
}

// hydrateClubs fills in members, prompts, responses, polls, options and votes
// for a set of clubs using six set-based queries, whatever the size of the
// result.
//
// It replaces a walk that fetched one entity at a time: 4 queries per club plus
// 2 per prompt, 1 per response and 3 per poll. Because GET /v1/book-clubs is
// public, unauthenticated and was unpaginated, a few dozen active clubs put
// thousands of sequential round trips behind a single anonymous request.
func (s *Store) hydrateClubs(ctx context.Context, clubs []BookClub) error {
	if len(clubs) == 0 {
		return nil
	}
	clubIDs := make([]string, len(clubs))
	byClub := make(map[string]*BookClub, len(clubs))
	for i := range clubs {
		clubIDs[i] = clubs[i].ID
		byClub[clubs[i].ID] = &clubs[i]
	}

	if e := s.eachRow(ctx, `SELECT club_id,user_id FROM book_club_members WHERE club_id = ANY($1::uuid[]) ORDER BY joined_at`, clubIDs, func(rows pgx.Rows) error {
		var cid uuid.UUID
		var u string
		if e := rows.Scan(&cid, &u); e != nil {
			return e
		}
		if c := byClub[cid.String()]; c != nil {
			c.Members = append(c.Members, u)
		}
		return nil
	}); e != nil {
		return e
	}

	if e := s.eachRow(ctx, `SELECT id,club_id,chapter_number,question,description,created_at,creator_id FROM book_club_prompts WHERE club_id = ANY($1::uuid[]) ORDER BY chapter_number,created_at`, clubIDs, func(rows pgx.Rows) error {
		var pid, cid uuid.UUID
		var v DiscussionPrompt
		if e := rows.Scan(&pid, &cid, &v.ChapterNumber, &v.Question, &v.Description, &v.CreatedAt, &v.CreatorID); e != nil {
			return e
		}
		v.ID = pid.String()
		v.Responses = []PromptResponse{}
		if c := byClub[cid.String()]; c != nil {
			c.DiscussionPrompts = append(c.DiscussionPrompts, v)
		}
		return nil
	}); e != nil {
		return e
	}

	// Indexed only once every prompt is in place: appending can move the
	// backing array, so pointers taken during the loop above would dangle.
	promptIDs := []string{}
	byPrompt := map[string]*DiscussionPrompt{}
	for ci := range clubs {
		for pi := range clubs[ci].DiscussionPrompts {
			p := &clubs[ci].DiscussionPrompts[pi]
			promptIDs = append(promptIDs, p.ID)
			byPrompt[p.ID] = p
		}
	}
	if len(promptIDs) > 0 {
		if e := s.eachRow(ctx, `SELECT r.id,r.prompt_id,r.user_id,COALESCE(p.username,'Anonymous'),r.content,r.created_at FROM book_club_prompt_responses r LEFT JOIN public_profiles p ON p.user_id=r.user_id WHERE r.prompt_id = ANY($1::uuid[]) ORDER BY r.created_at`, promptIDs, func(rows pgx.Rows) error {
			var rid, pid uuid.UUID
			var v PromptResponse
			if e := rows.Scan(&rid, &pid, &v.UserID, &v.Username, &v.Content, &v.CreatedAt); e != nil {
				return e
			}
			v.ID = rid.String()
			if p := byPrompt[pid.String()]; p != nil {
				p.Responses = append(p.Responses, v)
			}
			return nil
		}); e != nil {
			return e
		}
	}

	if e := s.eachRow(ctx, `SELECT id,club_id,type,question,end_date,creator_id,is_active,created_at FROM book_club_polls WHERE club_id = ANY($1::uuid[]) ORDER BY created_at DESC`, clubIDs, func(rows pgx.Rows) error {
		var lid, cid uuid.UUID
		var v BookClubPoll
		if e := rows.Scan(&lid, &cid, &v.Type, &v.Question, &v.EndDate, &v.CreatorID, &v.IsActive, &v.CreatedAt); e != nil {
			return e
		}
		v.ID = lid.String()
		v.Options = []PollOption{}
		v.Votes = map[string]int{}
		if c := byClub[cid.String()]; c != nil {
			c.Polls = append(c.Polls, v)
		}
		return nil
	}); e != nil {
		return e
	}

	pollIDs := []string{}
	byPoll := map[string]*BookClubPoll{}
	for ci := range clubs {
		for li := range clubs[ci].Polls {
			p := &clubs[ci].Polls[li]
			pollIDs = append(pollIDs, p.ID)
			byPoll[p.ID] = p
		}
	}
	if len(pollIDs) == 0 {
		return nil
	}
	if e := s.eachRow(ctx, `SELECT poll_id,text,COALESCE(book_data,'null'::jsonb) FROM book_club_poll_options WHERE poll_id = ANY($1::uuid[]) ORDER BY poll_id,position`, pollIDs, func(rows pgx.Rows) error {
		var lid uuid.UUID
		var o PollOption
		if e := rows.Scan(&lid, &o.Text, &o.BookData); e != nil {
			return e
		}
		if p := byPoll[lid.String()]; p != nil {
			p.Options = append(p.Options, o)
		}
		return nil
	}); e != nil {
		return e
	}
	return s.eachRow(ctx, `SELECT poll_id,user_id,option_position FROM book_club_poll_votes WHERE poll_id = ANY($1::uuid[])`, pollIDs, func(rows pgx.Rows) error {
		var lid uuid.UUID
		var u string
		var pos int
		if e := rows.Scan(&lid, &u, &pos); e != nil {
			return e
		}
		if p := byPoll[lid.String()]; p != nil {
			p.Votes[u] = pos
		}
		return nil
	})
}

// eachRow runs one batched lookup and applies scan to every row, keeping the
// cursor bookkeeping out of hydrateClubs.
func (s *Store) eachRow(ctx context.Context, sql string, ids []string, scan func(pgx.Rows) error) error {
	rows, e := s.db.Query(ctx, sql, ids)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		if e = scan(rows); e != nil {
			return e
		}
	}
	return rows.Err()
}
func (s *Store) CreateBookClub(ctx context.Context, user string, in BookClubInput) (BookClub, error) {
	if !validClubInput(in) {
		return BookClub{}, ErrValidation
	}
	id := uuid.New()
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return BookClub{}, e
	}
	defer tx.Rollback(ctx)
	if e = s.consumeDailyQuota(ctx, tx, user, "book-club", MaxBookClubsPerDay); e != nil {
		return BookClub{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO book_clubs(id,owner_id,name,description,image,category,activity,meetup) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, user, strings.TrimSpace(in.Name), in.Description, in.Image, in.Category, in.Activity, in.MeetUp)
	if e != nil {
		return BookClub{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO book_club_members(club_id,user_id,role) VALUES($1,$2,'owner')`, id, user)
	if e != nil {
		return BookClub{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return BookClub{}, e
	}
	return s.GetBookClub(ctx, id.String())
}
func (s *Store) UpdateBookClub(ctx context.Context, id, user string, in BookClubInput) (BookClub, error) {
	if !validClubInput(in) {
		return BookClub{}, ErrValidation
	}
	cmd, e := s.db.Exec(ctx, `UPDATE book_clubs SET name=$1,description=$2,image=$3,category=$4,activity=$5,meetup=$6,updated_at=now() WHERE id=$7 AND owner_id=$8`, strings.TrimSpace(in.Name), in.Description, in.Image, in.Category, in.Activity, in.MeetUp, id, user)
	if e != nil {
		return BookClub{}, e
	}
	if cmd.RowsAffected() == 0 {
		return BookClub{}, s.clubWrite(ctx, id, user)
	}
	return s.GetBookClub(ctx, id)
}
func (s *Store) UpdateBookClubSettings(ctx context.Context, id, user string, in BookClubSettings) (BookClub, error) {
	// Both blobs are stored verbatim and handed back to every member, so they
	// are bounded as well as parsed.
	if !validJSONSize(in.BookOfTheMonth) || !validJSONSize(in.ReadingSchedule) {
		return BookClub{}, ErrValidation
	}
	if !jsonValue(in.BookOfTheMonth) || !jsonValue(in.ReadingSchedule) {
		return BookClub{}, ErrValidation
	}

	if ok, e := s.clubBookIsPublic(ctx, in.BookOfTheMonth); e != nil {
		return BookClub{}, e
	} else if !ok {
		return BookClub{}, ErrValidation
	}
	book, schedule := "null", "null"
	if len(in.BookOfTheMonth) > 0 {
		book = string(in.BookOfTheMonth)
	}
	if len(in.ReadingSchedule) > 0 {
		schedule = string(in.ReadingSchedule)
	}
	cmd, e := s.db.Exec(ctx, `UPDATE book_clubs SET meetup=COALESCE($1,meetup),book_of_the_month=COALESCE(NULLIF($2::jsonb,'null'::jsonb),book_of_the_month),reading_schedule=COALESCE(NULLIF($3::jsonb,'null'::jsonb),reading_schedule),updated_at=now() WHERE id=$4 AND owner_id=$5`, in.MeetUp, book, schedule, id, user)
	if e != nil {
		return BookClub{}, e
	}
	if cmd.RowsAffected() == 0 {
		return BookClub{}, s.clubWrite(ctx, id, user)
	}
	return s.GetBookClub(ctx, id)
}

func (s *Store) clubBookIsPublic(ctx context.Context, book json.RawMessage) (bool, error) {
	if len(book) == 0 || string(book) == "null" {
		return true, nil
	}
	var x struct {
		Source  string `json:"source"`
		StoryID string `json:"storyId"`
	}
	if e := json.Unmarshal(book, &x); e != nil {
		// Not an object — jsonValue already accepted it as valid JSON, and a
		// non-object carries no story to vouch for.
		return true, nil
	}
	if x.Source != "novelsync" {
		return true, nil
	}
	var published bool
	e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stories WHERE id::text=$1 AND is_published)`, x.StoryID).Scan(&published)
	return published, e
}

func (s *Store) DeleteBookClub(ctx context.Context, id, user string) error {
	cmd, e := s.db.Exec(ctx, `DELETE FROM book_clubs WHERE id=$1 AND owner_id=$2`, id, user)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() == 0 {
		return s.clubWrite(ctx, id, user)
	}
	return nil
}
func (s *Store) JoinBookClub(ctx context.Context, id, user string) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var owner string
	e = tx.QueryRow(ctx, `SELECT owner_id FROM book_clubs WHERE id=$1 FOR UPDATE`, id).Scan(&owner)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	var n int
	e = tx.QueryRow(ctx, `SELECT count(*) FROM book_club_members WHERE club_id=$1`, id).Scan(&n)
	if e != nil {
		return e
	}
	if n >= bookClubCapacity {
		var member bool
		_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM book_club_members WHERE club_id=$1 AND user_id=$2)`, id, user).Scan(&member)
		if !member {
			return ErrLimit
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO book_club_members(club_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, user)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) LeaveBookClub(ctx context.Context, id, user string) error {
	cmd, e := s.db.Exec(ctx, `DELETE FROM book_club_members WHERE club_id=$1 AND user_id=$2 AND role='member'`, id, user)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() > 0 {
		return nil
	}
	// Nothing was removed, which means one of three things. An owner cannot
	// leave their own club, and a missing club is a 404 — but someone who was
	// never a member has already achieved what they asked for, so that is a
	// no-op rather than an error. Reporting it as forbidden made a repeated
	// DELETE fail, which a double-clicked Leave button does routinely.
	var owner string
	e = s.db.QueryRow(ctx, `SELECT owner_id FROM book_clubs WHERE id=$1`, id).Scan(&owner)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if owner == user {
		return ErrForbidden
	}
	return nil
}
func (s *Store) ListClubProgress(ctx context.Context, id, viewer string) ([]ClubProgress, error) {
	if !s.isMember(ctx, id, viewer) {
		return nil, ErrForbidden
	}
	// Joined to the membership table on purpose: progress rows have no foreign
	// key to it and survive a member leaving, so without this a departed reader
	// keeps appearing on the club's timeline.
	rows, e := s.db.Query(ctx, `SELECT p.user_id,COALESCE(pr.username,'Unknown User'),p.current_chapter,p.updated_at,p.notes FROM book_club_member_progress p JOIN book_club_members m ON m.club_id=p.club_id AND m.user_id=p.user_id LEFT JOIN public_profiles pr ON pr.user_id=p.user_id WHERE p.club_id=$1 ORDER BY p.current_chapter DESC`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	x := []ClubProgress{}
	for rows.Next() {
		var v ClubProgress
		if e := rows.Scan(&v.UserID, &v.Username, &v.CurrentChapter, &v.LastUpdated, &v.Notes); e != nil {
			return nil, e
		}
		x = append(x, v)
	}
	return x, rows.Err()
}
func (s *Store) PutClubProgress(ctx context.Context, id, user string, in ProgressInput) (ClubProgress, error) {
	if in.CurrentChapter < 0 {
		return ClubProgress{}, ErrValidation
	}
	if !s.isMember(ctx, id, user) {
		return ClubProgress{}, ErrForbidden
	}
	var x ClubProgress
	x.UserID = user
	e := s.db.QueryRow(ctx, `INSERT INTO book_club_member_progress(club_id,user_id,current_chapter,notes,updated_at) VALUES($1,$2,$3,$4,now()) ON CONFLICT(club_id,user_id) DO UPDATE SET current_chapter=EXCLUDED.current_chapter,notes=EXCLUDED.notes,updated_at=now() RETURNING current_chapter,updated_at,notes`, id, user, in.CurrentChapter, in.Notes).Scan(&x.CurrentChapter, &x.LastUpdated, &x.Notes)
	if e != nil {
		return x, e
	}
	_ = s.db.QueryRow(ctx, `SELECT COALESCE(username,'Unknown User') FROM public_profiles WHERE user_id=$1`, user).Scan(&x.Username)
	if x.Username == "" {
		x.Username = "Unknown User"
	}
	return x, nil
}
func (s *Store) CreatePrompt(ctx context.Context, club, user string, in PromptInput) (DiscussionPrompt, error) {
	if in.ChapterNumber < 1 || len(strings.TrimSpace(in.Question)) == 0 || len(in.Question) > 500 || len(in.Description) > 1000 {
		return DiscussionPrompt{}, ErrValidation
	}
	if !s.isOwner(ctx, club, user) {
		return DiscussionPrompt{}, ErrForbidden
	}
	// The quota is spent in the same transaction as the write, so a failed
	// insert gives the budget back instead of silently costing the user one of
	// their ten prompts for the day.
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return DiscussionPrompt{}, e
	}
	defer tx.Rollback(ctx)
	if e = s.consumeClubLimit(ctx, tx, user, "prompt", 24, 10); e != nil {
		return DiscussionPrompt{}, e
	}
	id := uuid.New()
	if _, e = tx.Exec(ctx, `INSERT INTO book_club_prompts(id,club_id,chapter_number,question,description,creator_id) VALUES($1,$2,$3,$4,$5,$6)`, id, club, in.ChapterNumber, strings.TrimSpace(in.Question), in.Description, user); e != nil {
		return DiscussionPrompt{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return DiscussionPrompt{}, e
	}
	return s.prompt(ctx, id.String())
}
func (s *Store) AddPromptResponse(ctx context.Context, club, prompt, user string, in PromptResponseInput) (PromptResponse, error) {
	if len(strings.TrimSpace(in.Content)) == 0 || len(in.Content) > 2000 {
		return PromptResponse{}, ErrValidation
	}
	if !s.isMember(ctx, club, user) {
		return PromptResponse{}, ErrForbidden
	}
	// As with prompts: quota and write commit together, so a response aimed at
	// a prompt in another club costs nothing.
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return PromptResponse{}, e
	}
	defer tx.Rollback(ctx)
	if e = s.consumeClubLimit(ctx, tx, user, "prompt-response", 24, 20); e != nil {
		return PromptResponse{}, e
	}
	id := uuid.New()
	cmd, e := tx.Exec(ctx, `INSERT INTO book_club_prompt_responses(id,prompt_id,user_id,content) SELECT $1,$2,$3,$4 WHERE EXISTS(SELECT 1 FROM book_club_prompts WHERE id=$2 AND club_id=$5)`, id, prompt, user, strings.TrimSpace(in.Content), club)
	if e != nil {
		return PromptResponse{}, e
	}
	if cmd.RowsAffected() == 0 {
		return PromptResponse{}, ErrNotFound
	}
	if e = tx.Commit(ctx); e != nil {
		return PromptResponse{}, e
	}
	return s.response(ctx, id.String())
}
func (s *Store) CreatePoll(ctx context.Context, club, user string, in PollInput) (BookClubPoll, error) {
	// The owner check is separate from the input checks so that a member who
	// is not allowed to create polls is told that, rather than being told
	// their request was malformed. CreatePrompt and ClosePoll already answer
	// ErrForbidden for the identical rule.
	if !s.isOwner(ctx, club, user) {
		return BookClubPoll{}, ErrForbidden
	}
	if len(strings.TrimSpace(in.Question)) == 0 || len(in.Question) > 500 || len(in.Options) < 2 || len(in.Options) > 10 {
		return BookClubPoll{}, ErrValidation
	}
	for _, o := range in.Options {
		if len(strings.TrimSpace(o.Text)) == 0 || len(o.Text) > 500 || !jsonValue(o.BookData) {
			return BookClubPoll{}, ErrValidation
		}
	}
	id := uuid.New()
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return BookClubPoll{}, e
	}
	defer tx.Rollback(ctx)
	// Inside the transaction: the poll and its options are written here too, so
	// a failure part-way through must not leave the quota spent.
	if e = s.consumeClubLimit(ctx, tx, user, "poll", 24, 5); e != nil {
		return BookClubPoll{}, e
	}
	typ := in.Type
	if typ == "" {
		typ = "book-selection"
	}
	_, e = tx.Exec(ctx, `INSERT INTO book_club_polls(id,club_id,type,question,end_date,creator_id) VALUES($1,$2,$3,$4,$5,$6)`, id, club, typ, strings.TrimSpace(in.Question), in.EndDate, user)
	if e != nil {
		return BookClubPoll{}, e
	}
	for i, o := range in.Options {
		_, e = tx.Exec(ctx, `INSERT INTO book_club_poll_options(id,poll_id,position,text,book_data) VALUES($1,$2,$3,$4,$5)`, uuid.New(), id, i, strings.TrimSpace(o.Text), nullableJSON(o.BookData))
		if e != nil {
			return BookClubPoll{}, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return BookClubPoll{}, e
	}
	return s.poll(ctx, id.String())
}
func (s *Store) VotePoll(ctx context.Context, club, poll, user string, position int) error {
	if position < 0 {
		return ErrValidation
	}
	if !s.isMember(ctx, club, user) {
		return ErrForbidden
	}
	cmd, e := s.db.Exec(ctx, `INSERT INTO book_club_poll_votes(poll_id,user_id,option_position) SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM book_club_polls p JOIN book_club_poll_options o ON o.poll_id=p.id AND o.position=$3 WHERE p.id=$1 AND p.club_id=$4 AND p.is_active) ON CONFLICT(poll_id,user_id) DO UPDATE SET option_position=EXCLUDED.option_position,updated_at=now()`, poll, user, position, club)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() == 0 {
		return s.classifyVote(ctx, club, poll)
	}
	return nil
}

// classifyVote explains a vote that matched nothing. The upsert's WHERE EXISTS
// folds three different situations into zero rows, and a caller who mistyped an
// option index needs a different answer from one whose poll closed underneath
// them.
func (s *Store) classifyVote(ctx context.Context, club, poll string) error {
	var active bool
	e := s.db.QueryRow(ctx, `SELECT is_active FROM book_club_polls WHERE id=$1 AND club_id=$2`, poll, club).Scan(&active)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if !active {
		return ErrConflict
	}
	return ErrValidation
}
func (s *Store) ClosePoll(ctx context.Context, club, poll, user string) error {
	if !s.isOwner(ctx, club, user) {
		return ErrForbidden
	}
	cmd, e := s.db.Exec(ctx, `UPDATE book_club_polls SET is_active=false WHERE id=$1 AND club_id=$2`, poll, club)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) prompt(ctx context.Context, id string) (DiscussionPrompt, error) {
	var x DiscussionPrompt
	var uid uuid.UUID
	e := s.db.QueryRow(ctx, `SELECT id,chapter_number,question,description,created_at,creator_id FROM book_club_prompts WHERE id=$1`, id).Scan(&uid, &x.ChapterNumber, &x.Question, &x.Description, &x.CreatedAt, &x.CreatorID)
	if errors.Is(e, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	if e != nil {
		return x, e
	}
	x.ID = uid.String()
	rows, e := s.db.Query(ctx, `SELECT id FROM book_club_prompt_responses WHERE prompt_id=$1 ORDER BY created_at`, id)
	if e != nil {
		return x, e
	}
	defer rows.Close()
	x.Responses = []PromptResponse{}
	for rows.Next() {
		var rid uuid.UUID
		if e = rows.Scan(&rid); e != nil {
			return x, e
		}
		v, z := s.response(ctx, rid.String())
		if z != nil {
			return x, z
		}
		x.Responses = append(x.Responses, v)
	}
	return x, rows.Err()
}
func (s *Store) response(ctx context.Context, id string) (PromptResponse, error) {
	var x PromptResponse
	var uid uuid.UUID
	e := s.db.QueryRow(ctx, `SELECT r.id,r.user_id,COALESCE(p.username,'Anonymous'),r.content,r.created_at FROM book_club_prompt_responses r LEFT JOIN public_profiles p ON p.user_id=r.user_id WHERE r.id=$1`, id).Scan(&uid, &x.UserID, &x.Username, &x.Content, &x.CreatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	if e != nil {
		return x, e
	}
	x.ID = uid.String()
	return x, nil
}
func (s *Store) poll(ctx context.Context, id string) (BookClubPoll, error) {
	var x BookClubPoll
	var uid uuid.UUID
	e := s.db.QueryRow(ctx, `SELECT id,type,question,end_date,creator_id,is_active,created_at FROM book_club_polls WHERE id=$1`, id).Scan(&uid, &x.Type, &x.Question, &x.EndDate, &x.CreatorID, &x.IsActive, &x.CreatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	if e != nil {
		return x, e
	}
	x.ID = uid.String()
	rows, e := s.db.Query(ctx, `SELECT text,COALESCE(book_data,'null'::jsonb) FROM book_club_poll_options WHERE poll_id=$1 ORDER BY position`, id)
	if e != nil {
		return x, e
	}
	defer rows.Close()
	x.Options = []PollOption{}
	for rows.Next() {
		var o PollOption
		if e = rows.Scan(&o.Text, &o.BookData); e != nil {
			return x, e
		}
		x.Options = append(x.Options, o)
	}
	vr, e := s.db.Query(ctx, `SELECT user_id,option_position FROM book_club_poll_votes WHERE poll_id=$1`, id)
	if e != nil {
		return x, e
	}
	defer vr.Close()
	x.Votes = map[string]int{}
	for vr.Next() {
		var u string
		var p int
		if e = vr.Scan(&u, &p); e != nil {
			return x, e
		}
		x.Votes[u] = p
	}
	return x, vr.Err()
}
func (s *Store) isMember(ctx context.Context, club, user string) bool {
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM book_club_members WHERE club_id=$1 AND user_id=$2)`, club, user).Scan(&ok)
	return ok
}
func (s *Store) isOwner(ctx context.Context, club, user string) bool {
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM book_clubs WHERE id=$1 AND owner_id=$2)`, club, user).Scan(&ok)
	return ok
}
func (s *Store) clubWrite(ctx context.Context, id, user string) error {
	var exists bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM book_clubs WHERE id=$1)`, id).Scan(&exists)
	if !exists {
		return ErrNotFound
	}
	return ErrForbidden
}
func validClubInput(in BookClubInput) bool {
	return validRequiredText(in.Name, maxNameChars) &&
		validText(in.Description, maxDescriptionChars) &&
		validURL(in.Image) &&
		validText(in.Category, maxShortFieldChars) &&
		validText(in.Activity, maxShortFieldChars) &&
		validText(in.MeetUp, maxNameChars)
}
func jsonValue(v json.RawMessage) bool { return len(v) == 0 || json.Valid(v) }
func nullableJSON(v json.RawMessage) any {
	if len(v) == 0 || string(v) == "null" {
		return nil
	}
	return string(v)
}

// consumeClubLimit takes a transaction rather than the pool so that spending
// budget and doing the work it pays for succeed or fail together.
func (s *Store) consumeClubLimit(ctx context.Context, tx pgx.Tx, user, action string, hours, maximum int) error {
	cmd, err := tx.Exec(ctx, `INSERT INTO book_club_usage(user_id,action,window_start,count)
VALUES($1,$2,date_trunc(CASE WHEN $3=24 THEN 'day' ELSE 'hour' END,now()),1)
ON CONFLICT(user_id,action,window_start) DO UPDATE SET count=book_club_usage.count+1
WHERE book_club_usage.count < $4`, user, action, hours, maximum)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrRateLimit
	}
	return nil
}
