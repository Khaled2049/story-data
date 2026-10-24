# story-data service guide

`story-data` is TheTaleTribe's PostgreSQL-backed HTTP service for product data
that benefits from relational integrity, transactions, public reads, and vector
search. It is the system of record for the domains migrated from Firestore.

Firebase is still part of the product: Firebase Authentication establishes user
identity, while Firestore remains in use for legacy-only features, MCP OAuth
state, Brain memory, and encrypted BYOK settings. The browser never connects
to PostgreSQL or Neon directly.

## Request flow

```text
React client
  │ Firebase ID token (or local emulator identity)
  ▼
story-data HTTP API ──► auth verifier ──► handlers ──► store / transactions
                                                     │
                                                     ▼
                                                PostgreSQL / pgvector
                                                     │
                                      indexing_outbox│
                                                     ▼
                                           taleTribe-agents index worker
                                                     │
                                                     ▼
                                           story_vector_chunks embeddings
```

In local development, Vite proxies `/story-data` to `http://localhost:8084`.
The frontend sends its Firebase token. In `AUTH_MODE=dev`, the service also
accepts `X-User-ID`, which makes direct local API checks convenient.

## Ownership and boundaries

| Component | Owns |
| --- | --- |
| `story-data` | Migrated stories, chapters, worldbuilding, public reads, social data, profiles, reading history, guestbooks, book clubs, competitions, token ledger, and the indexing outbox. |
| Firebase Auth | Identity and ID tokens. |
| Firestore | Unmigrated/legacy data and Firebase-specific features. |
| `taleTribe-agents` | AI workflows and the worker that creates pgvector embeddings from the outbox. |
| `creditProxy` | LLM request metering, credit reservation, and provider routing. |

Do not add browser-to-database access or new Firestore writes for a domain
already owned by this service.

## Code layout

```text
cmd/api/                 process startup, connection retry, and migrations
internal/config/         environment-variable configuration
internal/auth/           Firebase token and local development identity checks
internal/httpapi/        route dispatch, request decoding, and HTTP responses
internal/httpapi/e2e/    black-box suite: real router, real store, real SQL
internal/store/          SQL queries, transactions, authorization checks, DTOs
migrations/              ordered PostgreSQL schema migrations
openapi/openapi.yaml     API contract for consumers
terraform/               production infrastructure configuration
```

Keep HTTP handlers thin. Authorization, SQL, transaction boundaries, and data
invariants belong in `internal/store`.

Tests for the HTTP surface live in `internal/httpapi/e2e`, a separate package
that reaches the service only the way a client does — over HTTP, through the
exported constructor. Put a new endpoint's tests there. A test that needs an
unexported symbol goes beside the code it covers instead
(`internal/httpapi/errors_test.go` is the current example). The suite needs a
PostgreSQL server; it creates its own throwaway database and never touches the
dev one.

## Data model and migrations

Migrations are ordered and immutable. The API runs outstanding migrations on
startup under PostgreSQL advisory lock `82104231`; this makes concurrent
deployments safe. To run only migrations:

```sh
go run ./cmd/api migrate
```

Migration groups correspond to product domains:

| Migration | Domain |
| --- | --- |
| `000001` | Story workspace and chapters |
| `000002` | Characters, places, plots, events, and relationships |
| `000003` | AI context, indexing outbox, and pgvector chunks |
| `000004`–`000006` | Public reads, social interactions, and profiles |
| `000007` | Reading progress/history |
| `000008`–`000010` | Guestbooks, book clubs, and competitions/token ledger |
| `000011`–`000018` | Competition repair, quotas, validation ceilings, profile names, and public search |
| `000019`–`000021` | Recommendation schema, restricted service role, and catalog-read grant |
| `000022`–`000023` | Per-user and platform-wide recommendation LLM budgets |
| `000024`–`000026` | Assistant threads, optional submission story, and story comments |
| `000027` | Guestbook author-feed and reply-parent indexes |
| `000028` | Tag URL-form index for public tag listings |
| `000029` | Versioned writer agreement records |
| `000030` | Per-story paragraph style |
| `000031`–`000032` | Story completion status and reader update; per-chapter publication and `public_updated_at` |

