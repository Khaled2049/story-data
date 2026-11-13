package e2e

import (
	"context"
	"testing"

	"github.com/kh1011/novelsync-story-data/internal/store"
)

// Pins migration 000048: what each service group may touch. Production logins
// join these groups, so a grant that drifts here drifts there.

func hasPrivilege(t *testing.T, role, table, privilege string) bool {
	t.Helper()
	var ok bool
	if err := testPool.QueryRow(context.Background(),
		`SELECT has_table_privilege($1, $2, $3)`, role, table, privilege).Scan(&ok); err != nil {
		t.Fatalf("has_table_privilege(%s, %s, %s): %v", role, table, privilege, err)
	}
	return ok
}

func TestServiceRoleGrants(t *testing.T) {
	cases := []struct {
		role, table, privilege string
		want                   bool
	}{
		// The API reads and writes rows everywhere, but never the migration log.
		{"story_data_app", "reading_progress", "UPDATE", true},
		{"story_data_app", "recommendations.interactions", "INSERT", true},
		{"story_data_app", "stories", "TRUNCATE", false},
		{"story_data_app", "goose_db_version", "SELECT", true},
		{"story_data_app", "goose_db_version", "INSERT", false},

		// Agents: story content to read, its own queue and index to write.
		{"story_data_agents", "chapters", "SELECT", true},
		{"story_data_agents", "chapters", "UPDATE", false},
		{"story_data_agents", "stories", "UPDATE", false},
		{"story_data_agents", "story_vector_chunks", "DELETE", true},
		{"story_data_agents", "indexing_outbox", "UPDATE", true},
		{"story_data_agents", "indexing_outbox", "DELETE", false},
		{"story_data_agents", "story_voice_learning_state", "INSERT", true},
		{"story_data_agents", "reading_progress", "SELECT", false},
		{"story_data_agents", "public_profiles", "SELECT", false},
		{"story_data_agents", "assistant_messages", "SELECT", false},
		{"story_data_agents", "user_bans", "SELECT", false},
		{"story_data_agents", "recommendations.items", "SELECT", false},

		// recs: its schema plus the published catalog, never private history.
		{"story_data_recs", "recommendations.items", "UPDATE", true},
		{"story_data_recs", "story_tags", "SELECT", true},
		{"story_data_recs", "chapters", "SELECT", true},
		{"story_data_recs", "chapters", "UPDATE", false},
		{"story_data_recs", "reading_progress", "SELECT", false},
		{"story_data_recs", "story_likes", "SELECT", false},
		{"story_data_recs", "assistant_messages", "SELECT", false},
	}
	for _, c := range cases {
		if got := hasPrivilege(t, c.role, c.table, c.privilege); got != c.want {
			t.Errorf("%s %s on %s = %v, want %v", c.role, c.privilege, c.table, got, c.want)
		}
	}

	for _, role := range []string{"story_data_app", "story_data_agents", "story_data_recs"} {
		var login, create bool
		var memberships int
		if err := testPool.QueryRow(context.Background(), `
			SELECT r.rolcanlogin, has_schema_privilege(r.rolname, 'public', 'CREATE'),
			       (SELECT count(*) FROM pg_auth_members m WHERE m.member = r.oid)
			  FROM pg_roles r WHERE r.rolname = $1`, role).Scan(&login, &create, &memberships); err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if login || create || memberships != 0 {
			t.Errorf("%s: login=%v create-in-public=%v memberships=%d; want a bare NOLOGIN group",
				role, login, create, memberships)
		}
	}
}

// A table a later migration adds reaches the API by default privileges, and
// stays out of agents' and recs' reach until a migration grants it.
func TestServiceRoleDefaultPrivileges(t *testing.T) {
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `CREATE TABLE later_feature (id int)`); err != nil {
		t.Fatal(err)
	}
	for role, want := range map[string]bool{
		"story_data_app": true, "story_data_agents": false, "story_data_recs": false,
	} {
		var got bool
		if err := tx.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'later_feature', 'SELECT')`, role).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s SELECT on a new table = %v, want %v", role, got, want)
		}
	}
}

// Privilege checks alone miss FOR UPDATE (needs UPDATE) and ON CONFLICT (needs
// SELECT). Run agents' own statement shapes as the group.
func TestAgentsRoleRunsItsStatements(t *testing.T) {
	reset(t)
	story := newStory(t, alice, "Ledger of Tides")["id"].(string)
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE story_data_agents`); err != nil {
		t.Fatal(err)
	}
	statements := map[string]string{
		"claim outbox": `WITH next AS (
			SELECT id FROM indexing_outbox WHERE delivered_at IS NULL
			ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 5
		) UPDATE indexing_outbox o SET locked_at=now(), attempts=attempts+1
		  FROM next, stories s WHERE o.id=next.id AND s.id=o.story_id
		  RETURNING o.id, s.owner_id`,
		"index budget": `INSERT INTO indexing_usage(user_id,day,pass_count)
			VALUES('` + alice + `',(now() AT TIME ZONE 'utc')::date,1)
			ON CONFLICT(user_id,day) DO UPDATE SET pass_count=indexing_usage.pass_count+1
			WHERE indexing_usage.pass_count < 300 RETURNING pass_count`,
		"voice claim": `INSERT INTO story_voice_learning_state(story_id,last_analyzed_at,day,pass_count,claim_id)
			VALUES('` + story + `',now(),(now() AT TIME ZONE 'utc')::date,1,gen_random_uuid())
			ON CONFLICT(story_id) DO UPDATE SET pass_count=story_voice_learning_state.pass_count+1
			RETURNING claim_id`,
		"replace chunks": `DELETE FROM story_vector_chunks WHERE story_id='` + story + `'`,
		"read context":   `SELECT s.title, c.title FROM stories s LEFT JOIN chapters c ON c.story_id=s.id WHERE s.id='` + story + `'`,
	}
	for name, sql := range statements {
		if _, err := tx.Exec(ctx, "SAVEPOINT s"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Errorf("%s as story_data_agents: %v", name, err)
			if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT s"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT s"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM reading_progress LIMIT 1`); err == nil {
		t.Error("story_data_agents read reading_progress")
	}
}

// The production API refuses to serve as an over-privileged role.
func TestCheckRuntimeRole(t *testing.T) {
	ctx := context.Background()
	if err := store.CheckRuntimeRole(ctx, testPool); err == nil {
		t.Error("CheckRuntimeRole accepted the test superuser")
	}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE story_data_app`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckRuntimeRole(ctx, tx); err != nil {
		t.Errorf("CheckRuntimeRole refused story_data_app: %v", err)
	}
}
