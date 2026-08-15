# Repository Guidelines — story-data

`story-data` is the PostgreSQL-backed system of record for migrated NovelSync
data. It exposes the API consumed by the frontend and agents; it is not a
Firebase replacement for identity or legacy-only features.

## Commands

- `docker compose up --build`: run local PostgreSQL (with pgvector) and API.
- `go test ./...`: run the Go test suite.
- `gofmt -w internal cmd`: format changed Go code.
- `go run ./cmd/api migrate`: apply migrations manually; normal API startup
  also migrates under an advisory lock.

## Data and API rules

- Add schema changes as ordered SQL migrations in `migrations/`; never edit an
  already-applied migration.
- Keep handlers thin. Put SQL, transactions, and invariants in `internal/store`.
- Production authentication uses Firebase ID tokens. `AUTH_MODE=dev` is local
  only and accepts `X-User-ID`.
- Keep request/response JSON backward compatible with the frontend client.
- Story and chapter mutations use optimistic concurrency: preserve and enforce
  `If-Match` / `revision`, returning `409` for stale writes.
- Do not expose database credentials or permit browser-to-Postgres access.
- Preserve indexing-outbox writes for content that must be embedded by
  taleTribe-agents.

## Verification

Run `gofmt` and `go test ./...`, then check `GET /health`. For changed
authenticated endpoints, test with `AUTH_MODE=dev` and an `X-User-ID` header.