When adding a feature, create a new migration rather than editing an existing
file. Schema writes that need fresh AI context must enqueue an
`indexing_outbox` event in the same logical write path.

## Authentication and authorization

Production configuration uses:

```text
AUTH_MODE=production
FIREBASE_PROJECT_ID=<Firebase project id>
DATABASE_URL=<managed PostgreSQL/Neon connection string>
```

The client uses `Authorization: Bearer <Firebase ID token>`. The auth verifier
extracts the Firebase UID; store methods use it to enforce ownership and other
permissions. Never trust client-supplied owner IDs.

`AUTH_MODE=dev` is for local development only. It accepts `X-User-ID` and
`X-Admin: true` so scripts can exercise protected routes without external
token verification.

### Story moderation

`/v1/admin/*` requires a verified Firebase `admin: true` claim on every
request (or `X-Admin: true` with a user ID in local dev auth). These routes
verify the ID token with the revocation check, so `npm run unset-admin` (which
also revokes the user's sessions) cuts access off immediately rather than when
the token expires; it costs one Firebase Auth lookup per admin request. It lists stories
from every author, including drafts, with cursor pagination, `q` search and
`status=draft|ongoing|completed` filtering. The detail and chapter routes below
it let an admin review private content; ordinary story and public reader routes
keep their existing visibility rules. Moderation reads return `Cache-Control:
no-store`. Service-token callers cannot claim admin access.

`DELETE /v1/admin/stories/{id}` requires the current story `If-Match` revision
and reuses owner deletion, including the active competition entry restriction
and cascading cleanup.

Admin access to other users' content is recorded in `admin_audit` (actor uid,
action, target ID, JSON detail, time): `story.view`, `chapter.view`,
`competition.view`, `story.delete` and `competition.delete`. A delete writes its
row in the same transaction, so a refused delete leaves none; a view returns no
content if its row cannot be written. Listings are not recorded. The frontend provides `/admin/stories`
and `/admin/stories/{id}` (`/admin` redirects to the story list), with an Admin
link shown only to admins and explicit
confirmation before permanent deletion. This MVP provides story review and
deletion for investigating reports; it does not create a report inbox.

### Member directory for admins

`GET /v1/admin/members` lists every member with a public profile, newest
first, with `q` (username or name substring, or exact user ID), `limit` and
`cursor`. Each row carries `storyCount`, which includes drafts, and
`publishedStoryCount`. It reads `public_profiles` only: emails and the admin
claim live in Firebase Auth and are not returned. The frontend page is
`/admin/members`.

`GET /v1/admin/members/{userId}/usage?from=&to=` returns that member's daily
AI counters kept here, counts only: `indexingPasses` (`indexing_usage`) and
`recsSearches`/`recsExplains` (`recommendations.llm_usage`). Inclusive UTC
days, the last 30 by default, at most 90 (`422` otherwise), every day present.
The admin AI usage page combines it with creditProxy's ledger.

### Competition read visibility

The dedicated moderation routes `/v1/admin/competitions` and
`/v1/admin/competitions/{id}` allow verified admins to list and review all
competitions, including drafts. These routes return `Cache-Control: no-store`
and preserve the embargo on unsettled vote tallies. The list accepts `q`,
`phase`, `limit` and `cursor`, returning `{competitions,nextCursor}`. Admins may
delete an unpublished draft through `DELETE /v1/admin/competitions/{id}`;
published competitions return `409` and must use the existing cancellation and
refund flow. `/admin/competitions` is the matching protected frontend table.
These explicit moderation exceptions do not change ordinary reader visibility:

A published competition is readable by anyone, including anonymous callers —
that is the public contest page. A **draft** is its author's private working
copy and answers only to them; every other caller, admin included, gets `404`
rather than `403`, because confirming that a draft id exists is itself the
disclosure. `GET /v1/competitions/{id}/submissions` **requires
authentication** and is gated on the same rule: the list carries the Firebase
uid of every entrant, which joins directly against the public profile
directory. The guard lives in `GetCompetition`/`ListSubmissions`, not in the
internal `competition()` helper, which every store method uses with the acting
user and must keep working for an admin operating on someone else's record.

### Competition voter eligibility

