package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	UserID           string    `json:"userId"`
	Username         string    `json:"username"`
	PhotoURL         string    `json:"photoUrl,omitempty"`
	FirstName        string    `json:"firstName,omitempty"`
	LastName         string    `json:"lastName,omitempty"`
	Bio              string    `json:"bio,omitempty"`
	Occupation       string    `json:"occupation,omitempty"`
	Location         string    `json:"location,omitempty"`
	WritingInterests string    `json:"writingInterests,omitempty"`
	WalletAddress    string    `json:"walletAddress,omitempty"`
	GuestbookPolicy  string    `json:"guestbookPolicy"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	FollowerCount    int       `json:"followerCount"`
	IsWriter         bool      `json:"isWriter"`
}

type ProfilePage struct {
	Profiles   []PublicProfile `json:"profiles"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

type profileCursor struct {
	Sort      string     `json:"sort"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	Username  string     `json:"username,omitempty"`
	UserID    string     `json:"userId"`
}

// FollowerEntry backs the "Followed you back" list — who recently followed
// the given user, newest first.
type FollowerEntry struct {
	UserID     string    `json:"userId"`
	Username   string    `json:"username"`
	FollowedAt time.Time `json:"followedAt"`
}

// ProfileInput uses pointers so PATCH can distinguish omitted fields from
// intentional empty-string clears.
type ProfileInput struct {
	Username         *string `json:"username,omitempty"`
	PhotoURL         *string `json:"photoUrl,omitempty"`
	FirstName        *string `json:"firstName,omitempty"`
	LastName         *string `json:"lastName,omitempty"`
	Bio              *string `json:"bio,omitempty"`
	Occupation       *string `json:"occupation,omitempty"`
	Location         *string `json:"location,omitempty"`
	WritingInterests *string `json:"writingInterests,omitempty"`
	WalletAddress    *string `json:"walletAddress,omitempty"`
	GuestbookPolicy  *string `json:"guestbookPolicy,omitempty"`
}

func (s *Store) GetPublicProfile(ctx context.Context, userID string) (PublicProfile, error) {
	x, err := scanPublicProfile(s.db.QueryRow(ctx, profileSelect+` WHERE user_id=$1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicProfile{}, ErrNotFound
	}
	return x, err
}

