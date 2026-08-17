package auth

// The service-token path is exercised in production mode on purpose: it returns
// before ensure() is reached, so these tests need no Firebase credentials and
// still cover the mode real deployments run.

import (
	"context"
	"net/http"
	"testing"
)

const token = "s3cret"

func request(headers map[string]string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/v1/stories", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestServiceTokenAssertsUser(t *testing.T) {
	v := New("production", "proj", token)
	uid, err := v.UserID(context.Background(), request(map[string]string{
		"X-Service-Token": token,
		"X-User-ID":       "user-42",
	}))
	if err != nil {
		t.Fatalf("expected the asserted uid to be accepted, got %v", err)
	}
	if uid != "user-42" {
		t.Fatalf("uid = %q, want user-42", uid)
	}
}

func TestServiceTokenRequiresUserID(t *testing.T) {
	v := New("production", "proj", token)
	if _, err := v.UserID(context.Background(), request(map[string]string{
		"X-Service-Token": token,
	})); err == nil {
		t.Fatal("a valid service token with no X-User-ID must be rejected")
	}
}

func TestWrongServiceTokenDoesNotFallThrough(t *testing.T) {
	// The Firebase path would report a missing bearer token, which sends a
	// misconfigured caller after the wrong bug. It must fail as what it is.
	v := New("production", "proj", token)
	_, err := v.UserID(context.Background(), request(map[string]string{
		"X-Service-Token": "wrong",
		"X-User-ID":       "user-42",
	}))
	if err == nil {
		t.Fatal("a wrong service token must be rejected")
	}
	if err.Error() != "invalid service token" {
		t.Fatalf("error = %q, want invalid service token", err.Error())
	}
}

func TestServiceTokenIgnoredWhenUnconfigured(t *testing.T) {
	// An unconfigured deployment must not be talked into trusting a header, so
	// a presented token is rejected rather than matched against "".
	v := New("production", "proj", "")
	if _, err := v.UserID(context.Background(), request(map[string]string{
		"X-Service-Token": "",
		"X-User-ID":       "user-42",
	})); err == nil {
		t.Fatal("with no SERVICE_TOKEN set, X-User-ID alone must not authenticate")
	}
	if _, err := v.UserID(context.Background(), request(map[string]string{
		"X-Service-Token": "anything",
		"X-User-ID":       "user-42",
	})); err == nil {
		t.Fatal("with no SERVICE_TOKEN set, any presented token must be rejected")
	}
}

func TestServiceTokenNeverGrantsAdmin(t *testing.T) {
	v := New("production", "proj", token)
	admin, err := v.IsAdmin(context.Background(), request(map[string]string{
		"X-Service-Token": token,
		"X-User-ID":       "user-42",
		"X-Admin":         "true",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if admin {
		t.Fatal("the service path must never grant admin, even with X-Admin: true")
	}
}

func TestDevPathUnchanged(t *testing.T) {
	v := New("dev", "", token)
	uid, err := v.UserID(context.Background(), request(map[string]string{"X-User-ID": "dev-user"}))
	if err != nil || uid != "dev-user" {
		t.Fatalf("dev mode should still trust X-User-ID; got %q, %v", uid, err)
	}
	admin, err := v.IsAdmin(context.Background(), request(map[string]string{
		"X-User-ID": "dev-user",
		"X-Admin":   "true",
	}))
	if err != nil || !admin {
		t.Fatalf("dev mode should still honor X-Admin; got %v, %v", admin, err)
	}
}