Casting a competition ballot needs more than a valid token, because a ballot
decides a winner-take-all prize and Firebase sign-up is free and unverified. A
voter must have **joined** the competition — registration closes when entries
do — and must hold a **public profile** at least `VOTER_MIN_PROFILE_AGE` old
(default 24h, a Go duration such as `48h`). Set it to `0` in local stacks so a
freshly seeded account can vote; a malformed value fails startup rather than
silently reverting to the default. Ballot size is capped per competition by
`competitions.max_votes_per_user`.

The **creator** may not join their own competition (`403`), and so can neither
enter nor vote in it. They keep the host share of entry fees and get the prize
back when nobody wins, so they must have no say in the result.

### Competition voting deadline

`voting_deadline_at` is enforced, judged on the database clock. A competition
stays in `voting` until it is settled, so the phase alone does not say whether
voting is open:

- `CastBallot` refuses (`422`) once `now() >= voting_deadline_at`, including a
  recast of an existing ballot.
- `SettleCompetition` refuses (`422`) while `now() < voting_deadline_at`, for
  the creator and admins alike. Settling early at zero ballots would return the
  prize and the host's fee share to the creator; `CancelCompetition`, which
  refunds every fee, is the way to end a competition early.

Settlement re-reads the competition under a row lock inside its transaction,
so it is idempotent under a race as well as on retry: a settle that waited on
one which committed returns the settled competition and writes nothing.

Nothing settles a competition automatically yet; the creator or an admin calls
`POST /v1/competitions/{id}/settle` after the deadline.

## Errors, logging and lifecycle

**Error mapping** lives in `respond` (`internal/httpapi/server.go`). The store's
sentinel errors map to their status codes; anything else falls through
`translatePgError`, which turns the constraint violations a bad request can
provoke into the 4xx they are — check/not-null/invalid-text/truncation/numeric
range to `422`, foreign key to `404`, unclaimed unique violation to `409`.
Validators in `internal/store` should catch each of these first; this is the
backstop for the one that gets missed, and it matters because an
attacker-triggerable 500 poisons the error rate operators alert on. Client
bodies stay generic — the detail goes to the log.

**Request logging** (`internal/httpapi/logging.go`) is the outermost
middleware, so throttled and CORS-rejected requests are logged too. One JSON
line per request: request id, method, path, status, duration, uid, client
address, and the underlying error when the response was a 500. `/health` is
skipped. Every response carries `X-Request-ID`; an inbound `X-Request-ID` or
`X-Cloud-Trace-Context` is preserved so a trace survives across services.
`cmd/api` installs `slog`'s JSON handler on stderr, which is what Cloud Run
parses into structured entries.

**Server lifecycle** (`cmd/api/main.go`). `newServer` sets
`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout` and
`MaxHeaderBytes` — a bare `http.ListenAndServe` leaves all of them at zero,
which lets one slow client hold a goroutine indefinitely. `serve` drains on
`SIGTERM` with a 20s grace period, so an in-flight ledger transfer commits
before an instance goes away. Both are covered by tests in `cmd/api`, since
`go vet` does not catch either.

## Input validation

`internal/store/validate.go` holds the ceilings and the URL rule; every write
path applies them and returns `422` (`ErrValidation`) rather than letting the
database decide or storing the value.

- **URL-shaped fields** — story `coverImageUrl`/`thumbnailUrl`, profile
  `photoUrl`, book-club `image`, character `artUrl`, place `imageUrl` — accept
  only absolute `http`/`https` URLs, by allowlist. That rejects `javascript:`,
  `data:`, `vbscript:` and their obfuscations in one predicate instead of
  enumerating them. Empty is still "not set".
- **Text ceilings**: title 500, description 5 000, names 200, short fields
  (category, language, copyright, audience) 100, long-form worldbuilding prose
  20 000, tags 20 per record at 100 each, JSON blobs 16 KB. Counted in
  **runes**, matching the SQL `char_length`, so a limit means the same thing in
  every script.
- **Per-story entity ceilings**: 200 characters, places, plot lines, and events
  per plot line, alongside the existing 50 chapters and 100 stories per user.
