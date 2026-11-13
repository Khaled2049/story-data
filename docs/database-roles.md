# Database roles: production runbook

Each direct database user logs in with a role limited to its job. Migration
`000048_service_roles.sql` creates three permission groups and their grants.
`terraform/database.tf` creates the logins, adds each to its group, and writes
their connection strings to Secret Manager. Passwords and the owner URL are
ephemeral or write-only in Terraform, so neither is saved in state.

| Login | Group | Used by | Can |
|---|---|---|---|
| `neondb_owner` | (owner) | `novelsync-story-data-migrate` job, and Terraform during a deploy | Everything, including DDL |
| `story_data_api` | `story_data_app` | story-data API, `sync-recs` job | Read and write rows in `public` and `recommendations`; no DDL, no TRUNCATE |
| `agents_service` | `story_data_agents` | taleTribe-agents | Read story content; write the search index, indexing queue and usage, voice-learning state |
| `recs_app` | `story_data_recs` | taleTribe-recs | Its `recommendations` schema; read `stories`, `story_tags`, `chapters`, `chapter_summaries` |

| Secret | Holds | Written by | Read by |
|---|---|---|---|
| `story-data-neon-database-url` | owner URL | you, in the Neon console | migrate job, deploy Terraform |
| `story-data-database-url-app` | `story_data_api` URL | story-data Terraform | `novelsync-story-data-run` |
| `story-data-database-url-agents` | `agents_service` URL | story-data Terraform | `novelsync-agents-run` |
| `recs-postgres-dsn-rw`, `recs-postgres-dsn-ro` | `recs_app` URL | story-data Terraform (versions only) | the recs runtime |

**Never create these logins in the Neon console or with Neon's API or
Terraform provider.** Neon adds every role it creates that way to
`neon_superuser`, which carries `pg_read_all_data` and `pg_write_all_data` and
overrides every grant above. That is how the old `recs_service` came to read
every table. The PostgreSQL provider in `database.tf` creates them with SQL.
As a backstop, the production API refuses to start if its own login is in
`neon_superuser`, can create roles, bypasses row-level security, or can create
objects in `public` (`store.CheckRuntimeRole`).

## One-time setup

Create the migrate service account. CI cannot create service accounts, and
should not be given that right:

```bash
gcloud iam service-accounts create novelsync-story-data-migrate \
  --project=story-6f89f --display-name="story-data migrations"
```

That is the only manual step before deploying.

## Deploy

Merge story-data's `feature/security-hardening`. The deploy workflow:

1. **Migrate.** Applies only the migrate job (with the new image) and runs it
   as `neondb_owner`. That applies `000048`, creating the three groups.
2. **Apply.** The full apply connects to the database as the owner and:
   - creates `story_data_api`, `agents_service` and `recs_app` with generated
     passwords, and adds each to its group;
   - writes `story-data-database-url-app` and `-agents`, and new versions of
     both recs secrets;
   - moves the API and `sync-recs` to `story_data_api` with
     `MIGRATE_ON_START=false`;
   - leaves the owner secret readable only by the migrate account (and CI,
     which has project-wide secret access).
3. **Health check.** The API refuses to start if the schema is behind or its
   login is over-privileged, so either problem fails the deploy.

Then, **right away**, merge taleTribe-agents' `feature/security-hardening`. Its
Terraform reads `story-data-database-url-agents`, which step 2 created, and
drops its access to the owner secret. Between the two deploys MCP and the
assistant get 401 from story-data (phase 2), so do not leave a gap.

Finally, give recs a new revision so the service picks up its new secret
versions (its jobs read `latest` on each run):

```bash
gcloud run services update novelsync-recs --region=us-central1 --project=story-6f89f \
  --update-labels=db-role=recs-app
```

## Verify

Run read-only checks as the owner. This helper keeps the URL off the command
line:

```bash
PROJECT=story-6f89f
OWNER_URL="$(gcloud secrets versions access latest --secret=story-data-neon-database-url --project=$PROJECT)"
owner_psql() { docker run --rm -i -e URL="$OWNER_URL" postgres:17-alpine sh -c 'psql "$URL" -X -v ON_ERROR_STOP=1 "$@"' -- "$@"; }

owner_psql <<'SQL'
-- each login in exactly its one group, and none in neon_superuser
SELECT r.rolname, g.rolname AS member_of FROM pg_auth_members m
  JOIN pg_roles r ON r.oid=m.member JOIN pg_roles g ON g.oid=m.roleid
 WHERE r.rolname IN ('story_data_api','agents_service','recs_app') ORDER BY 1;

SELECT u, t, p, has_table_privilege(u, t, p) FROM (VALUES
  ('story_data_api','reading_progress','UPDATE'),   -- t
  ('story_data_api','stories','TRUNCATE'),          -- f
  ('agents_service','chapters','SELECT'),           -- t
  ('agents_service','reading_progress','SELECT'),   -- f
  ('agents_service','stories','UPDATE'),            -- f
  ('recs_app','recommendations.items','UPDATE'),    -- t
  ('recs_app','reading_progress','SELECT')          -- f
) v(u,t,p);

-- who is connected: expect the new logins, and no recs_service
SELECT usename, count(*) FROM pg_stat_activity
 WHERE usename IS NOT NULL GROUP BY 1 ORDER BY 1;
SQL
unset OWNER_URL
```

Then use the product: open the Story Assistant on a story and ask about it,
edit a chapter and check the indexing queue drains, and load recommendations.

```bash
owner_psql -c "SELECT count(*) FILTER (WHERE delivered_at IS NULL) AS pending,
  count(*) FILTER (WHERE last_error IS NOT NULL AND delivered_at IS NULL) AS failing
  FROM indexing_outbox WHERE created_at > now() - interval '1 hour'"
```

A `permission denied` in the agents or recs logs means `000048` missed a table
that service now queries. Grant it in a new migration, not by hand.

## Clean up

Once everything has run cleanly for a day:

1. **Drop `recs_service`** in the Neon console (Roles); the console made it.
2. **Rotate the owner password.** `neondb_owner` was readable by the public
   agents service until this change. Reset it in the Neon console, add the new
   URL as a version of `story-data-neon-database-url`, and disable the old
   version. The migrate job and the next deploy read `latest`.
3. **Delete the leftovers** from phase 2 and the IAM review:
   ```bash
   gcloud secrets delete story-data-service-token --project=story-6f89f
   gcloud iam service-accounts keys delete 61db1466f938d51d42273d4c0bb9f7b136aa37f9 \
     --iam-account=firebase-adminsdk-fbsvc@story-6f89f.iam.gserviceaccount.com
   gh secret delete GCP_SA_KEY -R Khaled2049/taleTribe-agents
   ```
4. Disable the old versions of `recs-postgres-dsn-rw` and `-ro`, which hold the
   `recs_service` URL (`gcloud secrets versions list` / `versions disable`).

## Rotating the login passwords

Bump `db_password_version` in `terraform/variables.tf` and deploy. Each login
and the secrets holding its URL change in the same apply; services pick up
the new value on their next revision or job run. Old secret versions are
destroyed with the resource they belonged to.

## Rollback

- **story-data API:** add a version of `story-data-database-url-app` holding the
  owner URL and set `MIGRATE_ON_START` back to true. The API runs as before,
  minus the protection. (The role check only runs when `MIGRATE_ON_START` is
  false.)
- **agents:** point its Terraform back at `story-data-neon-database-url`.
- **recs:** disable the newest version of each recs secret so `latest` falls
  back to the `recs_service` URL, until cleanup drops that role.

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
