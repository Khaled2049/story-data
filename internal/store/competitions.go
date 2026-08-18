package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const taleAsset = "TALE"

type TokenBalance struct {
	AccountID string `json:"accountId"`
	AssetID   string `json:"assetId"`
	Symbol    string `json:"symbol"`
	Decimals  int    `json:"decimals"`
	Balance   string `json:"balance"`
}
type Competition struct {
	ID              string              `json:"id"`
	Title           string              `json:"title"`
	Description     string              `json:"description"`
	Category        string              `json:"category"`
	Tags            []string            `json:"tags"`
	CreatorID       string              `json:"creatorId"`
	CreatorName     string              `json:"creatorName"`
	Organizer       string              `json:"organizer"`
	Phase           string              `json:"phase"`
	Published       bool                `json:"published"`
	StartDate       *time.Time          `json:"startDate"`
	Deadline        *time.Time          `json:"deadline"`
	VotingDeadline  *time.Time          `json:"votingDeadline"`
	MaxParticipants *int                `json:"maxParticipants"`
	Participants    int                 `json:"participants"`
	SubmissionCount int                 `json:"submissionCount"`
	BallotCount     int                 `json:"ballotCount"`
	PrizePool       TokenAmount         `json:"prizePool"`
	EntryFee        TokenAmount         `json:"entryFee"`
	FeeBps          int                 `json:"feeBps"`
	EntryFeesHeld   string              `json:"entryFeesHeld"`
	IsJoined        bool                `json:"isJoined"`
	Results         []CompetitionResult `json:"results,omitempty"`
	ResultsDigest   string              `json:"resultsDigest,omitempty"`
	SettledAt       *time.Time          `json:"settledAt,omitempty"`
	// createdAt backs the listing cursor. Unexported because it is ordering
	// state, not part of the record the API promises.
	createdAt time.Time
}
type TokenAmount struct {
	AssetID  string `json:"assetId"`
	Symbol   string `json:"symbol"`
	Decimals int    `json:"decimals"`
	Amount   string `json:"amount"`
}
type CompetitionResult struct {
	Rank         int    `json:"rank"`
	UserID       string `json:"userId"`
	SubmissionID string `json:"submissionId"`
	Votes        int    `json:"votes"`
	Amount       string `json:"amount"`
}
type CompetitionInput struct {
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Category        string     `json:"category"`
	Tags            []string   `json:"tags"`
	MaxParticipants *int       `json:"maxParticipants"`
	StartDate       *time.Time `json:"startDate"`
	Deadline        *time.Time `json:"deadline"`
	VotingDeadline  *time.Time `json:"votingDeadline"`
	PrizeAmount     *string    `json:"prizeAmount"`
	EntryFee        *string    `json:"entryFee"`
	CreatorName     string     `json:"creatorName"`
}
type CompetitionSubmission struct {
	ID              string    `json:"id"`
	UserID          string    `json:"userId"`
	StoryID         string    `json:"storyId"`
	StoryTitle      string    `json:"storyTitle"`
	StoryAuthorName *string   `json:"storyAuthorName,omitempty"`
	CoverImageURL   *string   `json:"coverImageUrl,omitempty"`
	Status          string    `json:"status"`
	SubmittedAt     time.Time `json:"submittedAt"`
	VoteCount       *int      `json:"voteCount,omitempty"`
}
type CompetitionBallot struct {
	VoterID       string    `json:"voterId"`
	SubmissionIDs []string  `json:"submissionIds"`
	CastAt        time.Time `json:"castAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func tokenAmount(v string) TokenAmount { return TokenAmount{taleAsset, "TALE", 18, v} }
func amount(v string) (string, error) {
	if v == "" {
		return "0", nil
	}
	if strings.HasPrefix(v, "-") {
		return "", ErrValidation
	}
	if _, ok := new(big.Int).SetString(v, 10); !ok {
		return "", ErrValidation
	}
	return v, nil
}
func account(uid string) string { return "user:" + uid }
func escrow(id string) string   { return "escrow:competition:" + id }

func (s *Store) TokenBalance(ctx context.Context, user string) (TokenBalance, error) {
	if e := s.initialGrant(ctx, user); e != nil {
		return TokenBalance{}, e
	}
	return s.balance(ctx, account(user))
}
func (s *Store) ClaimFaucet(ctx context.Context, user string) (TokenBalance, error) {
	key := "grant:faucet:" + user + ":" + time.Now().UTC().Format("2006-01-02")
	if e := s.transfer(ctx, key, "grant:faucet", "", []posting{{"system:mint", "-250000000000000000000"}, {account(user), "250000000000000000000"}}); e != nil {
		return TokenBalance{}, e
	}
	return s.balance(ctx, account(user))
}

// GrantTokens mints TALE to a user. Besides the automatic opening balance and
// the daily faucet, this is the platform's only mint, so it is admin-only and
// keyed by a caller-supplied idempotency key: a retried support request, or a
// re-run of the seed script, must not pay twice.
func (s *Store) GrantTokens(ctx context.Context, user, raw, key string) (TokenBalance, error) {
	if strings.TrimSpace(user) == "" || strings.TrimSpace(key) == "" {
		return TokenBalance{}, ErrValidation
	}
	value, e := amount(raw)
	if e != nil {
		return TokenBalance{}, e
	}
	if value == "0" {
		return TokenBalance{}, ErrValidation
	}
	// Materialized first so the recipient's opening balance is not lost behind
	// the grant — the same lazy grant every other balance path performs.
	if e = s.initialGrant(ctx, user); e != nil {
		return TokenBalance{}, e
	}
	if e = s.transfer(ctx, "grant:admin:"+key, "grant:admin", "", []posting{{"system:mint", "-" + value}, {account(user), value}}); e != nil {
		return TokenBalance{}, e
	}
	return s.balance(ctx, account(user))
}
func (s *Store) initialGrant(ctx context.Context, user string) error {
	return s.transfer(ctx, "grant:initial:"+user, "grant:initial", "", initialGrantPostings(user))
}
func (s *Store) initialGrantTx(ctx context.Context, tx pgx.Tx, user string) error {
	return s.transferTx(ctx, tx, "grant:initial:"+user, "grant:initial", "", initialGrantPostings(user))
}
func initialGrantPostings(user string) []posting {
	return []posting{{"system:mint", "-1000000000000000000000"}, {account(user), "1000000000000000000000"}}
}

// Entry fees are keyed per attempt, because a withdrawal refunds one charge
// and re-entering makes another. Reusing a key would make the ledger treat the
// second charge as a duplicate and silently skip it.
func entryKey(id, user string, attempt int) string {
	return fmt.Sprintf("escrow:entry:%s:%s:%d", id, user, attempt)
}
func refundKey(id, user string, attempt int) string {
	return fmt.Sprintf("escrow:entry-refund:%s:%s:%d", id, user, attempt)
}

type posting struct{ account, delta string }

func (s *Store) balance(ctx context.Context, id string) (TokenBalance, error) {
	var b string
	e := s.db.QueryRow(ctx, `SELECT balance::text FROM token_accounts WHERE account_id=$1`, id).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		b = "0"
		e = nil
	}
	return TokenBalance{id, taleAsset, "TALE", 18, b}, e
}

// mergePostings collapses a transfer's postings to one line per account and
// drops the ones that net to zero, which the delta <> 0 check would reject.
// Accounts come back sorted so every transfer takes its FOR UPDATE row locks
// in the same order and concurrent transfers cannot deadlock against each
// other.
func mergePostings(ps []posting) ([]posting, error) {
	sums := map[string]*big.Int{}
	order := make([]string, 0, len(ps))
	for _, p := range ps {
		n, ok := new(big.Int).SetString(p.delta, 10)
		if !ok {
			return nil, ErrValidation
		}
		if sums[p.account] == nil {
			sums[p.account] = new(big.Int)
			order = append(order, p.account)
		}
		sums[p.account].Add(sums[p.account], n)
	}
	sort.Strings(order)
	out := make([]posting, 0, len(order))
	for _, a := range order {
		if sums[a].Sign() != 0 {
			out = append(out, posting{a, sums[a].String()})
		}
	}
	return out, nil
}

func (s *Store) transfer(ctx context.Context, key, reason, competition string, ps []posting) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = s.transferTx(ctx, tx, key, reason, competition, ps); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// transferTx posts a transfer inside a caller-supplied transaction, so that
// moving money and recording why it moved commit together. transfer is the
// standalone wrapper for callers that have nothing else to do.
func (s *Store) transferTx(ctx context.Context, tx pgx.Tx, key, reason, competition string, ps []posting) error {
	var exists bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ledger_transfers WHERE idempotency_key=$1)`, key).Scan(&exists)
	if e != nil {
		return e
	}
	if exists {
		return nil
	}
	// ledger_postings is keyed (transfer_key, account_id), so an account that
	// appears twice in one transfer aborts the whole insert with 23505. Merge
	// here rather than in each caller: settlement legitimately credits one
	// account from two sources (prize refund + host fee share when the creator
	// is also the recipient), and that must post as a single net line.
	ps, e = mergePostings(ps)
	if e != nil {
		return e
	}
	sum := big.NewInt(0)
	for _, p := range ps {
		n, ok := new(big.Int).SetString(p.delta, 10)
		if !ok {
			return ErrValidation
		}
		sum.Add(sum, n)
	}
	if sum.Sign() != 0 {
		return ErrValidation
	}
	for _, p := range ps {
		if strings.HasPrefix(p.account, "system:") {
			continue
		}
		kind := "escrow"
		var owner any
		if strings.HasPrefix(p.account, "user:") {
			kind = "user"
			x := strings.TrimPrefix(p.account, "user:")
			owner = &x
		} else if strings.HasPrefix(p.account, "platform:") {
			kind = "platform"
		}
		_, e = tx.Exec(ctx, `INSERT INTO token_accounts(account_id,owner_id,kind,balance) VALUES($1,$2,$3,0) ON CONFLICT DO NOTHING`, p.account, owner, kind)
		if e != nil {
			return e
		}
		var b string
		e = tx.QueryRow(ctx, `SELECT balance::text FROM token_accounts WHERE account_id=$1 FOR UPDATE`, p.account).Scan(&b)
		if e != nil {
			return e
		}
		cur, ok := new(big.Int).SetString(b, 10)
		if !ok {
			return ErrValidation
		}
		d, ok := new(big.Int).SetString(p.delta, 10)
		if !ok {
			return ErrValidation
		}
		if new(big.Int).Add(cur, d).Sign() < 0 {
			return ErrLimit
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO ledger_transfers(idempotency_key,reason,competition_id) VALUES($1,$2,NULLIF($3,'')::uuid)`, key, reason, competition)
	if e != nil {
		return e
	}
	for _, p := range ps {
		_, e = tx.Exec(ctx, `INSERT INTO ledger_postings(transfer_key,account_id,delta) VALUES($1,$2,$3)`, key, p.account, p.delta)
		if e != nil {
			return e
		}
		if !strings.HasPrefix(p.account, "system:") {
			_, e = tx.Exec(ctx, `UPDATE token_accounts SET balance=balance+$1::numeric,updated_at=now() WHERE account_id=$2`, p.delta, p.account)
			if e != nil {
				return e
			}
		}
	}
	return nil
}

// The competition listing is public and unauthenticated, so it is paged rather
// than returning the whole table.
const defaultCompetitionPageSize = 50
const maxCompetitionPageSize = 100

type CompetitionPage struct {
	Competitions []Competition
	NextCursor   string
}

type competitionCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

// ListCompetitions returns one page in three queries, whatever the page size.
// It used to select every non-draft competition with no limit and then call
// competition() per row, which issues one to three queries of its own — a few
// concurrent anonymous requests were enough to exhaust a ten-connection pool
// and take the whole API down, writes included.
func (s *Store) ListCompetitions(ctx context.Context, viewer, cursor string, pageSize int) (CompetitionPage, error) {
	if pageSize <= 0 {
		pageSize = defaultCompetitionPageSize
	}
	if pageSize > maxCompetitionPageSize {
		pageSize = maxCompetitionPageSize
	}
	args := []any{}
	where := `WHERE phase<>'draft'`
	if cursor != "" {
		after, e := decodeCompetitionCursor(cursor)
		if e != nil {
			return CompetitionPage{}, ErrValidation
		}
		args = append(args, after.CreatedAt, mustUUID(after.ID))
		where += ` AND (created_at, id) < ($1, $2)`
	}
	args = append(args, pageSize+1)
	rows, e := s.db.Query(ctx, competitionSelect+` `+where+
		` ORDER BY created_at DESC, id DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if e != nil {
		return CompetitionPage{}, e
	}
	out := []Competition{}
	for rows.Next() {
		x, z := scanCompetition(rows)
		if z != nil {
			rows.Close()
			return CompetitionPage{}, z
		}
		out = append(out, x)
	}
	// Closed before the hydration queries run, so the pool is not asked for a
	// second connection while this cursor is still open.
	rows.Close()
	if e = rows.Err(); e != nil {
		return CompetitionPage{}, e
	}

	page := CompetitionPage{Competitions: out}
	if len(out) > pageSize {
		out = out[:pageSize]
		page.Competitions = out
		page.NextCursor, _ = encodeCompetitionCursor(out[len(out)-1])
	}
	if e = s.hydrateCompetitions(ctx, page.Competitions, viewer); e != nil {
		return CompetitionPage{}, e
	}
	return page, nil
}

// hydrateCompetitions fills in the two fields the row itself cannot carry:
// whether the viewer has joined, and the ranked results of a settled contest.
// Both are one set-based query for the whole page.
func (s *Store) hydrateCompetitions(ctx context.Context, list []Competition, viewer string) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	byID := make(map[string]*Competition, len(list))
	settled := []string{}
	for i := range list {
		ids[i] = list[i].ID
		byID[list[i].ID] = &list[i]
		if list[i].Phase == "settled" {
			settled = append(settled, list[i].ID)
		}
	}
	if viewer != "" {
		rows, e := s.db.Query(ctx, `SELECT competition_id FROM competition_participants WHERE user_id=$1 AND competition_id = ANY($2::uuid[])`, viewer, ids)
		if e != nil {
			return e
		}
		for rows.Next() {
			var cid uuid.UUID
			if e = rows.Scan(&cid); e != nil {
				rows.Close()
				return e
			}
			if c := byID[cid.String()]; c != nil {
				c.IsJoined = true
			}
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
	}
	if len(settled) == 0 {
		return nil
	}
	return s.eachRow(ctx, `SELECT competition_id,rank,user_id,submission_id,votes,amount::text FROM competition_results WHERE competition_id = ANY($1::uuid[]) ORDER BY competition_id,rank`, settled, func(rows pgx.Rows) error {
		var cid uuid.UUID
		var r CompetitionResult
		if e := rows.Scan(&cid, &r.Rank, &r.UserID, &r.SubmissionID, &r.Votes, &r.Amount); e != nil {
			return e
		}
		if c := byID[cid.String()]; c != nil {
			c.Results = append(c.Results, r)
		}
		return nil
	})
}

func encodeCompetitionCursor(c Competition) (string, error) {
	raw, e := json.Marshal(competitionCursor{CreatedAt: c.createdAt, ID: c.ID})
	if e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCompetitionCursor(v string) (competitionCursor, error) {
	raw, e := base64.RawURLEncoding.DecodeString(v)
	if e != nil {
		return competitionCursor{}, e
	}
	var out competitionCursor
	if e = json.Unmarshal(raw, &out); e != nil {
		return competitionCursor{}, e
	}
	if _, e = uuid.Parse(out.ID); e != nil {
		return competitionCursor{}, e
	}
	return out, nil
}
func (s *Store) GetCompetition(ctx context.Context, id, viewer string) (Competition, error) {
	return s.competition(ctx, id, viewer)
}
func (s *Store) ListDrafts(ctx context.Context, user string) ([]Competition, error) {
	rows, e := s.db.Query(ctx, `SELECT id FROM competitions WHERE creator_id=$1 AND phase='draft' ORDER BY updated_at DESC`, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Competition{}
	for rows.Next() {
		var id uuid.UUID
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		x, z := s.competition(ctx, id.String(), user)
		if z != nil {
			return nil, z
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) SaveDraft(ctx context.Context, user string, id string, in CompetitionInput) (Competition, error) {
	if strings.TrimSpace(in.Title) == "" {
		return Competition{}, ErrValidation
	}
	prize, e := amount(ptrString(in.PrizeAmount))
	if e != nil {
		return Competition{}, e
	}
	fee, e := amount(ptrString(in.EntryFee))
	if e != nil {
		return Competition{}, e
	}
	if id == "" {
		id = uuid.NewString()
		// Only creating a draft is metered. Editing one is bounded by the
		// drafts a user already has, and a host revising a competition
		// repeatedly is ordinary use.
		tx, z := s.db.Begin(ctx)
		if z != nil {
			return Competition{}, z
		}
		defer tx.Rollback(ctx)
		if z = s.consumeDailyQuota(ctx, tx, user, "competition-draft", MaxCompetitionDraftsPerDay); z != nil {
			return Competition{}, z
		}
		if _, z = tx.Exec(ctx, `INSERT INTO competitions(id,creator_id,creator_name,title,description,category,tags,max_participants,start_at,deadline_at,voting_deadline_at,prize_amount,entry_fee) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, user, defaultName(in.CreatorName), strings.TrimSpace(in.Title), in.Description, in.Category, in.Tags, in.MaxParticipants, in.StartDate, in.Deadline, in.VotingDeadline, prize, fee); z != nil {
			return Competition{}, z
		}
		if z = tx.Commit(ctx); z != nil {
			return Competition{}, z
		}
		return s.competition(ctx, id, user)
	}
	_, e = s.db.Exec(ctx, `UPDATE competitions SET title=$1,description=$2,category=$3,tags=$4,max_participants=$5,start_at=$6,deadline_at=$7,voting_deadline_at=$8,prize_amount=$9,entry_fee=$10,updated_at=now() WHERE id=$11 AND creator_id=$12 AND phase='draft'`, in.Title, in.Description, in.Category, in.Tags, in.MaxParticipants, in.StartDate, in.Deadline, in.VotingDeadline, prize, fee, id, user)
	if e != nil {
		return Competition{}, e
	}
	return s.competition(ctx, id, user)
}
func (s *Store) PublishCompetition(ctx context.Context, id, user string) (Competition, error) {
	c, e := s.competition(ctx, id, user)
	if e != nil {
		return c, e
	}
	if c.CreatorID != user || c.Phase != "draft" {
		return c, ErrForbidden
	}
	if c.StartDate == nil || c.Deadline == nil || c.VotingDeadline == nil || !c.StartDate.Before(*c.Deadline) || !c.Deadline.Before(*c.VotingDeadline) {
		return c, ErrValidation
	}
	if c.PrizePool.Amount == "0" {
		return c, ErrValidation
	}
	if e = s.transfer(ctx, "escrow:fund:"+id, "escrow:fund", id, []posting{{account(user), "-" + c.PrizePool.Amount}, {escrow(id), c.PrizePool.Amount}}); e != nil {
		return c, e
	}
	phase := "scheduled"
	if !time.Now().Before(*c.StartDate) {
		phase = "open"
	}
	_, e = s.db.Exec(ctx, `UPDATE competitions SET phase=$1,updated_at=now() WHERE id=$2`, phase, id)
	if e != nil {
		return c, e
	}
	return s.competition(ctx, id, user)
}
func (s *Store) DiscardCompetitionDraft(ctx context.Context, id, user string) error {
	cmd, e := s.db.Exec(ctx, `DELETE FROM competitions WHERE id=$1 AND creator_id=$2 AND phase='draft'`, id, user)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() == 0 {
		return s.competitionWrite(ctx, id, user)
	}
	return nil
}
func (s *Store) UpdateCompetition(ctx context.Context, id, user string, in CompetitionInput) (Competition, error) {
	c, e := s.competition(ctx, id, user)
	if e != nil {
		return c, e
	}
	if c.CreatorID != user {
		return c, ErrForbidden
	}
	if c.Phase != "scheduled" && c.Phase != "open" {
		return c, ErrValidation
	}
	title := c.Title
	if strings.TrimSpace(in.Title) != "" {
		title = strings.TrimSpace(in.Title)
	}
	desc := c.Description
	if in.Description != "" {
		desc = in.Description
	}
	category := c.Category
	if in.Category != "" {
		category = in.Category
	}
	tags := c.Tags
	if in.Tags != nil {
		tags = in.Tags
	}
	max := c.MaxParticipants
	if in.MaxParticipants != nil {
		max = in.MaxParticipants
	}
	start := c.StartDate
	if in.StartDate != nil {
		start = in.StartDate
	}
	deadline := c.Deadline
	if in.Deadline != nil {
		deadline = in.Deadline
	}
	voting := c.VotingDeadline
	if in.VotingDeadline != nil {
		voting = in.VotingDeadline
	}
	if start == nil || deadline == nil || voting == nil || !start.Before(*deadline) || !deadline.Before(*voting) {
		return c, ErrValidation
	}
	_, e = s.db.Exec(ctx, `UPDATE competitions SET title=$1,description=$2,category=$3,tags=$4,max_participants=$5,start_at=$6,deadline_at=$7,voting_deadline_at=$8,updated_at=now() WHERE id=$9`, title, desc, category, tags, max, start, deadline, voting, id)
	if e != nil {
		return c, e
	}
	return s.competition(ctx, id, user)
}
func (s *Store) JoinCompetition(ctx context.Context, id, user string) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var phase string
	var max *int
	var n int
	e = tx.QueryRow(ctx, `SELECT phase,max_participants,participants_count FROM competitions WHERE id=$1 FOR UPDATE`, id).Scan(&phase, &max, &n)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if phase != "open" && phase != "scheduled" {
		return ErrValidation
	}
	// Joining is idempotent, so the capacity check must not reject someone who
	// already holds a seat — otherwise a participant re-opening a full
	// competition is told it is full.
	var already bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM competition_participants WHERE competition_id=$1 AND user_id=$2)`, id, user).Scan(&already); e != nil {
		return e
	}
	if !already && max != nil && n >= *max {
		return ErrLimit
	}
	cmd, e := tx.Exec(ctx, `INSERT INTO competition_participants(competition_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, user)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() > 0 {
		_, e = tx.Exec(ctx, `UPDATE competitions SET participants_count=participants_count+1,updated_at=now() WHERE id=$1`, id)
	}
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) SubmitCompetition(ctx context.Context, id, user, storyID string) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var phase, owner, title, author, cover string
	var fee string
	// A malformed story id would otherwise reach the uuid cast and surface as
	// a 500 rather than a 404.
	if _, z := uuid.Parse(storyID); z != nil {
		return ErrNotFound
	}
	// $2 is the story, referenced once: an earlier version bound the caller's
	// id as an extra parameter the SQL never used, which PostgreSQL cannot
	// type-infer — it made every submission fail with a 500.
	var prize, escrowed string
	e = tx.QueryRow(ctx, `SELECT c.phase,s.owner_id,s.title,COALESCE(s.author_name,''),COALESCE(s.cover_image_url,''),c.entry_fee::text,c.prize_amount::text,COALESCE((SELECT a.balance::text FROM token_accounts a WHERE a.account_id=$3),'0') FROM competitions c JOIN stories s ON s.id=$2 WHERE c.id=$1`, id, storyID, escrow(id)).Scan(&phase, &owner, &title, &author, &cover, &fee, &prize, &escrowed)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if phase != "open" || owner != user {
		return ErrForbidden
	}
	// Defence in depth against an unfunded competition taking money. Publish
	// is the only path that opens one and it escrows the prize first, so the
	// escrow always covers the advertised prize on top of the fees held in it.
	// If it does not, the competition cannot pay out and must not collect.
	held, ok := new(big.Int).SetString(escrowed, 10)
	want, ok2 := new(big.Int).SetString(prize, 10)
	if !ok || !ok2 || held.Cmp(want) < 0 {
		return ErrValidation
	}
	var joined bool
	_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM competition_participants WHERE competition_id=$1 AND user_id=$2)`, id, user).Scan(&joined)
	if !joined {
		return ErrForbidden
	}
	var submitted bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM competition_submissions WHERE competition_id=$1 AND user_id=$2 AND status='submitted')`, id, user).Scan(&submitted); e != nil {
		return e
	}
	if submitted {
		return ErrConflict
	}
	// The fee move and the submission row commit together. They used to be two
	// transactions with a commit between them, so a crash in the gap left the
	// entry fee sitting in escrow with nothing entered against it.
	attempt := 1
	if fee != "0" {
		// A withdrawal refunds the previous charge, so re-entering is a new
		// one and needs an idempotency key the ledger has not seen.
		var prior int
		if e = tx.QueryRow(ctx, `SELECT COALESCE(max(attempt),0) FROM competition_contributions WHERE competition_id=$1 AND user_id=$2`, id, user).Scan(&prior); e != nil {
			return e
		}
		attempt = prior + 1
		if e = s.initialGrantTx(ctx, tx, user); e != nil {
			return e
		}
		if e = s.transferTx(ctx, tx, entryKey(id, user, attempt), "escrow:entry", id, []posting{{account(user), "-" + fee}, {escrow(id), fee}}); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO competition_submissions(competition_id,user_id,story_id,story_title,story_author_name,cover_image_url) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(competition_id,user_id) DO UPDATE SET story_id=EXCLUDED.story_id,story_title=EXCLUDED.story_title,story_author_name=EXCLUDED.story_author_name,cover_image_url=EXCLUDED.cover_image_url,status='submitted',updated_at=now()`, id, user, storyID, title, author, cover)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE competitions SET submission_count=(SELECT count(*) FROM competition_submissions WHERE competition_id=$1 AND status='submitted'),entry_fees_held=entry_fees_held+$2::numeric WHERE id=$1`, id, fee)
	if e != nil {
		return e
	}
	if fee != "0" {
		_, e = tx.Exec(ctx, `INSERT INTO competition_contributions(competition_id,user_id,amount,attempt) VALUES($1,$2,$3,$4) ON CONFLICT(competition_id,user_id) DO UPDATE SET amount=EXCLUDED.amount,attempt=EXCLUDED.attempt,state='held',updated_at=now()`, id, user, fee, attempt)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

// WithdrawCompetitionSubmission refunds the entry fee and retracts the entry.
// The whole thing runs in one transaction behind a lock on the competition
// row: an earlier version read with a plain SELECT and then issued three
// unguarded UPDATEs, so a concurrent withdrawal could refund twice or leave
// the counters disagreeing with the ledger.
func (s *Store) WithdrawCompetitionSubmission(ctx context.Context, id, user string) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var phase string
	e = tx.QueryRow(ctx, `SELECT phase FROM competitions WHERE id=$1 FOR UPDATE`, id).Scan(&phase)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if phase != "open" {
		return ErrValidation
	}
	var fee string
	var held bool
	var attempt int
	e = tx.QueryRow(ctx, `SELECT COALESCE(c.amount::text,'0'),COALESCE(c.state='held',false),COALESCE(c.attempt,0) FROM competition_submissions s LEFT JOIN competition_contributions c ON c.competition_id=s.competition_id AND c.user_id=s.user_id WHERE s.competition_id=$1 AND s.user_id=$2 AND s.status='submitted'`, id, user).Scan(&fee, &held, &attempt)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if held && fee != "0" {
		if e = s.transferTx(ctx, tx, refundKey(id, user, attempt), "escrow:entry-refund", id, []posting{{escrow(id), "-" + fee}, {account(user), fee}}); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE competition_submissions SET status='withdrawn',updated_at=now() WHERE competition_id=$1 AND user_id=$2`, id, user)
	if e != nil {
		return e
	}
	if held {
		_, e = tx.Exec(ctx, `UPDATE competition_contributions SET state='refunded',updated_at=now() WHERE competition_id=$1 AND user_id=$2`, id, user)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE competitions SET submission_count=GREATEST(0,submission_count-1),entry_fees_held=GREATEST(0,entry_fees_held-$2::numeric),updated_at=now() WHERE id=$1`, id, fee)
	} else {
		_, e = tx.Exec(ctx, `UPDATE competitions SET submission_count=GREATEST(0,submission_count-1),updated_at=now() WHERE id=$1`, id)
	}
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) CancelCompetition(ctx context.Context, id, user string, admin bool, reason string) (Competition, error) {
	c, e := s.competition(ctx, id, user)
	if e != nil {
		return c, e
	}
	if !admin && c.CreatorID != user {
		return c, ErrForbidden
	}
	if c.Phase == "settled" || c.Phase == "settling" {
		return c, ErrValidation
	}
	if c.Phase == "cancelled" {
		return c, nil
	}
	posts := map[string]*big.Int{account(c.CreatorID): new(big.Int)}
	rows, e := s.db.Query(ctx, `SELECT user_id,amount::text FROM competition_contributions WHERE competition_id=$1 AND state='held'`, id)
	if e != nil {
		return c, e
	}
	for rows.Next() {
		var u, a string
		if e = rows.Scan(&u, &a); e != nil {
			rows.Close()
			return c, e
		}
		n, _ := new(big.Int).SetString(a, 10)
		if posts[account(u)] == nil {
			posts[account(u)] = new(big.Int)
		}
		posts[account(u)].Add(posts[account(u)], n)
	}
	rows.Close()
	seed, _ := new(big.Int).SetString(c.PrizePool.Amount, 10)
	posts[account(c.CreatorID)].Add(posts[account(c.CreatorID)], seed)
	total := new(big.Int)
	for _, n := range posts {
		total.Add(total, n)
	}
	ps := []posting{{escrow(id), new(big.Int).Neg(total).String()}}
	for a, n := range posts {
		if n.Sign() > 0 {
			ps = append(ps, posting{a, n.String()})
		}
	}
	if total.Sign() > 0 {
		if e = s.transfer(ctx, "escrow:refund:competition:"+id, "escrow:refund", id, ps); e != nil {
			return c, e
		}
	}
	_, e = s.db.Exec(ctx, `UPDATE competitions SET phase='cancelled',entry_fees_held=0,cancellation_reason=$1,updated_at=now() WHERE id=$2`, reason, id)
	if e != nil {
		return c, e
	}
	_, e = s.db.Exec(ctx, `UPDATE competition_contributions SET state='refunded',updated_at=now() WHERE competition_id=$1 AND state='held'`, id)
	if e != nil {
		return c, e
	}
	return s.competition(ctx, id, user)
}

func (s *Store) ListSubmissions(ctx context.Context, id string) ([]CompetitionSubmission, error) {
	return listSubmissions(ctx, s.db, id)
}

// querier is the read surface shared by *pgxpool.Pool and pgx.Tx, so a helper
// can run either standalone or inside a caller's transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func listSubmissions(ctx context.Context, q querier, id string) ([]CompetitionSubmission, error) {
	rows, e := q.Query(ctx, `SELECT user_id,story_id,story_title,story_author_name,cover_image_url,status,submitted_at FROM competition_submissions WHERE competition_id=$1 AND status='submitted' ORDER BY submitted_at`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	x := []CompetitionSubmission{}
	for rows.Next() {
		var v CompetitionSubmission
		var sid uuid.UUID
		if e = rows.Scan(&v.UserID, &sid, &v.StoryTitle, &v.StoryAuthorName, &v.CoverImageURL, &v.Status, &v.SubmittedAt); e != nil {
			return nil, e
		}
		v.ID = v.UserID
		v.StoryID = sid.String()
		x = append(x, v)
	}
	return x, rows.Err()
}
func (s *Store) MyBallot(ctx context.Context, id, user string) (CompetitionBallot, error) {
	var x CompetitionBallot
	e := s.db.QueryRow(ctx, `SELECT voter_id,cast_at,updated_at FROM competition_ballots WHERE competition_id=$1 AND voter_id=$2`, id, user).Scan(&x.VoterID, &x.CastAt, &x.UpdatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return CompetitionBallot{VoterID: user, SubmissionIDs: []string{}}, nil
	}
	if e != nil {
		return x, e
	}
	rows, e := s.db.Query(ctx, `SELECT submission_user_id FROM competition_ballot_choices WHERE competition_id=$1 AND voter_id=$2`, id, user)
	if e != nil {
		return x, e
	}
	defer rows.Close()
	x.SubmissionIDs = []string{}
	for rows.Next() {
		var z string
		if e = rows.Scan(&z); e != nil {
			return x, e
		}
		x.SubmissionIDs = append(x.SubmissionIDs, z)
	}
	return x, rows.Err()
}

// voterEligibleTx decides who may influence a prize that pays out
// winner-take-all. A ballot used to need nothing but an authenticated uid, so
// anyone able to sign up — free, unlimited and unverified — could register
// throwaway accounts and hand themselves any competition on the platform.
//
// Two cheap-for-honest-users, costly-for-sybils conditions apply. The voter
// must have joined the competition, which closes when entries do, so a slate
// of accounts has to exist and register before the outcome is knowable. And
// the voter must hold a public profile that is at least VoterMinProfileAge
// old: a profile costs a unique username, and the age requirement means a
// batch of accounts minted for one competition cannot vote in it.
//
// Neither is sufficient alone and neither is a substitute for rate limiting
// (finding 4); they raise the price of a sybil slate, they do not make one
// impossible.
func (s *Store) voterEligibleTx(ctx context.Context, tx pgx.Tx, id, user string) error {
	var joined bool
	if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM competition_participants WHERE competition_id=$1 AND user_id=$2)`, id, user).Scan(&joined); e != nil {
		return e
	}
	if !joined {
		return ErrForbidden
	}
	var created time.Time
	e := tx.QueryRow(ctx, `SELECT created_at FROM public_profiles WHERE user_id=$1`, user).Scan(&created)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if e != nil {
		return e
	}
	if time.Since(created) < s.VoterMinProfileAge {
		return ErrForbidden
	}
	return nil
}

func (s *Store) CastBallot(ctx context.Context, id, user string, choices []string) error {
	choices = unique(choices)
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var phase string
	var maxVotes int
	e = tx.QueryRow(ctx, `SELECT phase,max_votes_per_user FROM competitions WHERE id=$1 FOR UPDATE`, id).Scan(&phase, &maxVotes)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if phase != "voting" {
		return ErrValidation
	}
	// The per-competition ceiling, which the host sets; the hard-coded 3 this
	// replaces ignored the column entirely.
	if len(choices) > maxVotes {
		return ErrValidation
	}
	if e = s.voterEligibleTx(ctx, tx, id, user); e != nil {
		return e
	}
	for _, v := range choices {
		if v == user {
			return ErrValidation
		}
		var valid bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM competition_submissions WHERE competition_id=$1 AND user_id=$2 AND status='submitted')`, id, v).Scan(&valid)
		if e != nil {
			return e
		}
		if !valid {
			return ErrValidation
		}
	}
	var exists bool
	_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM competition_ballots WHERE competition_id=$1 AND voter_id=$2)`, id, user).Scan(&exists)
	_, e = tx.Exec(ctx, `INSERT INTO competition_ballots(competition_id,voter_id) VALUES($1,$2) ON CONFLICT(competition_id,voter_id) DO UPDATE SET updated_at=now()`, id, user)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `DELETE FROM competition_ballot_choices WHERE competition_id=$1 AND voter_id=$2`, id, user)
	if e != nil {
		return e
	}
	for _, v := range choices {
		_, e = tx.Exec(ctx, `INSERT INTO competition_ballot_choices(competition_id,voter_id,submission_user_id) VALUES($1,$2,$3)`, id, user, v)
		if e != nil {
			return e
		}
	}
	if !exists {
		_, e = tx.Exec(ctx, `UPDATE competitions SET ballot_count=ballot_count+1 WHERE id=$1`, id)
	}
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) AdvanceCompetition(ctx context.Context, id, user, target string, admin bool) (Competition, error) {
	c, e := s.competition(ctx, id, user)
	if e != nil {
		return c, e
	}
	if !admin && c.CreatorID != user {
		return c, ErrForbidden
	}
	// Advance is the scheduler, not a general phase setter: the only
	// transitions it performs are the ones the clock already implies. Every
	// phase change that moves money has its own endpoint that moves it —
	// draft->open escrows the prize in PublishCompetition, and ->cancelled
	// refunds the prize and every held entry fee in CancelCompetition.
	// Honouring a caller-supplied target let a creator open a competition
	// whose prize was never escrowed, collect entry fees against the
	// advertised prize, and then "cancel" it without refunding a thing.
	next := ""
	now := time.Now()
	switch {
	case c.Phase == "scheduled" && c.StartDate != nil && !now.Before(*c.StartDate):
		next = "open"
	case c.Phase == "open" && c.Deadline != nil && now.After(*c.Deadline):
		next = "voting"
	}
	// A target is accepted only as an assertion about the transition that is
	// already due. Asking for anything else is a request for a door this
	// endpoint does not have.
	if target != "" && target != next {
		return c, ErrValidation
	}
	if next == "" {
		return c, nil
	}
	// Guarded on the phase it was read at, so two concurrent calls cannot both
	// apply the transition.
	_, e = s.db.Exec(ctx, `UPDATE competitions SET phase=$1,updated_at=now() WHERE id=$2 AND phase=$3`, next, id, c.Phase)
	if e != nil {
		return c, e
	}
	return s.competition(ctx, id, user)
}
func (s *Store) SettleCompetition(ctx context.Context, id, user string, admin bool) (Competition, error) {
	c, e := s.competition(ctx, id, user)
	if e != nil {
		return c, e
	}
	if !admin && c.CreatorID != user {
		return c, ErrForbidden
	}
	if c.Phase == "settled" {
		return c, nil
	}
	if c.Phase != "voting" && c.Phase != "settling" {
		return c, ErrValidation
	}
	// Everything below — the settling claim, the payout, the results rows and
	// the final settled phase — commits or rolls back together. Claiming the
	// phase on its own connection used to survive a failed payout, leaving the
	// competition in 'settling', which settle re-entered and cancel refused:
	// the escrow was then unreachable by any code path.
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return c, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE competitions SET phase='settling',settlement_claimed_at=COALESCE(settlement_claimed_at,now()) WHERE id=$1 AND phase IN ('voting','settling')`, id)
	if e != nil {
		return c, e
	}
	subs, e := listSubmissions(ctx, tx, id)
	if e != nil {
		return c, e
	}
	votes := map[string]int{}
	rows, e := tx.Query(ctx, `SELECT submission_user_id,count(*) FROM competition_ballot_choices WHERE competition_id=$1 GROUP BY submission_user_id`, id)
	if e != nil {
		return c, e
	}
	for rows.Next() {
		var u string
		var n int
		if e = rows.Scan(&u, &n); e != nil {
			rows.Close()
			return c, e
		}
		votes[u] = n
	}
	rows.Close()
	sort.Slice(subs, func(i, j int) bool {
		if votes[subs[i].UserID] != votes[subs[j].UserID] {
			return votes[subs[i].UserID] > votes[subs[j].UserID]
		}
		return subs[i].SubmittedAt.Before(subs[j].SubmittedAt)
	})
	winner := ""
	if len(subs) > 0 && votes[subs[0].UserID] > 0 {
		winner = subs[0].UserID
	}
	post := []posting{{escrow(id), "-" + c.PrizePool.Amount}}
	if winner == "" {
		post = append(post, posting{account(c.CreatorID), c.PrizePool.Amount})
	} else {
		post = append(post, posting{account(winner), c.PrizePool.Amount})
	}
	fees, _ := new(big.Int).SetString(c.EntryFeesHeld, 10)
	if fees != nil && fees.Sign() > 0 {
		platform := new(big.Int).Div(new(big.Int).Mul(fees, big.NewInt(int64(c.FeeBps))), big.NewInt(10000))
		host := new(big.Int).Sub(fees, platform)
		prize, _ := new(big.Int).SetString(c.PrizePool.Amount, 10)
		post[0].delta = new(big.Int).Neg(new(big.Int).Add(prize, fees)).String()
		if platform.Sign() > 0 {
			post = append(post, posting{"platform:treasury", platform.String()})
		}
		if host.Sign() > 0 {
			post = append(post, posting{account(c.CreatorID), host.String()})
		}
	}
	if e = s.transferTx(ctx, tx, "escrow:release:"+id, "escrow:release", id, post); e != nil {
		return c, e
	}
	_, e = tx.Exec(ctx, `DELETE FROM competition_results WHERE competition_id=$1`, id)
	if e != nil {
		return c, e
	}
	digestData := []string{}
	for i, v := range subs {
		amt := "0"
		if i == 0 && winner != "" {
			amt = c.PrizePool.Amount
		}
		_, e = tx.Exec(ctx, `INSERT INTO competition_results(competition_id,rank,user_id,submission_id,votes,amount) VALUES($1,$2,$3,$4,$5,$6)`, id, i+1, v.UserID, v.UserID, votes[v.UserID], amt)
		if e != nil {
			return c, e
		}
		digestData = append(digestData, fmt.Sprintf("%d:%s:%d:%s", i+1, v.UserID, votes[v.UserID], amt))
	}
	d := sha256.Sum256([]byte(strings.Join(digestData, "|")))
	_, e = tx.Exec(ctx, `UPDATE competitions SET phase='settled',entry_fees_held=0,results_digest=$1,settled_at=now(),updated_at=now() WHERE id=$2`, hex.EncodeToString(d[:]), id)
	if e != nil {
		return c, e
	}
	if e = tx.Commit(ctx); e != nil {
		return c, e
	}
	return s.competition(ctx, id, user)
}

// competitionSelect is shared by the single-row read and the listing so the
// two can never drift into returning different shapes of the same record.
const competitionSelect = `SELECT id,title,description,category,tags,creator_id,creator_name,phase,start_at,deadline_at,voting_deadline_at,max_participants,participants_count,submission_count,ballot_count,prize_amount::text,entry_fee::text,fee_bps,entry_fees_held::text,COALESCE(results_digest,''),settled_at,created_at FROM competitions`

func scanCompetition(row pgx.Row) (Competition, error) {
	var x Competition
	var uid uuid.UUID
	var prize, fee string
	e := row.Scan(&uid, &x.Title, &x.Description, &x.Category, &x.Tags, &x.CreatorID, &x.CreatorName, &x.Phase, &x.StartDate, &x.Deadline, &x.VotingDeadline, &x.MaxParticipants, &x.Participants, &x.SubmissionCount, &x.BallotCount, &prize, &fee, &x.FeeBps, &x.EntryFeesHeld, &x.ResultsDigest, &x.SettledAt, &x.createdAt)
	if e != nil {
		return x, e
	}
	x.ID = uid.String()
	x.Organizer = x.CreatorName
	x.Published = x.Phase != "draft"
	x.PrizePool = tokenAmount(prize)
	x.EntryFee = tokenAmount(fee)
	return x, nil
}

func (s *Store) competition(ctx context.Context, id, viewer string) (Competition, error) {
	x, e := scanCompetition(s.db.QueryRow(ctx, competitionSelect+` WHERE id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	if e != nil {
		return x, e
	}
	if viewer != "" {
		_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM competition_participants WHERE competition_id=$1 AND user_id=$2)`, id, viewer).Scan(&x.IsJoined)
	}
	if x.Phase == "settled" {
		rows, z := s.db.Query(ctx, `SELECT rank,user_id,submission_id,votes,amount::text FROM competition_results WHERE competition_id=$1 ORDER BY rank`, id)
		if z != nil {
			return x, z
		}
		defer rows.Close()
		for rows.Next() {
			var r CompetitionResult
			if z = rows.Scan(&r.Rank, &r.UserID, &r.SubmissionID, &r.Votes, &r.Amount); z != nil {
				return x, z
			}
			x.Results = append(x.Results, r)
		}
	}
	return x, nil
}
func unique(v []string) []string {
	m := map[string]bool{}
	for _, x := range v {
		if x != "" {
			m[x] = true
		}
	}
	out := make([]string, 0, len(m))
	for x := range m {
		out = append(out, x)
	}
	return out
}
func (s *Store) competitionWrite(ctx context.Context, id, user string) error {
	var owner string
	e := s.db.QueryRow(ctx, `SELECT creator_id FROM competitions WHERE id=$1`, id).Scan(&owner)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	return ErrForbidden
}
func ptrString(v *string) string {
	if v == nil {
		return "0"
	}
	return *v
}
func defaultName(v string) string {
	if strings.TrimSpace(v) == "" {
		return "Admin"
	}
	return strings.TrimSpace(v)
}
