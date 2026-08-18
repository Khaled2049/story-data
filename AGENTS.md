# Repository Guidelines — story-data

`story-data` is the PostgreSQL-backed system of record for migrated NovelSync
data. It exposes the API consumed by the frontend and agents; it is not a
Firebase replacement for identity or legacy-only features.

## Commands

`make` lists every target; each is a thin wrapper around the underlying tool,
so either form works.

- `make up` (`docker compose up --build -d`): local PostgreSQL (with pgvector)
  and API.
- `make db` (`docker compose up -d postgres`): just the database, which is all
  the tests need.
- `make test` (`go test ./... -count=1`): the Go test suite. `make test-unit`
  skips the packages that need a database; `make test RUN=<regex>` narrows.
- `make fmt` (`gofmt -w internal cmd`): format changed Go code.
- `make check`: format check, vet and the full suite — what CI rejects a
  change for.
- `make migrate` (`go run ./cmd/api migrate`): apply migrations manually;
  normal API startup also migrates under an advisory lock.

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