- These are the outer wall, deliberately looser than the editor and the MCP
  write tools (`taleTribe-agents/mcp_server/writes.py`), which carry the
  product contract. Nothing a client legitimately produced is rejected.

Migration `000016_field_ceilings.sql` mirrors the same rules as SQL `CHECK`
constraints, added `NOT VALID` — enforced on write, but no table scan at
deploy, because migrations run at startup and a scan that failed on one legacy
row would take the service down instead of protecting it.

## Abuse controls

The service is invokable by anyone — Cloud Run grants `roles/run.invoker` to
`allUsers` and `/v1/public/*` needs no credential — so throttling is part of
the contract, at two layers.

**Per-caller request budget** (`internal/httpapi/ratelimit.go`). Middleware
keyed on the authenticated caller where there is one and the client address
otherwise, defaulting to 300 reads and 60 writes per minute. Override with
`RATE_LIMIT_READS_PER_MINUTE` / `RATE_LIMIT_WRITES_PER_MINUTE`; `0` disables
that half, which is what the local stack does. Over budget is `429` with
`Retry-After`. `GET /health` is exempt so a flood cannot fail the liveness
probe. Buckets are per instance and in memory: this bounds a burst, it is not
a platform-wide quota. `clientIP` reads the **last** `X-Forwarded-For` entry,
the one Google's front end appends — putting a load balancer in front of the
service invalidates that assumption.

**Durable daily budgets** (`internal/store/quota.go`, table
`user_daily_usage`). Per user, per action, in UTC days, spent in the same
transaction as the write they meter: comments (100), ratings (50), competition
drafts (10) and book clubs (5). The guestbook and book-club discussion domains
predate this and keep their own tables. Over budget is `429`.

Two related bounds: `POST /v1/public/stories/{id}/views` counts one reader per
story per day (`public_story_view_hits`, keyed on uid or a salted hash of the
address — never a raw IP), and `GET /v1/competitions` is paged with a constant
query count rather than selecting the whole table and hydrating row by row.

Not covered here: there is no edge rate limiting. Attaching Cloud Armor needs
an external load balancer in front of Cloud Run, which the service does not
have today.

## API conventions

- `GET /health` is the service health check.
- Full route definitions and request schemas are in
  [`openapi/openapi.yaml`](../openapi/openapi.yaml).
- Collection endpoints return JSON arrays, including `[]` when no records
  exist; they should never return `null` for an empty collection.
- Public listings are paged. `GET /v1/public/stories` carries its cursor in the
  body (`nextCursor`). `GET /v1/competitions` keeps a bare array — clients map
  over it directly — and returns its continuation token in the `X-Next-Cursor`
  response header. Both accept `?limit=` and `?cursor=`.
- `GET /v1/public/stories` also filters by `?tag=` (the tag's URL form:
  lower-cased, spaces as hyphens — `store.TagSlug`, served by
  `story_tags_slug_idx`) and `?author=<uid>`. `GET /v1/public/sitemap` walks
  the same published set with the same cursor but returns only `id`,
  `authorId`, `title` and `updatedAt`, up to 1000 a page; the frontend's
  `seoRender` Function builds `sitemap.xml` from it.
- `GET /v1/public/stories` filters by `?status=ongoing|completed` too. A
  story's completion status is separate from `is_published`: a serial is public
  long before it is finished. The author sets it, with an optional
  `nextChapterOn` date and a `readerNote` of up to 280 characters, through
  `PUT /v1/stories/{id}/reader-update` (`internal/store/reader_update.go`).
  That endpoint is the one story write that takes no `If-Match` and bumps
  neither `revision` nor `updated_at`, so a note edit cannot conflict with a
  story edit the author has open. A story `PATCH` does not carry these fields
  and cannot reset them.
  Public responses omit `nextChapterOn` once the date is more than a day past
  (the day of grace covers authors west of UTC); the owner still sees it.
- **A chapter is a draft until its author releases it.** `chapters.published_at`
  is null for a draft. New chapters start as drafts;
  `PUT /v1/stories/{id}/chapters/{chapterId}/publication` with
  `{"published": true|false}` and the chapter's `If-Match` revision releases one
  or pulls it back (`internal/store/chapter_publication.go`). It does not bump
  the chapter revision, since the text is unchanged, and it refuses to release
  an empty chapter. A release also clears the story's `nextChapterOn`.
  Publishing a story for the first time releases every chapter that has text,
  but only if the author has released none themselves.
