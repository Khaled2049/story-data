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

## API conventions

- `GET /health` is the service health check.
- Full route definitions and request schemas are in
  [`openapi/openapi.yaml`](../openapi/openapi.yaml).
- Collection endpoints return JSON arrays, including `[]` when no records
  exist; they should never return `null` for an empty collection.
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
