package store

import (
	"context"
	"strings"
	"unicode/utf8"
)

const maxBanReason = 500

// IsBanned is checked on every authenticated request, so it stays one primary-key lookup.
func (s *Store) IsBanned(ctx context.Context, uid string) (bool, error) {
	var banned bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_bans WHERE user_id=$1)`, uid).Scan(&banned)
	return banned, err
}

// BanUser records or replaces a ban. An admin cannot ban themselves.
func (s *Store) BanUser(ctx context.Context, admin bool, actor, uid, reason string) error {
	if !admin {
		return ErrForbidden
	}
	reason = strings.TrimSpace(reason)
	if uid == "" || len(uid) > 128 || uid == actor || utf8.RuneCountInString(reason) > maxBanReason {
		return ErrValidation
	}
	_, err := s.db.Exec(ctx, `INSERT INTO user_bans (user_id, reason, banned_by) VALUES ($1, NULLIF($2,''), $3)
		ON CONFLICT (user_id) DO UPDATE SET reason=EXCLUDED.reason, banned_by=EXCLUDED.banned_by, banned_at=now()`,
		uid, reason, actor)
	return err
}

// UnbanUser is idempotent: lifting a ban that is not there succeeds.
func (s *Store) UnbanUser(ctx context.Context, admin bool, uid string) error {
	if !admin {
		return ErrForbidden
	}
	_, err := s.db.Exec(ctx, `DELETE FROM user_bans WHERE user_id=$1`, uid)
	return err
}
