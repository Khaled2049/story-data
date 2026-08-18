package config

import (
	"fmt"
	"os"
	"strings"
	"time"
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
	// VoterMinProfileAge is how long a public profile must exist before its
	// owner may cast a competition ballot. nil means the variable was unset
	// and the store's own default applies — local stacks set it to 0 so a
	// freshly seeded account can vote.
	VoterMinProfileAge *time.Duration
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
	age, e := optionalDuration("VOTER_MIN_PROFILE_AGE")
	if e != nil {
		return Config{}, e
	}
	c.VoterMinProfileAge = age
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

// optionalDuration parses a Go duration such as "24h" or "0", returning nil
// when the variable is unset. A malformed value is an error rather than a
// silent fallback: a typo in a security setting must stop the process, not
// quietly restore the default it was meant to change.
func optionalDuration(key string) (*time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil, nil
	}
	d, e := time.ParseDuration(raw)
	if e != nil {
		return nil, fmt.Errorf("%s must be a duration such as 24h: %w", key, e)
	}
	if d < 0 {
		return nil, fmt.Errorf("%s must not be negative", key)
	}
	return &d, nil
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
