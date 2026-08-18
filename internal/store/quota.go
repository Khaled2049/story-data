package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

// Daily ceilings for the write paths that had none. They are deliberately
// generous — the point is that no single account can flood a shared surface,
// not that anyone bumps into them while using the product. The guestbook and
// book club domains predate this and keep their own tables and helpers.
const (
	MaxCommentsPerDay          = 100
	MaxRatingsPerDay           = 50
	MaxCompetitionDraftsPerDay = 10
	MaxBookClubsPerDay         = 5
)

// execer is the write surface shared by *pgxpool.Pool and pgx.Tx, so a quota
// can be spent either standalone or inside the transaction that does the work
// it pays for.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// consumeDailyQuota spends one unit of a user's budget for an action, in UTC
// days. The ceiling is enforced in the WHERE clause of the upsert rather than
// by reading the count first, so concurrent requests cannot both observe
// "under the limit" and both proceed.
//
// Pass a transaction whenever the work being metered is itself transactional:
// spending budget for a write that then rolls back would let a user burn their
// own quota on nothing, and worse, would let a failed write escape the count.
func (s *Store) consumeDailyQuota(ctx context.Context, q execer, user, action string, maximum int) error {
	cmd, e := q.Exec(ctx, `INSERT INTO user_daily_usage(user_id,action,day,count)
VALUES($1,$2,current_date,1)
ON CONFLICT(user_id,action,day) DO UPDATE SET count=user_daily_usage.count+1
WHERE user_daily_usage.count < $3`, user, action, maximum)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() == 0 {
		return ErrRateLimit
	}
	return nil
}
