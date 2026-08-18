package httpapi_test

// End-to-end HTTP tests: a real PostgreSQL database with the real migrations,
// the real store, and the real router. Nothing is mocked — the point is to
// catch the things unit tests cannot, like a route that never dispatches or a
// query that only fails against actual SQL.
//
// The suite creates its own throwaway database so it can never touch the dev
// one. Point it elsewhere with TEST_DATABASE_URL.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/kh1011/novelsync-story-data/internal/auth"
	"github.com/kh1011/novelsync-story-data/internal/httpapi"
	"github.com/kh1011/novelsync-story-data/internal/store"
)

const defaultAdminURL = "postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable"

// testServiceToken is the shared secret the suite's server accepts on
// X-Service-Token. Any other value must be rejected outright.
const testServiceToken = "test-service-token"

var (
	testPool    *pgxpool.Pool
	testServer  *httptest.Server
	truncateSQL string
)

func TestMain(m *testing.M) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		adminURL = defaultAdminURL
	}

	code, err := run(adminURL, m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nstory-data e2e suite could not start: %v\n\n"+
			"It needs a PostgreSQL server. Start one with:\n"+
			"  docker compose up -d postgres\n"+
			"or point the suite elsewhere with TEST_DATABASE_URL.\n\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(adminURL string, m *testing.M) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dbName := fmt.Sprintf("story_data_e2e_%d", os.Getpid())
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		return 0, err
	}
	if err := admin.PingContext(ctx); err != nil {
		admin.Close()
		return 0, err
	}
	// A previous crashed run may have left the database behind.
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %q", dbName)); err != nil {
		admin.Close()
		return 0, err
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %q", dbName)); err != nil {
		admin.Close()
		return 0, err
	}
	defer func() {
		// Terminate stragglers so the drop cannot block on an open handle.
		admin.Exec(fmt.Sprintf(
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'", dbName))
		admin.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %q", dbName))
		admin.Close()
	}()

	testURL := swapDatabase(adminURL, dbName)
	if err := migrateTestDB(ctx, testURL); err != nil {
		return 0, err
	}

	cfg, err := pgxpool.ParseConfig(testURL)
	if err != nil {
		return 0, err
	}
	cfg.ConnConfig.Tracer = &queries
	testPool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return 0, err
	}
	defer testPool.Close()

	if err := testPool.QueryRow(ctx, `
		SELECT string_agg(format('%I', tablename), ', ')
		FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`).Scan(&truncateSQL); err != nil {
		return 0, err
	}

	// AUTH_MODE=dev so tests can act as a user with X-User-ID rather than
	// minting Firebase tokens. Everything below the auth layer is the real path.
	// The suite fires thousands of requests from a handful of uids, so the
	// shared server runs with rate limiting off. TestRateLimit stands up its
	// own server with a real budget.
	testServer = httptest.NewServer(httpapi.New(store.New(testPool),
		auth.New("dev", "", testServiceToken), nil, httpapi.RateLimit{}))
	defer testServer.Close()

	return m.Run(), nil
}

func swapDatabase(dsn, name string) string {
	slash := strings.LastIndex(dsn, "/")
	rest := dsn[slash+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		return dsn[:slash+1] + name + rest[q:]
	}
	return dsn[:slash+1] + name
}

func migrateTestDB(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	dir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		return err
	}
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, db, dir)
}

// ── query counting ──────────────────────────────────────────────────────────

// queryCounter counts every statement the pool issues. It exists so a test can
// assert that a read does not fan out into an N+1 — a behaviour that is
// invisible to status-code assertions and only shows up as latency in
// production.
type queryCounter struct{ n atomic.Int64 }

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}
func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

var queries queryCounter

// resetQueryCount zeroes the counter. Call it immediately before the request
// being measured — test setup issues plenty of statements of its own.
func resetQueryCount() { queries.n.Store(0) }

func queryCount() int64 { return queries.n.Load() }

// reset empties every table so each test starts from a known state.
func reset(t *testing.T) {
	t.Helper()
	if truncateSQL == "" {
		t.Fatal("no tables found to truncate — did migrations run?")
	}
	_, err := testPool.Exec(context.Background(),
		"TRUNCATE "+truncateSQL+" RESTART IDENTITY CASCADE")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
}

// ── request helpers ─────────────────────────────────────────────────────────

type response struct {
	t      *testing.T
	Status int
	Header http.Header
	Body   []byte
}

// call issues a request as `uid` (empty for an anonymous caller). headers are
// applied last so a test can set If-Match or X-Admin.
func call(t *testing.T, method, path, uid string, body any, headers ...map[string]string) response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, testServer.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if uid != "" {
		req.Header.Set("X-User-ID", uid)
	}
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{t: t, Status: res.StatusCode, Header: res.Header, Body: raw}
}

// get is call for the bodyless case, which is most reads.
func get(t *testing.T, path, uid string, headers ...map[string]string) response {
	t.Helper()
	return call(t, "GET", path, uid, nil, headers...)
}

func ifMatch(revision int64) map[string]string {
	return map[string]string{"If-Match": fmt.Sprint(revision)}
}

// expect asserts the status code, failing with the body so a mismatch is
// diagnosable without a second run.
func (r response) expect(status int) response {
	r.t.Helper()
	if r.Status != status {
		r.t.Fatalf("expected %d, got %d — body: %s", status, r.Status, string(r.Body))
	}
	return r
}

func (r response) decode(into any) {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, into); err != nil {
		r.t.Fatalf("decode %q: %v", string(r.Body), err)
	}
}

// json decodes into a generic map for assertions on a single object.
func (r response) json() map[string]any {
	r.t.Helper()
	out := map[string]any{}
	r.decode(&out)
	return out
}

// list decodes into a slice, and fails if the endpoint returned null instead
// of [] — the service guide requires empty collections to serialize as [].
func (r response) list() []map[string]any {
	r.t.Helper()
	if strings.TrimSpace(string(r.Body)) == "null" {
		r.t.Fatalf("collection returned null; the API contract requires []")
	}
	var out []map[string]any
	r.decode(&out)
	return out
}
