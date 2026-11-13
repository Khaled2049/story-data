# Database roles: production runbook

Each direct database user logs in with a role limited to its job. Migration
`000048_service_roles.sql` creates three permission groups and their grants;
this runbook creates the login roles, their secrets and the migrate service
account, which hold credentials and so stay out of the repository.

| Login | Group | Used by | Can |
|---|---|---|---|
| `neondb_owner` | (owner) | `novelsync-story-data-migrate` job only | Everything, including DDL |
| `story_data_api` | `story_data_app` | story-data API, `sync-recs` job | Read and write rows in `public` and `recommendations`; no DDL, no TRUNCATE |
| `agents_service` | `story_data_agents` | taleTribe-agents | Read story content; write the search index, indexing queue and usage, voice-learning state |
| `recs_app` | `story_data_recs` | taleTribe-recs | Its `recommendations` schema; read `stories`, `story_tags`, `chapters`, `chapter_summaries` |

| Secret | Holds | Readable by |
|---|---|---|
| `story-data-neon-database-url` | owner URL | `novelsync-story-data-migrate` |
| `story-data-database-url-app` | `story_data_api` URL | `novelsync-story-data-run` |
| `story-data-database-url-agents` | `agents_service` URL | `novelsync-agents-run` |
| `recs-postgres-dsn-rw`, `recs-postgres-dsn-ro` | `recs_app` URL | the recs runtime |

**Create logins with SQL, never the Neon console.** The console adds every role
it creates to `neon_superuser`, which carries `pg_read_all_data` and
`pg_write_all_data` and overrides every grant above. That is how the old
`recs_service` came to read every table.

Never paste a password or URL into a shell command line, a ticket or a chat.
The steps below keep them in shell variables for one session.

## Before you start

- `gcloud` signed in as a project owner, project `story-6f89f`.
- Docker, for `psql` (`postgres:17-alpine` image).
- The story-data, agents and frontend `feature/security-hardening` branches
  reviewed and ready to merge. Phase 2 (service identity) ships in the same
  deploys, so story-data and agents must go out together.

Open one terminal and keep it for the whole runbook. Define this helper, which
runs `psql` against the owner URL without putting it on a command line:

```bash
PROJECT=story-6f89f REGION=us-central1
OWNER_URL="$(gcloud secrets versions access latest --secret=story-data-neon-database-url --project=$PROJECT)"
owner_psql() { docker run --rm -i -e URL="$OWNER_URL" postgres:17-alpine sh -c 'psql "$URL" -X -v ON_ERROR_STOP=1 "$@"' -- "$@"; }
owner_psql -tAc 'SELECT current_user'   # expect: neondb_owner
```

## Part A: prepare (no running service changes)

### A1. Migrate service account

```bash
gcloud iam service-accounts create novelsync-story-data-migrate \
  --project=$PROJECT --display-name="story-data migrations"
```

The `github-actions` account already holds project-level `run.admin` and
`iam.serviceAccountUser`, which is what the deploy needs to run a job as it.

### A2. Login roles

Generate the passwords in this shell only:

```bash
PW_API="$(openssl rand -base64 33 | tr -d '/+=')"
PW_AGENTS="$(openssl rand -base64 33 | tr -d '/+=')"
PW_RECS="$(openssl rand -base64 33 | tr -d '/+=')"

owner_psql -v pw_api="$PW_API" -v pw_agents="$PW_AGENTS" -v pw_recs="$PW_RECS" <<'SQL'
CREATE ROLE story_data_api LOGIN PASSWORD :'pw_api';
CREATE ROLE agents_service LOGIN PASSWORD :'pw_agents';
CREATE ROLE recs_app       LOGIN PASSWORD :'pw_recs';
SQL
```

Check none of them joined `neon_superuser` (expect zero rows):

```bash
owner_psql -c "SELECT r.rolname, g.rolname AS member_of FROM pg_auth_members m
  JOIN pg_roles r ON r.oid=m.member JOIN pg_roles g ON g.oid=m.roleid
  WHERE r.rolname IN ('story_data_api','agents_service','recs_app')"
```

