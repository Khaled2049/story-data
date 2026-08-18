# story-data service guide

`story-data` is NovelSync's PostgreSQL-backed HTTP service for product data
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
internal/store/          SQL queries, transactions, authorization checks, DTOs
migrations/              ordered PostgreSQL schema migrations
openapi/openapi.yaml     API contract for consumers
terraform/               production infrastructure configuration
```

Keep HTTP handlers thin. Authorization, SQL, transaction boundaries, and data
invariants belong in `internal/store`.

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

### Competition read visibility

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
- Story, chapter, and worldbuilding mutations use optimistic concurrency.
  Clients send `If-Match: <revision>` for writes that update/delete an existing
  record. A stale revision returns `409 Conflict`.
- Public endpoints expose only published/safe data. Mutating and private reads
  require authentication.
- Handlers return consistent JSON errors; do not leak database or internal
  implementation details.

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

Before handing off a change:

1. Run `gofmt -w internal cmd` for Go changes.
2. Run `go test ./...`.
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