- **Every non-owner chapter read must exclude drafts.** That is the public
  manifest, the public chapter read and `chapterCount`, and also the
  authenticated `ListChapters`, `ListChapterIndex`, `GetChapter` and
  `StoryContext`, which serve a published story to any caller (`draftFilter` in
  `store.go`). Reading progress can only be saved on a released chapter, and
  history and the recommendations completion rule count released chapters
  only. Any new query that returns chapter rows to someone other than the owner
  needs the same condition; `TestDraftChaptersAreHiddenFromEveryoneButTheAuthor`
  is the guard.
- **Discovery orders by `stories.public_updated_at`, not `updated_at`.**
  `updated_at` moves on every draft save and orders the owner's shelf.
  `public_updated_at` moves only on a change readers can see: a story `PATCH`,
  a chapter release or pull-back, an edit to or deletion of a released chapter,
  and a change of completion status (`touchStory` in `store.go`). The public
  listing, the sitemap, their shared cursor and the `updatedAt` on public
  responses all use it.
- Story, chapter, and worldbuilding mutations use optimistic concurrency.
  Clients send `If-Match: <revision>` for writes that update/delete an existing
  record. A stale revision returns `409 Conflict`.
- Public endpoints expose only published/safe data. Mutating and private reads
  require authentication.
- Handlers return consistent JSON errors; do not leak database or internal
  implementation details.

### Guestbook feed and reply pages

`GET /v1/me/wall` (`ListPersonalWall`) builds its page from up to three
branches — the viewer's wall, the viewer's authorship, and one bounded probe
per followed author — each read newest-first through an index and limited
before they are merged. Do not collapse them back into one scan filtered by
`owner OR author OR author IN (follows)`: without a time index that reads every
entry, and with one it walks past every entry that is not the viewer's, which
for an account with a quiet feed is the whole table. Cost scales with the
viewer's follow count, not with the number of entries.

Each feed entry carries `viewerCanPost`: whether the viewer may reply on the
wall that entry sits on. The wall policy exists once, as the SQL expression
`guestbookCanPostSQL`; `canPostGuestbook` (the write gate) and this flag both
evaluate it. Change the policy there, never in one caller.

`GET /v1/public/guestbooks/{owner}/entries/{id}/replies` returns the whole
thread as a bare array, which is what already-deployed clients read. With
`?limit=` (1–50) it returns `{ replies, nextCursor, totalCount }` instead,
paged by **top-level** reply and carrying every descendant of the replies on
the page, so a reply never arrives without its parent. `limit` bounds the
top-level replies, not the rows.

## AI context and pgvector

When a chapter, character, place, or plot event changes, story-data records an
outbox event. `taleTribe-agents`, started with both
`STORY_DATA_DATABASE_URL` and `INDEXING_WORKER_ENABLED=true`, claims events,
loads canonical story context, chunks text, generates 768-dimension embeddings,
and replaces the corresponding rows in `story_vector_chunks`.

This is intentionally asynchronous: the author-facing write succeeds without
waiting for an embedding provider. Failed events retain an error and are made
available for retry. JSONB metadata must be JSON serializable; PostgreSQL
numeric values can arrive in Python as `Decimal` values and must be converted
by the worker before insertion.

## The recommendations schema

`taleTribe-recs` is a separate service, but its schema is not a separate
database: `recommendations.*` lives here and is created by
`migrations/000019_recommendations_schema.sql`. recs does not migrate anything,
so schema changes for it are ordinary story-data migrations.

The boundary that matters is which service reads what. recs pulls its catalog
from `stories` — published stories are public data that `ListPublicStories`
already serves to any caller — but it must **never** read `reading_progress`,
which is strictly private per-user history. So story-data derives the reader
signals instead:

```bash
story-data sync-recs      # or: go run ./cmd/api sync-recs
```