They hold no privileges yet. Migration 000048 grants each its group when the
deploy runs it.

### A3. Secrets

Each URL is the owner URL with the user and password swapped, so host,
database and `sslmode` stay identical:

```bash
swap_user() { python3 -c 'import sys,urllib.parse as u
p=u.urlsplit(sys.argv[1]); host=p.netloc.split("@",1)[1]
print(u.urlunsplit(p._replace(netloc=f"{sys.argv[2]}:{u.quote(sys.argv[3],safe=str())}@{host}")))' "$OWNER_URL" "$1" "$2"; }

swap_user story_data_api "$PW_API" | gcloud secrets create story-data-database-url-app \
  --project=$PROJECT --replication-policy=automatic --data-file=-
swap_user agents_service "$PW_AGENTS" | gcloud secrets create story-data-database-url-agents \
  --project=$PROJECT --replication-policy=automatic --data-file=-
```

Keep `PW_RECS` in this shell for B4. Test that a new login connects (it can
log in but read nothing yet, which is expected):

```bash
APP_URL="$(swap_user story_data_api "$PW_API")"
docker run --rm -e URL="$APP_URL" postgres:17-alpine psql "$URL" -XtAc 'SELECT current_user'
unset APP_URL
```

## Part B: deploy and switch

### B1. story-data

Merge story-data's `feature/security-hardening`. The deploy workflow now:

1. points the `novelsync-story-data-migrate` job at the new image and runs it as
   `neondb_owner`, which applies 000048 and grants each login its group;
2. applies the rest: the API and `sync-recs` move to `story_data_api`,
   `MIGRATE_ON_START=false`, and only the migrate account can read the owner
   secret.

The API refuses to start if the database is behind its newest migration, so a
failed migrate step shows up as a failed deploy, not a half-working service.

Then verify:

```bash
owner_psql <<'SQL'
SELECT r.rolname, g.rolname AS member_of FROM pg_auth_members m
  JOIN pg_roles r ON r.oid=m.member JOIN pg_roles g ON g.oid=m.roleid
 WHERE r.rolname IN ('story_data_api','agents_service','recs_app') ORDER BY 1;
-- expect each login in exactly its one group

SELECT u, t, p, has_table_privilege(u, t, p) FROM (VALUES
  ('story_data_api','reading_progress','UPDATE'),   -- t
  ('story_data_api','stories','TRUNCATE'),          -- f
  ('agents_service','chapters','SELECT'),           -- t
  ('agents_service','reading_progress','SELECT'),   -- f
  ('agents_service','stories','UPDATE'),            -- f
  ('recs_app','recommendations.items','UPDATE'),    -- t
  ('recs_app','reading_progress','SELECT')          -- f
) v(u,t,p);
SELECT has_schema_privilege('story_data_api','public','CREATE');  -- f
SQL
gcloud run services describe novelsync-story-data --region=$REGION --project=$PROJECT \
  --format='value(status.url)' | xargs -I{} curl -fsS {}/health
```

If a login was created after the migrate job ran, grant it by hand:
`GRANT story_data_app TO story_data_api;` (and so on for the others).

### B2. taleTribe-agents (right after B1)

Merge agents' `feature/security-hardening`. Terraform moves
`STORY_DATA_DATABASE_URL` to `story-data-database-url-agents` and removes the
agents account's access to the owner secret. Between B1 and B2, MCP and the
assistant get 401 from story-data (phase 2), so do not leave a gap.

Verify: open the Story Assistant on a story and ask about it (exercises
`slim_context` and `search_story`), then edit a chapter and check that the
indexing queue drains:

```bash
owner_psql -c "SELECT count(*) FILTER (WHERE delivered_at IS NULL) AS pending,
  count(*) FILTER (WHERE last_error IS NOT NULL AND delivered_at IS NULL) AS failing
  FROM indexing_outbox WHERE created_at > now() - interval '1 hour'"
```

A `permission denied` in the agents logs means 000048 missed a table agents now
queries; grant it in a new migration rather than by hand.

