package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Addr              string
	DatabaseURL       string
	AuthMode          string
	FirebaseProjectID string
}

func Load() (Config, error) {
	c := Config{
		Addr:              value("ADDR", ":8080"),
		DatabaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
		AuthMode:          value("AUTH_MODE", "dev"),
		FirebaseProjectID: strings.TrimSpace(os.Getenv("FIREBASE_PROJECT_ID")),
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if c.AuthMode != "dev" && c.AuthMode != "production" {
		return Config{}, fmt.Errorf("AUTH_MODE must be dev or production")
	}
	if c.AuthMode == "production" && c.FirebaseProjectID == "" {
		return Config{}, fmt.Errorf("FIREBASE_PROJECT_ID is required in production")
	}
	return c, nil
}

func value(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
