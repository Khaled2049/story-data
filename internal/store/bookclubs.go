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

func (s *Store) ListBookClubs(ctx context.Context, viewer string) ([]BookClub, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM book_clubs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	x := []BookClub{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		c, e := s.bookClub(ctx, id.String())
		if e != nil {
			return nil, e
		}
		x = append(x, c)
	}
	return x, rows.Err()
}
func (s *Store) GetBookClub(ctx context.Context, id string) (BookClub, error) {
	if _, e := uuid.Parse(id); e != nil {
		return BookClub{}, ErrNotFound
	}
	return s.bookClub(ctx, id)
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
	return s.bookClub(ctx, id.String())
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
	return s.bookClub(ctx, id)
}
func (s *Store) UpdateBookClubSettings(ctx context.Context, id, user string, in BookClubSettings) (BookClub, error) {
	if !jsonValue(in.BookOfTheMonth) || !jsonValue(in.ReadingSchedule) {
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
	return s.bookClub(ctx, id)
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
	if cmd.RowsAffected() == 0 {
		return s.clubWrite(ctx, id, user)
	}
	return nil
}
func (s *Store) ListClubProgress(ctx context.Context, id, viewer string) ([]ClubProgress, error) {
	if !s.isMember(ctx, id, viewer) {
		return nil, ErrForbidden
	}
	rows, e := s.db.Query(ctx, `SELECT p.user_id,COALESCE(pr.username,'Unknown User'),p.current_chapter,p.updated_at,p.notes FROM book_club_member_progress p LEFT JOIN public_profiles pr ON pr.user_id=p.user_id WHERE p.club_id=$1 ORDER BY p.current_chapter DESC`, id)
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
	if err := s.consumeClubLimit(ctx, user, "prompt", 24, 10); err != nil {
		return DiscussionPrompt{}, err
	}
	id := uuid.New()
	_, e := s.db.Exec(ctx, `INSERT INTO book_club_prompts(id,club_id,chapter_number,question,description,creator_id) VALUES($1,$2,$3,$4,$5,$6)`, id, club, in.ChapterNumber, strings.TrimSpace(in.Question), in.Description, user)
	if e != nil {
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
	if err := s.consumeClubLimit(ctx, user, "prompt-response", 24, 20); err != nil {
		return PromptResponse{}, err
	}
	id := uuid.New()
	cmd, e := s.db.Exec(ctx, `INSERT INTO book_club_prompt_responses(id,prompt_id,user_id,content) SELECT $1,$2,$3,$4 WHERE EXISTS(SELECT 1 FROM book_club_prompts WHERE id=$2 AND club_id=$5)`, id, prompt, user, strings.TrimSpace(in.Content), club)
	if e != nil {
		return PromptResponse{}, e
	}
	if cmd.RowsAffected() == 0 {
		return PromptResponse{}, ErrNotFound
	}
	return s.response(ctx, id.String())
}
func (s *Store) CreatePoll(ctx context.Context, club, user string, in PollInput) (BookClubPoll, error) {
	if !s.isOwner(ctx, club, user) || len(strings.TrimSpace(in.Question)) == 0 || len(in.Question) > 500 || len(in.Options) < 2 || len(in.Options) > 10 {
		return BookClubPoll{}, ErrValidation
	}
	for _, o := range in.Options {
		if len(strings.TrimSpace(o.Text)) == 0 || len(o.Text) > 500 || !jsonValue(o.BookData) {
			return BookClubPoll{}, ErrValidation
		}
	}
	if err := s.consumeClubLimit(ctx, user, "poll", 24, 5); err != nil {
		return BookClubPoll{}, err
	}
	id := uuid.New()
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return BookClubPoll{}, e
	}
	defer tx.Rollback(ctx)
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
	if position < 0 || !s.isMember(ctx, club, user) {
		return ErrForbidden
	}
	cmd, e := s.db.Exec(ctx, `INSERT INTO book_club_poll_votes(poll_id,user_id,option_position) SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM book_club_polls p JOIN book_club_poll_options o ON o.poll_id=p.id AND o.position=$3 WHERE p.id=$1 AND p.club_id=$4 AND p.is_active) ON CONFLICT(poll_id,user_id) DO UPDATE SET option_position=EXCLUDED.option_position,updated_at=now()`, poll, user, position, club)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() == 0 {
		return ErrValidation
	}
	return nil
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

func (s *Store) bookClub(ctx context.Context, id string) (BookClub, error) {
	var x BookClub
	var uid uuid.UUID
	e := s.db.QueryRow(ctx, `SELECT id,name,description,image,category,activity,owner_id,meetup,COALESCE(book_of_the_month,'null'::jsonb),COALESCE(reading_schedule,'null'::jsonb) FROM book_clubs WHERE id=$1`, id).Scan(&uid, &x.Name, &x.Description, &x.Image, &x.Category, &x.Activity, &x.CreatorID, &x.MeetUp, &x.BookOfTheMonth, &x.ReadingSchedule)
	if errors.Is(e, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	if e != nil {
		return x, e
	}
	x.ID = uid.String()
	rows, e := s.db.Query(ctx, `SELECT user_id FROM book_club_members WHERE club_id=$1 ORDER BY joined_at`, id)
	if e != nil {
		return x, e
	}
	defer rows.Close()
	x.Members = []string{}
	for rows.Next() {
		var u string
		if e = rows.Scan(&u); e != nil {
			return x, e
		}
		x.Members = append(x.Members, u)
	}
	if e = rows.Err(); e != nil {
		return x, e
	}
	x.DiscussionPrompts, e = s.prompts(ctx, id)
	if e != nil {
		return x, e
	}
	x.Polls, e = s.polls(ctx, id)
	return x, e
}
func (s *Store) prompts(ctx context.Context, club string) ([]DiscussionPrompt, error) {
	rows, e := s.db.Query(ctx, `SELECT id FROM book_club_prompts WHERE club_id=$1 ORDER BY chapter_number,created_at`, club)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	x := []DiscussionPrompt{}
	for rows.Next() {
		var id uuid.UUID
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		v, z := s.prompt(ctx, id.String())
		if z != nil {
			return nil, z
		}
		x = append(x, v)
	}
	return x, rows.Err()
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
	x.ID = uid.String()
	return x, e
}
func (s *Store) polls(ctx context.Context, club string) ([]BookClubPoll, error) {
	rows, e := s.db.Query(ctx, `SELECT id FROM book_club_polls WHERE club_id=$1 ORDER BY created_at DESC`, club)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	x := []BookClubPoll{}
	for rows.Next() {
		var id uuid.UUID
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		v, z := s.poll(ctx, id.String())
		if z != nil {
			return nil, z
		}
		x = append(x, v)
	}
	return x, rows.Err()
}
func (s *Store) poll(ctx context.Context, id string) (BookClubPoll, error) {
	var x BookClubPoll
	var uid uuid.UUID
	e := s.db.QueryRow(ctx, `SELECT id,type,question,end_date,creator_id,is_active,created_at FROM book_club_polls WHERE id=$1`, id).Scan(&uid, &x.Type, &x.Question, &x.EndDate, &x.CreatorID, &x.IsActive, &x.CreatedAt)
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
	return len(strings.TrimSpace(in.Name)) > 0 && len(in.Name) <= 200 && len(in.Description) <= 5000
}
func jsonValue(v json.RawMessage) bool { return len(v) == 0 || json.Valid(v) }
func nullableJSON(v json.RawMessage) any {
	if len(v) == 0 || string(v) == "null" {
		return nil
	}
	return string(v)
}

func (s *Store) consumeClubLimit(ctx context.Context, user, action string, hours, maximum int) error {
	cmd, err := s.db.Exec(ctx, `INSERT INTO book_club_usage(user_id,action,window_start,count)
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
