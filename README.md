# story-data

The PostgreSQL-backed system of record for NovelSync's relational product data.
Firebase Authentication remains the identity provider; clients authenticate to
this API and never receive a Neon connection string. Firestore remains in use
for legacy jobs, Brain memory, MCP OAuth state, and encrypted BYOK settings.
PostgreSQL stories use pgvector embeddings through the agents outbox worker.

For architecture, data ownership, authentication, migrations, pgvector
indexing, local setup, and production notes, read the
[service guide](docs/service-guide.md).

## Local development

Create a local `story_data` database in Postgres (the existing creditProxy
Postgres container is suitable), then run:

```sh
DATABASE_URL='postgres://postgres:postgres@localhost:5432/story_data?sslmode=disable' AUTH_MODE=dev go run ./cmd/api
```

`AUTH_MODE=dev` requires `X-User-ID` on authenticated requests. Production
uses Firebase ID tokens in `Authorization: Bearer <token>` and requires
`FIREBASE_PROJECT_ID`.

Run migrations with `go run ./cmd/api migrate`. The API also runs outstanding
migrations at startup, under a PostgreSQL advisory lock.

For pgvector local development, use `docker compose up --build`; the bundled
database image includes the `vector` extension. Then run agents with
`STORY_DATA_DATABASE_URL=postgres://postgres:postgres@localhost:5433/story_data`
and `INDEXING_WORKER_ENABLED=true`.

## Initial API

- `GET /v1/public/stories` and `GET /v1/public/stories/{storyId}` (anonymous published-story discovery and reading)
- `GET /v1/public/profiles`, `GET /v1/public/profiles/{userId}`, and authenticated `GET, PUT, PATCH /v1/profiles/me`
- Authenticated reading resume/history under `/v1/me/reading-progress/{storyId}` and `/v1/me/reading-history`
- `GET /v1/public/stories/{storyId}/chapters/{chapterId}` and `POST /v1/public/stories/{storyId}/views`
- Public comment reads plus Firebase-authenticated likes, ratings, and comment mutations under `/v1/stories/{storyId}`
- `GET, POST /v1/stories`
- `GET, PATCH, DELETE /v1/stories/{storyId}`
- `GET, POST /v1/stories/{storyId}/chapters`
- `GET, PATCH, DELETE /v1/stories/{storyId}/chapters/{chapterId}`
- `GET /v1/stories/{storyId}/context` (internal AI context endpoint)

Mutating story and chapter requests require `If-Match: <revision>` when
updating/deleting. A stale revision returns `409 Conflict`.
