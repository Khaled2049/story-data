package config

import (
	"testing"
	"time"
)

func TestLoadRejectsProductionWithoutFirebaseProject(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("AUTH_MODE", "production")
	t.Setenv("FIREBASE_PROJECT_ID", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted production without FIREBASE_PROJECT_ID")
	}
}

func TestLoadDevelopment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("AUTH_MODE", "dev")
	c, err := Load()
	if err != nil || c.AuthMode != "dev" {
		t.Fatalf("Load() = %#v, %v", c, err)
	}
}

func TestVoterMinProfileAge(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("AUTH_MODE", "dev")

	// Unset means "use the store's default", which only the store knows.
	t.Setenv("VOTER_MIN_PROFILE_AGE", "")
	c, err := Load()
	if err != nil || c.VoterMinProfileAge != nil {
		t.Fatalf("unset VOTER_MIN_PROFILE_AGE = %v, %v", c.VoterMinProfileAge, err)
	}

	// A local stack turns the gate off explicitly.
	t.Setenv("VOTER_MIN_PROFILE_AGE", "0")
	if c, err = Load(); err != nil || c.VoterMinProfileAge == nil || *c.VoterMinProfileAge != 0 {
		t.Fatalf(`VOTER_MIN_PROFILE_AGE="0" = %v, %v`, c.VoterMinProfileAge, err)
	}

	t.Setenv("VOTER_MIN_PROFILE_AGE", "48h")
	if c, err = Load(); err != nil || c.VoterMinProfileAge == nil || *c.VoterMinProfileAge != 48*time.Hour {
		t.Fatalf(`VOTER_MIN_PROFILE_AGE="48h" = %v, %v`, c.VoterMinProfileAge, err)
	}

	// A typo in a security setting must stop the process, not silently fall
	// back to a default the operator was trying to change.
	for _, bad := range []string{"24", "twenty-four hours", "-1h"} {
		t.Setenv("VOTER_MIN_PROFILE_AGE", bad)
		if _, err := Load(); err == nil {
			t.Errorf("Load() accepted VOTER_MIN_PROFILE_AGE=%q", bad)
		}
	}
}

func TestRateLimitSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("AUTH_MODE", "dev")

	t.Setenv("RATE_LIMIT_READS_PER_MINUTE", "")
	t.Setenv("RATE_LIMIT_WRITES_PER_MINUTE", "")
	c, err := Load()
	if err != nil || c.RateLimitReads != nil || c.RateLimitWrites != nil {
		t.Fatalf("unset rate limits = %v/%v, %v", c.RateLimitReads, c.RateLimitWrites, err)
	}

	// 0 is meaningful — it switches a limit off — so it must survive as a
	// value rather than reading as "unset".
	t.Setenv("RATE_LIMIT_READS_PER_MINUTE", "0")
	t.Setenv("RATE_LIMIT_WRITES_PER_MINUTE", "120")
	if c, err = Load(); err != nil || c.RateLimitReads == nil || *c.RateLimitReads != 0 {
		t.Fatalf("reads=0 = %v, %v", c.RateLimitReads, err)
	}
	if c.RateLimitWrites == nil || *c.RateLimitWrites != 120 {
		t.Fatalf("writes=120 = %v", c.RateLimitWrites)
	}

	for _, bad := range []string{"-1", "lots", "1.5"} {
		t.Setenv("RATE_LIMIT_READS_PER_MINUTE", bad)
		if _, err := Load(); err == nil {
			t.Errorf("Load() accepted RATE_LIMIT_READS_PER_MINUTE=%q", bad)
		}
	}
}
