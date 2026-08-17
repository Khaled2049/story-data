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
	// ServiceToken lets a trusted backend assert a caller identity via
	// X-User-ID without holding that user's Firebase token. Empty disables the
	// path entirely: an unconfigured deployment must never trust a header.
	ServiceToken string
	CORSOrigins  []string
}

func Load() (Config, error) {
	c := Config{
		Addr:              value("ADDR", ":8080"),
		DatabaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
		AuthMode:          value("AUTH_MODE", "dev"),
		FirebaseProjectID: strings.TrimSpace(os.Getenv("FIREBASE_PROJECT_ID")),
		ServiceToken:      strings.TrimSpace(os.Getenv("SERVICE_TOKEN")),
		CORSOrigins:       splitList(os.Getenv("CORS_ORIGINS")),
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

func splitList(v string) []string {
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func value(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
