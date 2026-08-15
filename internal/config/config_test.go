package config

import "testing"

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
