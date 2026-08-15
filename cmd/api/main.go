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

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err = waitForDatabase(ctx, db); err != nil {
		log.Fatal(err)
	}
	if err = migrate(ctx, cfg.DatabaseURL); err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		return
	}
	h := httpapi.New(store.New(db), auth.New(cfg.AuthMode, cfg.FirebaseProjectID))
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
	if _, err = db.ExecContext(ctx, "SELECT pg_advisory_lock(82104231)"); err != nil {
		return err
	}
	defer db.ExecContext(ctx, "SELECT pg_advisory_unlock(82104231)")
	return goose.UpContext(ctx, db, "migrations")
}
