package store

import (
	"context"
	"strings"
	"time"
)

type AdminMember struct {
	UserID              string    `json:"userId"`
	Username            string    `json:"username"`
	FirstName           string    `json:"firstName"`
	LastName            string    `json:"lastName"`
	PhotoURL            string    `json:"photoUrl"`
	CreatedAt           time.Time `json:"createdAt"`
	StoryCount          int       `json:"storyCount"`
	PublishedStoryCount int       `json:"publishedStoryCount"`
}

type AdminMemberPage struct {
	Members    []AdminMember `json:"members"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// A member is anyone with a public profile. Story counts include drafts, which
// the public directory never reveals.
func (s *Store) ListAdminMembers(ctx context.Context, admin bool, search, cursor string, limit int) (AdminMemberPage, error) {
	if !admin {
		return AdminMemberPage{}, ErrForbidden
	}
	search = strings.TrimSpace(search)
	if len(search) > 500 {
		return AdminMemberPage{}, ErrValidation
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
		c, err := decodeProfileCursor(cursor)
		if err != nil || c.Sort != "newest" {
			return AdminMemberPage{}, ErrValidation
		}
		afterTime, afterID = c.CreatedAt, &c.UserID
	}
	rows, err := s.db.Query(ctx, `WITH page AS (
		SELECT user_id, username, first_name, last_name, photo_url, created_at
		FROM public_profiles WHERE
		($1='' OR strpos(username_lower,lower($1))>0 OR strpos(lower(first_name||' '||last_name),lower($1))>0 OR user_id=$1)
		AND ($2::timestamptz IS NULL OR (created_at,user_id)<($2,$3::text))
		ORDER BY created_at DESC,user_id DESC LIMIT $4
	) SELECT page.*,
		(SELECT count(*) FROM stories s WHERE s.owner_id=page.user_id),
		(SELECT count(*) FROM stories s WHERE s.owner_id=page.user_id AND s.is_published)
	FROM page ORDER BY created_at DESC,user_id DESC`, search, afterTime, afterID, limit+1)
	if err != nil {
		return AdminMemberPage{}, err
	}
	defer rows.Close()
	page := AdminMemberPage{Members: []AdminMember{}}
	for rows.Next() {
		var x AdminMember
		if err := rows.Scan(&x.UserID, &x.Username, &x.FirstName, &x.LastName, &x.PhotoURL, &x.CreatedAt, &x.StoryCount, &x.PublishedStoryCount); err != nil {
			return AdminMemberPage{}, err
		}
		page.Members = append(page.Members, x)
	}
	if err := rows.Err(); err != nil {
		return AdminMemberPage{}, err
	}
	if len(page.Members) > limit {
		last := page.Members[limit-1]
		page.NextCursor, err = encodeProfileCursor("newest", PublicProfile{UserID: last.UserID, CreatedAt: last.CreatedAt})
		page.Members = page.Members[:limit]
	}
	return page, err
}

type MemberUsageDay struct {
	Day            string `json:"day"`
	IndexingPasses int    `json:"indexingPasses"`
	RecsSearches   int    `json:"recsSearches"`
	RecsExplains   int    `json:"recsExplains"`
}

type MemberUsage struct {
	UserID string           `json:"userId"`
	From   string           `json:"from"`
	To     string           `json:"to"`
	Days   []MemberUsageDay `json:"days"`
}

// MemberUsage returns one member's daily AI counters kept here: embedding
// passes (indexing_usage) and recommendation LLM calls. Counts only.
func (s *Store) MemberUsage(ctx context.Context, admin bool, userID, fromRaw, toRaw string) (MemberUsage, error) {
	if !admin {
		return MemberUsage{}, ErrForbidden
	}
	from, to, err := memberUsageRange(fromRaw, toRaw, time.Now())
	if err != nil {
		return MemberUsage{}, err
	}
	rows, err := s.db.Query(ctx, `SELECT d::date::text,
		COALESCE((SELECT pass_count FROM indexing_usage WHERE user_id=$1 AND day=d::date), 0),
		COALESCE((SELECT call_count FROM recommendations.llm_usage WHERE user_id=$1 AND day=d::date AND kind='search'), 0),
		COALESCE((SELECT call_count FROM recommendations.llm_usage WHERE user_id=$1 AND day=d::date AND kind='explain'), 0)
	FROM generate_series($2::date, $3::date, interval '1 day') AS d ORDER BY d`, userID, from, to)
	if err != nil {
		return MemberUsage{}, err
	}
	defer rows.Close()
	out := MemberUsage{UserID: userID, From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), Days: []MemberUsageDay{}}
	for rows.Next() {
		var d MemberUsageDay
		if err := rows.Scan(&d.Day, &d.IndexingPasses, &d.RecsSearches, &d.RecsExplains); err != nil {
			return MemberUsage{}, err
		}
		out.Days = append(out.Days, d)
	}
	return out, rows.Err()
}

// memberUsageRange matches creditProxy's usage ranges: inclusive UTC days,
// the last 30 by default, at most 90.
func memberUsageRange(fromRaw, toRaw string, now time.Time) (time.Time, time.Time, error) {
	to := now.UTC().Truncate(24 * time.Hour)
	if toRaw != "" {
		parsed, err := time.Parse("2006-01-02", toRaw)
		if err != nil {
			return time.Time{}, time.Time{}, ErrValidation
		}
		to = parsed
	}
	from := to.AddDate(0, 0, -29)
	if fromRaw != "" {
		parsed, err := time.Parse("2006-01-02", fromRaw)
		if err != nil {
			return time.Time{}, time.Time{}, ErrValidation
		}
		from = parsed
	}
	if from.After(to) || to.Sub(from) >= 90*24*time.Hour {
		return time.Time{}, time.Time{}, ErrValidation
	}
	return from, to, nil
}
