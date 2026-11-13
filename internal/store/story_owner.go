package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// RequireStoryOwner refuses a story uid does not own, published or not. A
// service asserting uid gets this instead of the public reader's allowance;
// ErrNotFound either way, so the answer does not reveal the story exists.
func (s *Store) RequireStoryOwner(ctx context.Context, storyID, uid string) error {
	var owner string
	err := s.db.QueryRow(ctx, `SELECT owner_id FROM stories WHERE id=$1`, storyID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != uid) {
		return ErrNotFound
	}
	return err
}
