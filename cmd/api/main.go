package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
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
	maxConns       = 10
	migrateLockID  = 82104231
)

func main() {
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
	log.Printf("story-data listening on %s", cfg.Addr)
	log.Fatal(http.ListenAndServe(cfg.Addr, h))
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
