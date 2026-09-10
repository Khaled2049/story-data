package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/kh1011/novelsync-story-data/internal/auth"
	"github.com/kh1011/novelsync-story-data/internal/config"
	"github.com/kh1011/novelsync-story-data/internal/httpapi"
	"github.com/kh1011/novelsync-story-data/internal/store"
)

const (
	connectTimeout = 60 * time.Second
	migrateTimeout = 5 * time.Minute
	// A full re-derivation of the reader signals. Generous because it scans the
	// social tables end to end, but bounded so a scheduled run cannot hang.
	syncRecsTimeout = 10 * time.Minute
	maxConns        = 10
	migrateLockID   = 82104231

	// Without these the server runs on a zero-value http.Server, where a
	// client that opens a connection and dribbles headers holds a goroutine
	// and a file descriptor for as long as it likes. Cloud Run's own request
	// timeout covers the managed deployment; the container is reachable
	// without that front end locally and anywhere it might be placed later.
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 60 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 1 << 20

	// Cloud Run sends SIGTERM and then waits. Long enough for an in-flight
	// ledger transaction to commit, short enough to stay inside that window.
	shutdownTimeout = 20 * time.Second
)

func main() {
	// JSON to stderr, which is what Cloud Run's logging agent parses into
	// structured entries. Set before anything can log.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), connectTimeout)
	defer cancelConnect()
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	poolCfg.MaxConns = maxConns
	db, err := pgxpool.NewWithConfig(connectCtx, poolCfg)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err = waitForDatabase(connectCtx, db); err != nil {
		log.Fatal(err)
	}
	migrateCtx, cancelMigrate := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancelMigrate()
	if err = migrate(migrateCtx, cfg.DatabaseURL); err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		return
	}
	st := store.New(db)
	// Rebuild the reader signals taleTribe-recs scores on, then exit. Run on a
	// schedule (Cloud Scheduler → a Cloud Run job), not by the serving process:
	// recommendation staleness of minutes is not a product problem, and this
	// keeps a full scan of the social tables off the request path.
	if len(os.Args) > 1 && os.Args[1] == "sync-recs" {
		syncCtx, cancelSync := context.WithTimeout(context.Background(), syncRecsTimeout)
		defer cancelSync()
		stats, e := st.SyncRecommendationSignals(syncCtx)
		if e != nil {
			log.Fatal(e)
		}
		slog.Info("recommendation signals synced",
			"interactions", stats.Interactions, "views", stats.Views)
		return
	}
	if cfg.VoterMinProfileAge != nil {
		st.VoterMinProfileAge = *cfg.VoterMinProfileAge
	}
	rl := httpapi.DefaultRateLimit
	if cfg.RateLimitReads != nil {
		rl.ReadsPerMinute = *cfg.RateLimitReads
	}
	if cfg.RateLimitWrites != nil {
		rl.WritesPerMinute = *cfg.RateLimitWrites
	}
	h := httpapi.New(st, auth.New(cfg.AuthMode, cfg.FirebaseProjectID, cfg.ServiceToken), cfg.CORSOrigins, rl)
	if err = serve(newServer(cfg.Addr, h)); err != nil {
		log.Fatal(err)
	}
}

// newServer builds the HTTP server with every timeout set. Split out so a test
// can assert none of them is zero — go vet does not catch a bare
// http.ListenAndServe, and the failure mode is invisible until someone leans
// on it.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// serve runs until the process is asked to stop, then drains. The previous
// code dropped whatever was in flight when Cloud Run replaced an instance,
// including half-finished ledger transfers.
func serve(srv *http.Server) error {
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	errs := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if e := srv.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			errs <- e
			return
		}
		errs <- nil
	}()

	select {
	case e := <-errs:
		return e
	case <-stop.Done():
	}

	slog.Info("shutting down", "timeout", shutdownTimeout.String())
	ctx, done := context.WithTimeout(context.Background(), shutdownTimeout)
	defer done()
	if e := srv.Shutdown(ctx); e != nil {
		// Shutdown returning here means requests were still running when the
		// grace period ran out; the listener is closed either way.
		slog.Error("shutdown timed out with requests still in flight", "error", e)
		return e
	}
	return <-errs
}

func waitForDatabase(ctx context.Context, db *pgxpool.Pool) error {
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if err = db.Ping(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return err
}
func migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrateLockID); err != nil {
		return err
	}
	defer func() {
		var released bool
		if err := conn.QueryRowContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrateLockID).Scan(&released); err != nil {
			log.Printf("releasing migration lock: %v", err)
		} else if !released {
			log.Printf("migration lock %d was not held by this session", migrateLockID)
		}
	}()
	return goose.UpContext(ctx, db, "migrations")
}