`internal/store/recommendations.go` rebuilds `recommendations.interactions`
from `story_likes`, `story_ratings` and `reading_progress` in one transaction,
and copies `stories.views` into `item_stats.views`. It is a full re-derivation
each run rather than incremental, because these are current *state* rather than
an event log — un-liking a story deletes the row, and an incremental pass has
nothing to observe. Rows for `synth_`-prefixed users are left alone; those are
generated readers recs writes itself.

Two details are implemented here **and** in recs, because recs cannot compute
them without reading the private table: the engagement weight per signal kind,
and the completion rule (last released chapter, scrolled past 0.9).
`TestRecommendationSignalWeights` and
`TestCompletionNeedsLastChapterAndDeepScroll` pin both to recs's Python values,
so drift fails a test rather than silently re-ranking the catalog.

Everything downstream of `interactions` belongs to recs and never leaves the
schema. Migration `000020` grants a `recs_service` role usage on
`recommendations` only — and on `public` solely so it can resolve the `vector`
type, with no table grant. The grant is a no-op where that role does not exist,
which is every local and test database.

Catalog ingest also needs read-only access to `stories`, `story_tags`, `chapters`
and `chapter_summaries`; migration `000021` grants exactly those four tables.
This is broader than row-level public visibility—the role can technically read
draft rows—but is the deliberately small deployment fix, and the ingest query
still selects only published stories. It does not grant access to
`story_likes`, `story_ratings` or `reading_progress`.

The role grants are guarded because the production role is absent in ordinary
local databases. Create `recs_service` **before** applying migrations `000020`
through `000023`. Goose records a guarded migration as applied even when the
role is absent, so creating the role afterward does not apply the grants
retroactively. CI creates a `NOLOGIN` role in its ephemeral PostgreSQL cluster
and asserts both the required grants and the prohibited private-table reads.

### `recommendations.llm_usage` — the daily spend ceiling

Migration `000022` adds `recommendations.llm_usage (user_id, day, kind,
call_count)`, the same shape as `indexing_usage` (`000013`) and for the same
reason. recs calls Gemini directly, so **creditProxy is not in that path**:
neither `PLATFORM_DAILY_REQUEST_LIMIT` nor the per-user `MAX_AI_USAGE` bounds a
HyDE search or an explanation. Its in-process token bucket bounds bursts but not
spend — the real ceiling is `instances x limit`, and it resets on every deploy.

recs owns the reads and writes (`recommendation_engine/usage.py`); story-data
owns the table only because it owns every migration in this database. The two
`kind` values are `search` and `explain`, budgeted separately so a reader
clicking "Why this story?" cannot consume their search allowance. Defaults are
10 searches and 30 explanations per user per UTC day.

Migration `000023` adds `recommendations.llm_platform_usage (day, kind,
call_count)` — the same counter with no user in the key. Per-user budgets
multiply by the user count, so alone they bound nothing in total; this is the
ceiling `PLATFORM_DAILY_REQUEST_LIMIT` would give these routes if creditProxy
were in them. Defaults are 1000 searches and 1000 explanations per UTC day.

Both counters move in **one statement**, and its ordering is load bearing: the
platform insert selects `FROM charged_user`, so it increments nothing when the
user's own guard already refused. The other order would let a single user who
had spent their own allowance keep driving the platform counter with requests
that are refused anyway — denying the feature to everyone else.

Note this is the one place recs writes on the **primary** rather than its
read-only compute: a counter has to be durable and shared across instances to
mean anything. The write is one upsert per metered request, which the ceilings
themselves bound. The platform row is a hot row by construction — one per kind
per day — and deliberately so, since the contention is bounded by the ceiling
that row enforces.

### Documentation

recs is documented in `repos/taleTribe-recs/recommendation_engine/docs/`, indexed
by `README.md` there. The two most relevant from this side:

- **`jobs.md`** — how `sync-recs` fits with recs's two jobs, why the order is
  fixed (`sync-recs` joins `recommendations.items`, so a like on a story that has
  not been ingested yet produces no row), and the `item_stats.views` column split
  that lets both services write that table safely.
- **`security-and-roles.md`** — the full privacy argument behind this section,
  the threat table, and the pseudonymisation decision that must be made before
  the first real sync.

## Local development

The easiest integrated startup is from the workspace root:

```sh
./dev-new.sh
```

For story-data alone:

