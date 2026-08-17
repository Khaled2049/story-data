package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const profilePageSize = 20
const profileBatchLimit = 50

// PublicProfile is safe to expose to anonymous readers.
type PublicProfile struct {
	UserID          string    `json:"userId"`
	Username        string    `json:"username"`
	PhotoURL        string    `json:"photoUrl,omitempty"`
	Bio             string    `json:"bio,omitempty"`
	Occupation      string    `json:"occupation,omitempty"`
	Location        string    `json:"location,omitempty"`
	WalletAddress   string    `json:"walletAddress,omitempty"`
	GuestbookPolicy string    `json:"guestbookPolicy"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// ProfileInput uses pointers so PATCH can distinguish omitted fields from
// intentional empty-string clears.
type ProfileInput struct {
	Username        *string `json:"username,omitempty"`
	PhotoURL        *string `json:"photoUrl,omitempty"`
	Bio             *string `json:"bio,omitempty"`
	Occupation      *string `json:"occupation,omitempty"`
	Location        *string `json:"location,omitempty"`
	WalletAddress   *string `json:"walletAddress,omitempty"`
	GuestbookPolicy *string `json:"guestbookPolicy,omitempty"`
}

func (s *Store) GetPublicProfile(ctx context.Context, userID string) (PublicProfile, error) {
	x, err := scanPublicProfile(s.db.QueryRow(ctx, profileSelect+` WHERE user_id=$1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicProfile{}, ErrNotFound
	}
	return x, err
}

func (s *Store) ListPublicProfiles(ctx context.Context, prefix string, ids []string, limit int) ([]PublicProfile, error) {
	if limit <= 0 || limit > profileBatchLimit {
		limit = profilePageSize
	}
	if len(ids) > 0 {
		if len(ids) > profileBatchLimit {
			return nil, ErrValidation
		}
		rows, err := s.db.Query(ctx, profileSelect+` WHERE user_id = ANY($1)`, ids)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		byID := map[string]PublicProfile{}
		for rows.Next() {
			x, e := scanPublicProfile(rows)
			if e != nil {
				return nil, e
			}
			byID[x.UserID] = x
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		out := make([]PublicProfile, 0, len(ids))
		for _, id := range ids {
			if x, ok := byID[id]; ok {
				out = append(out, x)
			}
		}
		return out, nil
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	query, args := profileSelect+` ORDER BY created_at DESC, user_id DESC LIMIT $1`, []any{limit}
	if prefix != "" {
		// LIKE rather than a >= / < range: the range's upper bound was
		// prefix + U+10FFFF, and this database's collation sorts that
		// noncharacter as if it were absent, so the bound collapsed onto the
		// prefix itself and the range matched nothing at all.
		query, args = profileSelect+` WHERE username_lower LIKE $1 ESCAPE '\' ORDER BY username_lower, user_id LIMIT $2`, []any{likePrefix(prefix), limit}
	}
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublicProfile{}
	for rows.Next() {
		x, e := scanPublicProfile(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) UpsertPublicProfile(ctx context.Context, userID string, in ProfileInput) (PublicProfile, error) {
	if in.Username == nil {
		return PublicProfile{}, ErrValidation
	}
	username, ok := normalizeUsername(*in.Username)
	if !ok {
		return PublicProfile{}, ErrValidation
	}
	photo, bio, occupation, location, wallet, policy := value(in.PhotoURL), value(in.Bio), value(in.Occupation), value(in.Location), value(in.WalletAddress), value(in.GuestbookPolicy)
	if policy == "" {
		policy = "everyone"
	}
	if !validGuestbookPolicy(policy) {
		return PublicProfile{}, ErrValidation
	}
	if !validProfileText(photo, 2048) || !validProfileText(bio, 300) || !validProfileText(occupation, 50) || !validProfileText(location, 50) {
		return PublicProfile{}, ErrValidation
	}
	wallet, ok = normalizeWallet(wallet)
	if !ok {
		return PublicProfile{}, ErrValidation
	}
	x, err := scanPublicProfile(s.db.QueryRow(ctx, `INSERT INTO public_profiles (user_id, username, username_lower, photo_url, bio, occupation, location, wallet_address, guestbook_policy)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (user_id) DO UPDATE SET username=EXCLUDED.username, username_lower=EXCLUDED.username_lower, photo_url=EXCLUDED.photo_url, bio=EXCLUDED.bio, occupation=EXCLUDED.occupation, location=EXCLUDED.location, wallet_address=EXCLUDED.wallet_address, guestbook_policy=EXCLUDED.guestbook_policy, updated_at=now()
RETURNING user_id, username, photo_url, bio, occupation, location, COALESCE(wallet_address,''), guestbook_policy, created_at, updated_at`, userID, username, strings.ToLower(username), photo, bio, occupation, location, emptyToNull(wallet), policy))
	if isUniqueViolation(err) {
		return PublicProfile{}, ErrUsernameTaken
	}
	return x, err
}

func (s *Store) PatchPublicProfile(ctx context.Context, userID string, in ProfileInput) (PublicProfile, error) {
	current, err := s.GetPublicProfile(ctx, userID)
	if err != nil {
		return PublicProfile{}, err
	}
	username, photo, bio, occupation, location, wallet, policy := current.Username, current.PhotoURL, current.Bio, current.Occupation, current.Location, current.WalletAddress, current.GuestbookPolicy
	if in.Username != nil {
		username = *in.Username
	}
	if in.PhotoURL != nil {
		photo = *in.PhotoURL
	}
	if in.Bio != nil {
		bio = *in.Bio
	}
	if in.Occupation != nil {
		occupation = *in.Occupation
	}
	if in.Location != nil {
		location = *in.Location
	}
	if in.WalletAddress != nil {
		wallet = *in.WalletAddress
	}
	if in.GuestbookPolicy != nil {
		policy = *in.GuestbookPolicy
	}
	return s.UpsertPublicProfile(ctx, userID, ProfileInput{Username: &username, PhotoURL: &photo, Bio: &bio, Occupation: &occupation, Location: &location, WalletAddress: &wallet, GuestbookPolicy: &policy})
}

const profileSelect = `SELECT user_id, username, photo_url, bio, occupation, location, COALESCE(wallet_address,''), guestbook_policy, created_at, updated_at FROM public_profiles`

func scanPublicProfile(row pgx.Row) (PublicProfile, error) {
	var x PublicProfile
	err := row.Scan(&x.UserID, &x.Username, &x.PhotoURL, &x.Bio, &x.Occupation, &x.Location, &x.WalletAddress, &x.GuestbookPolicy, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}
func value(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}
func validProfileText(v string, max int) bool { return len(v) <= max }

// likePrefix turns a search term into a LIKE pattern matching it as a literal
// prefix. Underscore is both a legal username character and a LIKE wildcard, so
// without escaping a search for "ada_b" would also match "adaXb".
func likePrefix(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(v) + "%"
}
func validGuestbookPolicy(v string) bool {
	return v == "everyone" || v == "followers" || v == "following" || v == "mutuals" || v == "nobody"
}
func normalizeUsername(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if len(v) < 3 || len(v) > 20 {
		return "", false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return "", false
		}
	}
	return v, true
}
func normalizeWallet(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "", true
	}
	if len(v) != 42 || !strings.HasPrefix(v, "0x") {
		return "", false
	}
	for _, r := range v[2:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return "", false
		}
	}
	return v, true
}
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