### B3. Frontend: delete the removed Functions

Merge the frontend's `feature/security-hardening` and deploy Functions. Firebase
does not delete a Function that disappeared from the code, and these five still
serve the ownership bug phase 1b fixed:

```bash
cd repos/taleTribe-frontend
firebase functions:list --project=$PROJECT | grep -iE 'generatenextlines|brainstormideas|enhancetext|summarizechapter|generatestorychoices'
firebase functions:delete generateNextLines brainstormIdeas enhanceText \
  summarizeChapter generateStoryChoices --region=us-central1 --project=$PROJECT
```

Check the exact names `functions:list` prints before deleting.

### B4. taleTribe-recs

Point both recs secrets at `recs_app`:

```bash
for s in recs-postgres-dsn-rw recs-postgres-dsn-ro; do
  swap_user recs_app "$PW_RECS" | gcloud secrets versions add $s --project=$PROJECT --data-file=-
done
unset PW_API PW_AGENTS PW_RECS
```

Jobs read `latest` at each execution. The service needs a new revision to
pick it up:

```bash
gcloud run services update novelsync-recs --region=$REGION --project=$PROJECT \
  --update-labels=db-role=recs-app
gcloud run jobs execute novelsync-recs-ingest --region=$REGION --project=$PROJECT --wait
gcloud run jobs execute novelsync-recs-refresh --region=$REGION --project=$PROJECT --wait
```

Then load recommendations in the app, and check the old role is idle:

```bash
owner_psql -c "SELECT usename, count(*) FROM pg_stat_activity
  WHERE usename IN ('recs_service','recs_app') GROUP BY 1"
```

### B5. Clean up

Do these once B1 to B4 have run cleanly for a day:

1. **Drop `recs_service`** in the Neon console (Roles), since the console made
   it. Then disable the old secret versions:
   `gcloud secrets versions list recs-postgres-dsn-rw --project=$PROJECT` and
   `gcloud secrets versions disable <old> --secret=…` for both recs secrets.
2. **Rotate the owner password.** `neondb_owner` was readable by the public
   agents service until B2. Reset it in the Neon console, then:
   `gcloud secrets versions add story-data-neon-database-url --data-file=-`
   with the new URL, and disable the old version. Only the migrate job uses
   it, and it reads `latest` on each run.
3. **Check who can read the owner secret** (expect only the migrate account):
   `gcloud secrets get-iam-policy story-data-neon-database-url --project=$PROJECT`
4. **Delete the phase 2 leftovers:** the `story-data-service-token` secret, the
   disabled `firebase-adminsdk-fbsvc` key `61db1466…`, and the unused
   `GCP_SA_KEY` secret in the agents GitHub repository:
   ```bash
   gcloud secrets delete story-data-service-token --project=$PROJECT
   gcloud iam service-accounts keys delete 61db1466f938d51d42273d4c0bb9f7b136aa37f9 \
     --iam-account=firebase-adminsdk-fbsvc@$PROJECT.iam.gserviceaccount.com
   gh secret delete GCP_SA_KEY -R Khaled2049/taleTribe-agents
   ```
5. `unset OWNER_URL` and close the terminal.

## Rollback

- **story-data API:** add a version of `story-data-database-url-app` holding the
  owner URL, then deploy a new revision. The API runs as before, minus the
  protection.
- **agents:** the same with `story-data-database-url-agents`.
- **recs:** disable the newest version of each recs secret so `latest` falls
  back to the `recs_service` URL, until B5 drops that role.

Migration 000048 has a down section that revokes its grants; the roles stay,
since they are shared by every database on the server.

## Local development

Nothing changes by default: the local stack connects as `postgres`, and
`MIGRATE_ON_START` defaults to true. To try agents as its production role
locally:

```sql
CREATE ROLE agents_service LOGIN PASSWORD 'agents-dev';
GRANT story_data_agents TO agents_service;
```

and set `STORY_DATA_DATABASE_URL=postgres://agents_service:agents-dev@localhost:5433/story_data`.
