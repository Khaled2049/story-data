package store

import (
	"context"
	"strings"
	"time"
)

// AwaitingSettlement is an admin list filter rather than a phase: voting has
// closed but nothing has settled the competition, because settlement happens
// on read and nobody has opened it since the deadline.
const AwaitingSettlement = "awaiting-settlement"

type AdminCompetitionPage struct {
	Competitions []Competition `json:"competitions"`
	NextCursor   string        `json:"nextCursor,omitempty"`
}

func (s *Store) ListAdminCompetitions(ctx context.Context, admin bool, search, phase, cursor string, limit int) (AdminCompetitionPage, error) {
	if !admin {
		return AdminCompetitionPage{}, ErrForbidden
	}
	search = strings.TrimSpace(search)
	if len(search) > 500 {
		return AdminCompetitionPage{}, ErrValidation
	}
	switch phase {
	case "", "draft", "scheduled", "open", "voting", "settling", "settled", "cancelled", AwaitingSettlement:
	default:
		return AdminCompetitionPage{}, ErrValidation
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
		after, err := decodeCompetitionCursor(cursor)
		if err != nil || after.CreatedAt.IsZero() {
			return AdminCompetitionPage{}, ErrValidation
		}
		afterTime, afterID = &after.CreatedAt, &after.ID
	}
	rows, err := s.db.Query(ctx, competitionSelect+` WHERE
		($1='' OR strpos(lower(title),lower($1))>0 OR strpos(lower(creator_name),lower($1))>0 OR creator_id=$1 OR id::text=$1
		 OR EXISTS(SELECT 1 FROM public_profiles p WHERE p.user_id=competitions.creator_id AND strpos(lower(p.username),lower($1))>0))
		AND ($2='' OR phase=$2 OR ($2='awaiting-settlement' AND phase='voting' AND voting_deadline_at<=now()))
		AND ($3::timestamptz IS NULL OR (created_at,id)<($3,$4::uuid))
		ORDER BY created_at DESC,id DESC LIMIT $5`, search, phase, afterTime, afterID, limit+1)
	if err != nil {
		return AdminCompetitionPage{}, err
	}
	page := AdminCompetitionPage{Competitions: []Competition{}}
	for rows.Next() {
		x, err := scanCompetition(rows)
		if err != nil {
			rows.Close()
			return AdminCompetitionPage{}, err
		}
		page.Competitions = append(page.Competitions, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AdminCompetitionPage{}, err
	}
	if len(page.Competitions) > limit {
		page.NextCursor, err = encodeCompetitionCursor(page.Competitions[limit-1])
		if err != nil {
			return AdminCompetitionPage{}, err
		}
		page.Competitions = page.Competitions[:limit]
	}
	// Reuse the public result hydration: even moderation must not reveal votes
	// before settlement. No per-entry vote tallies or ballots are selected here.
	err = s.hydrateCompetitions(ctx, page.Competitions, "")
	return page, err
}

func (s *Store) GetAdminCompetition(ctx context.Context, admin bool, actor, id string) (Competition, error) {
	if !admin {
		return Competition{}, ErrForbidden
	}
	c, err := s.competition(ctx, id, "")
	if err != nil {
		return Competition{}, err
	}
	if err = s.db.QueryRow(ctx, `SELECT COALESCE(cancellation_reason,'') FROM competitions WHERE id=$1`, id).Scan(&c.CancellationReason); err != nil {
		return Competition{}, err
	}
	return c, writeAdminAudit(ctx, s.db, actor, "competition.view", id, map[string]any{"creatorId": c.CreatorID, "phase": c.Phase})
}

func (s *Store) DiscardAdminCompetitionDraft(ctx context.Context, admin bool, actor, id string) error {
	if !admin {
		return ErrForbidden
	}
	c, err := s.competition(ctx, id, "")
	if err != nil {
		return err
	}
	if c.Phase != "draft" {
		return conflictErrf("only draft competitions can be deleted; published competitions must use cancellation")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.discardCompetitionDraft(ctx, tx, id, c.CreatorID); err != nil {
		return err
	}
	if err := writeAdminAudit(ctx, tx, actor, "competition.delete", id, map[string]any{"creatorId": c.CreatorID, "title": c.Title}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