func (s *Store) ListPublicProfiles(ctx context.Context, prefix string, ids []string, sort, cursor string, limit int) (ProfilePage, error) {
	if limit <= 0 || limit > profileBatchLimit {
		limit = profilePageSize
	}
	if len(ids) > 0 {
		if len(ids) > profileBatchLimit {
			return ProfilePage{}, ErrValidation
		}
		rows, err := s.db.Query(ctx, profileSelect+` WHERE user_id = ANY($1)`, ids)
		if err != nil {
			return ProfilePage{}, err
		}
		defer rows.Close()
		byID := map[string]PublicProfile{}
		for rows.Next() {
			x, e := scanPublicProfile(rows)
			if e != nil {
				return ProfilePage{}, e
			}
			byID[x.UserID] = x
		}
		if err := rows.Err(); err != nil {
			return ProfilePage{}, err
		}
		out := make([]PublicProfile, 0, len(ids))
		for _, id := range ids {
			if x, ok := byID[id]; ok {
				out = append(out, x)
			}
		}
		return ProfilePage{Profiles: out}, nil
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix != "" {
		// LIKE rather than a >= / < range: the range's upper bound was
		// prefix + U+10FFFF, and this database's collation sorts that
		// noncharacter as if it were absent, so the bound collapsed onto the
		// prefix itself and the range matched nothing at all.
		rows, err := s.db.Query(ctx, profileSelect+` WHERE username_lower LIKE $1 ESCAPE '\' ORDER BY username_lower, user_id LIMIT $2`, likePrefix(prefix), limit)
		if err != nil {
			return ProfilePage{}, err
		}
		defer rows.Close()
		out := []PublicProfile{}
		for rows.Next() {
			x, e := scanPublicProfile(rows)
			if e != nil {
				return ProfilePage{}, e
			}
			out = append(out, x)
		}
		return ProfilePage{Profiles: out}, rows.Err()
	}

	if sort != "az" {
		sort = "newest"
	}
	var c profileCursor
	if cursor != "" {
		var err error
		c, err = decodeProfileCursor(cursor)
		if err != nil || c.Sort != sort {
			return ProfilePage{}, ErrValidation
		}
	}

	var query string
	var args []any
	if sort == "az" {
		where := ""
		args = []any{}
		if cursor != "" {
			where = " WHERE (lower(username),user_id) > ($1,$2)"
			args = append(args, c.Username, c.UserID)
		}
		args = append(args, limit+1)
		query = profileSelect + where + ` ORDER BY lower(username) ASC, user_id ASC LIMIT $` + strconvArg(len(args))
	} else {
		where := ""
		args = []any{}
		if cursor != "" {
			where = " WHERE (created_at,user_id) < ($1,$2)"
			args = append(args, *c.CreatedAt, c.UserID)
		}
		args = append(args, limit+1)
		query = profileSelect + where + ` ORDER BY created_at DESC, user_id DESC LIMIT $` + strconvArg(len(args))
	}

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return ProfilePage{}, err
	}
	defer rows.Close()
	items := []PublicProfile{}
	for rows.Next() {
		x, e := scanPublicProfile(rows)
		if e != nil {
			return ProfilePage{}, e
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		return ProfilePage{}, err
	}
	page := ProfilePage{Profiles: items}
	if len(items) > limit {
		page.Profiles = items[:limit]
		page.NextCursor, _ = encodeProfileCursor(sort, page.Profiles[limit-1])
	}
	return page, nil
}

// ListRecentFollowers returns userID's most recent followers, newest first —
// backs the "Followed you back" panel.
func (s *Store) ListRecentFollowers(ctx context.Context, userID string, limit int) ([]FollowerEntry, error) {
	if limit <= 0 || limit > profileBatchLimit {
		limit = profilePageSize
	}
	rows, err := s.db.Query(ctx, `SELECT f.follower_id, COALESCE(p.username,'unknown'), f.created_at
 FROM user_follows f LEFT JOIN public_profiles p ON p.user_id=f.follower_id
 WHERE f.followed_id=$1 ORDER BY f.created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FollowerEntry{}
	for rows.Next() {
		var x FollowerEntry
		if err := rows.Scan(&x.UserID, &x.Username, &x.FollowedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func encodeProfileCursor(sort string, x PublicProfile) (string, error) {
	c := profileCursor{Sort: sort, UserID: x.UserID}
	if sort == "az" {
		c.Username = strings.ToLower(x.Username)
	} else {
		c.CreatedAt = &x.CreatedAt
	}
	b, e := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b), e
}
func decodeProfileCursor(v string) (profileCursor, error) {
	var x profileCursor
	b, e := base64.RawURLEncoding.DecodeString(v)
	if e != nil {
		return x, ErrValidation
	}
	e = json.Unmarshal(b, &x)
	if e != nil || x.UserID == "" || (x.Sort == "az" && x.Username == "") || (x.Sort != "az" && x.CreatedAt == nil) {
		return profileCursor{}, ErrValidation
	}
	return x, nil
}

func (s *Store) UpsertPublicProfile(ctx context.Context, userID string, in ProfileInput) (PublicProfile, error) {
	if in.Username == nil {
		return PublicProfile{}, ErrValidation
	}
	username, ok := normalizeUsername(*in.Username)
	if !ok {
		return PublicProfile{}, ErrValidation
	}
	photo, firstName, lastName, bio, occupation, location, writingInterests, wallet, policy := value(in.PhotoURL), value(in.FirstName), value(in.LastName), value(in.Bio), value(in.Occupation), value(in.Location), value(in.WritingInterests), value(in.WalletAddress), value(in.GuestbookPolicy)
	if policy == "" {
		policy = "everyone"
	}
	if !validGuestbookPolicy(policy) {
		return PublicProfile{}, ErrValidation
	}
	// photoUrl is rendered by every client that shows this profile, so it has
	// to be a real http(s) URL rather than any 2 KB string.
	if !validURL(photo) {
		return PublicProfile{}, ErrValidation
	}
	if !validProfileText(firstName, 50) || !validProfileText(lastName, 50) ||
		!validProfileText(bio, 300) || !validProfileText(occupation, 50) || !validProfileText(location, 50) ||
		!validProfileText(writingInterests, 200) {
		return PublicProfile{}, ErrValidation
	}
	wallet, ok = normalizeWallet(wallet)
	if !ok {
		return PublicProfile{}, ErrValidation
	}
	x, err := scanPublicProfile(s.db.QueryRow(ctx, `INSERT INTO public_profiles (user_id, username, username_lower, photo_url, first_name, last_name, bio, occupation, location, writing_interests, wallet_address, guestbook_policy)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (user_id) DO UPDATE SET username=EXCLUDED.username, username_lower=EXCLUDED.username_lower, photo_url=EXCLUDED.photo_url, first_name=EXCLUDED.first_name, last_name=EXCLUDED.last_name, bio=EXCLUDED.bio, occupation=EXCLUDED.occupation, location=EXCLUDED.location, writing_interests=EXCLUDED.writing_interests, wallet_address=EXCLUDED.wallet_address, guestbook_policy=EXCLUDED.guestbook_policy, updated_at=now()
RETURNING user_id, username, photo_url, first_name, last_name, bio, occupation, location, writing_interests, COALESCE(wallet_address,''), guestbook_policy, created_at, updated_at, `+profileStatsSelect, userID, username, strings.ToLower(username), photo, firstName, lastName, bio, occupation, location, writingInterests, emptyToNull(wallet), policy))
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
	username, photo, firstName, lastName, bio, occupation, location, writingInterests, wallet, policy := current.Username, current.PhotoURL, current.FirstName, current.LastName, current.Bio, current.Occupation, current.Location, current.WritingInterests, current.WalletAddress, current.GuestbookPolicy
	if in.Username != nil {
		username = *in.Username
	}
	if in.PhotoURL != nil {
		photo = *in.PhotoURL
	}
	if in.FirstName != nil {
		firstName = *in.FirstName
	}
	if in.LastName != nil {
		lastName = *in.LastName
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
	if in.WritingInterests != nil {
		writingInterests = *in.WritingInterests
	}
	if in.WalletAddress != nil {
		wallet = *in.WalletAddress
	}
	if in.GuestbookPolicy != nil {
		policy = *in.GuestbookPolicy
	}
	return s.UpsertPublicProfile(ctx, userID, ProfileInput{Username: &username, PhotoURL: &photo, FirstName: &firstName, LastName: &lastName, Bio: &bio, Occupation: &occupation, Location: &location, WritingInterests: &writingInterests, WalletAddress: &wallet, GuestbookPolicy: &policy})
}

const profileStatsSelect = `(SELECT count(*) FROM user_follows WHERE followed_id=user_id), EXISTS(SELECT 1 FROM stories WHERE owner_id=user_id AND is_published)`
const profileSelect = `SELECT user_id, username, photo_url, first_name, last_name, bio, occupation, location, writing_interests, COALESCE(wallet_address,''), guestbook_policy, created_at, updated_at, ` + profileStatsSelect + ` FROM public_profiles`

func scanPublicProfile(row pgx.Row) (PublicProfile, error) {
	var x PublicProfile
	err := row.Scan(&x.UserID, &x.Username, &x.PhotoURL, &x.FirstName, &x.LastName, &x.Bio, &x.Occupation, &x.Location, &x.WritingInterests, &x.WalletAddress, &x.GuestbookPolicy, &x.CreatedAt, &x.UpdatedAt, &x.FollowerCount, &x.IsWriter)
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