```sh
docker compose up --build
```

This starts PostgreSQL with pgvector at `localhost:5433` and the API at
`localhost:8084`.

| Setting | Value |
| --- | --- |
| Host | `localhost` |
| PostgreSQL port | `5433` |
| Database | `story_data` |
| Username | `postgres` |
| Password | `postgres` |
| API health URL | `http://localhost:8084/health` |

To run the API outside Docker, point `DATABASE_URL` at a local PostgreSQL
database and use `AUTH_MODE=dev`:

```sh
DATABASE_URL='postgres://postgres:postgres@localhost:5433/story_data?sslmode=disable' \
AUTH_MODE=dev \
go run ./cmd/api
```

## Development checklist

`make` lists the common commands; every one is a thin wrapper around the tool
it names, so nothing here is available only through make.

Before handing off a change:

1. Run `make fmt` (`gofmt -w internal cmd`) for Go changes.
2. Run `make test` (`go test ./...`), which needs PostgreSQL — `make db`
   starts one. `make check` does steps 1 and 2 plus `go vet` in one pass.
3. Start the service and check `GET /health`.
4. Exercise changed authenticated endpoints with a Firebase token or the local
   `X-User-ID` development header.
5. If a domain feeds AI context, verify an outbox event can be indexed by
   taleTribe-agents.

## Production notes

Production uses Neon/PostgreSQL and Firebase token verification. Configure
secrets through the deployment environment, not source control. Terraform
configuration lives in `terraform/`; inspect it before changing deployment
resources. Apply migrations through the normal application startup/deployment
path so the advisory lock protects concurrent instances.

Terraform also deploys `novelsync-story-data-sync-recs`, a single-task Cloud Run
Job using the API image and the owner database credential. The recommendations
stack invokes it between its catalog-ingest and aggregate-refresh jobs; this
repository deliberately owns no independent schedule because three separate
cron triggers would not enforce that ordering. Operators can run it directly:

```sh
gcloud run jobs execute novelsync-story-data-sync-recs \
  --project=story-6f89f --region=us-central1 --wait
```

The job performs a transactional full derivation and is safe to retry. Do not
run overlapping executions: they do redundant full scans and contend while
replacing the same interaction rows.

## Writer agreement

`GET /v1/me/writer-agreement` returns the current terms/privacy/attestation
versions, the attestation text, and the authenticated user's acceptance status.
`POST` requires exact current versions and explicit true values for `agreeTerms`,
`acknowledgePrivacy`, `attestRights`, and `adult`. The server records the Firebase
UID and its own timestamp; retries preserve the first timestamp. Records are
independent of story deletion. Do not auto-accept for real users or let MCP/AI
clients attest on behalf of users; direct them to the frontend agreement screen.

`CreateStory` requires acceptance in the store, including for imports and service
clients. The HTTP story workspace also requires it for POST/PUT/PATCH writes to
stories, chapters, worldbuilding, and assistant threads. Reads, reader social
interactions, and DELETE operations remain available. A new version leaves old
records intact but requires renewed acceptance for writing. Changes to policy
text must bump the matching constants in `internal/store/writer_agreement.go`
and the published frontend version, and preserve the old documents and
attestation text in the frontend's `docs/legal` archive.

Deploy the API/migration and frontend together. Older clients that create stories
without first recording agreement will receive 403. Local seeding records
agreement only for synthetic fixture accounts through the dev-auth API.

## Standalone iOS reader CORS

The bundled Tauri iOS reader uses `tauri://localhost`; it is an explicit allowed
origin in Terraform defaults. Preserve the existing website origins when
overriding `CORS_ORIGINS`. Never grant the opaque `null` origin or use a wildcard.
Requests still require the normal Firebase bearer token for private data.

Docker Compose also lists the standalone mobile dev server on 5174. For an
isolated local bundled check without restarting the shared stack, start a
separate API process on an unused port with `AUTH_MODE=production`, the project
ID, `FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9099`, and
`CORS_ORIGINS=tauri://localhost`. This exercises token verification rather than
the development user header; keep the emulator setting local only.

These configuration changes do not update a running deployment or container.
Apply the verified allowlists during the coordinated mobile cutover.
