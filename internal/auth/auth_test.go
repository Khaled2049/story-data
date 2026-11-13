package auth

// The service paths return before ensure() is reached, so these tests need no
// Firebase credentials. The shared secret is a dev-only credential and is
// tested in dev mode; production admits only a Google ID token.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/api/idtoken"
)

const (
	token    = "s3cret"
	audience = "https://story-data.example.run.app"
	agents   = "agents-run@proj.iam.gserviceaccount.com"
)

func request(headers map[string]string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/v1/stories", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// idToken builds a JWT whose signature fakeValidate accepts only when signed.
func idToken(t *testing.T, claims map[string]any, signed bool) string {
	t.Helper()
	body := map[string]any{
		"iss": "https://accounts.google.com", "aud": audience, "exp": 4102444800,
		"email": agents, "email_verified": true,
	}
	for k, v := range claims {
		if v == nil {
			delete(body, k)
		} else {
			body[k] = v
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	sig := "forged"
	if signed {
		sig = "signed"
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc(raw) + "." + enc([]byte(sig))
}

// fakeValidate stands in for Google's key check: it trusts the payload only
// when the signature segment says "signed", and enforces the audience as
// idtoken.Validate does.
func fakeValidate(_ context.Context, tok, aud string) (*idtoken.Payload, error) {
	parts := strings.Split(tok, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[len(parts)-1])
	if string(sig) != "signed" {
		return nil, errors.New("bad signature")
	}
	p, err := idtoken.ParsePayload(tok)
	if err != nil {
		return nil, err
	}
	if aud != "" && p.Audience != aud {
		return nil, errors.New("audience mismatch")
	}
	return p, nil
}

func production() *Verifier {
	return New("production", "proj", "").WithServiceAccounts([]string{audience}, []string{agents}, fakeValidate)
}

func serviceRequest(tok, uid string) *http.Request {
	h := map[string]string{ServiceIDTokenHeader: "Bearer " + tok}
	if uid != "" {
		h["X-User-ID"] = uid
	}
	return request(h)
}

func TestServiceIDTokenAssertsUser(t *testing.T) {
	v := production()
	r := serviceRequest(idToken(t, nil, true), "user-42")
	uid, err := v.UserID(context.Background(), r)
	if err != nil || uid != "user-42" {
		t.Fatalf("UserID = %q, %v; want user-42", uid, err)
	}
	if class, caller := v.Caller(r); class != "service" || caller != agents {
		t.Errorf("Caller = %q, %q; want service, %s", class, caller, agents)
	}
}

func TestServiceIDTokenIsRefusedUnlessEveryCheckPasses(t *testing.T) {
	cases := map[string]string{
		"forged signature":   idToken(t, nil, false),
		"wrong audience":     idToken(t, map[string]any{"aud": "https://elsewhere.example"}, true),
		"no audience":        idToken(t, map[string]any{"aud": nil}, true),
		"wrong issuer":       idToken(t, map[string]any{"iss": "https://evil.example"}, true),
		"other account":      idToken(t, map[string]any{"email": "someone@proj.iam.gserviceaccount.com"}, true),
		"unverified email":   idToken(t, map[string]any{"email_verified": false}, true),
		"no email":           idToken(t, map[string]any{"email": nil}, true),
		"not a JWT":          "not-a-jwt",
		"oversized":          strings.Repeat("a", maxIDTokenBytes+1),
		"empty after Bearer": "",
	}
	for name, tok := range cases {
		r := serviceRequest(tok, "user-42")
		if _, err := production().UserID(context.Background(), r); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if production().IsService(r) {
			t.Errorf("%s: IsService reported true", name)
		}
	}
}

func TestServiceIDTokenRequiresUserID(t *testing.T) {
	if _, err := production().UserID(context.Background(), serviceRequest(idToken(t, nil, true), "")); err == nil {
		t.Fatal("a valid service identity with no X-User-ID must be rejected")
	}
}

func TestServiceIDTokenNeverGrantsAdmin(t *testing.T) {
	r := serviceRequest(idToken(t, nil, true), "user-42")
	r.Header.Set("X-Admin", "true")
	uid, admin, err := production().Admin(context.Background(), r)
	if err != nil || uid != "user-42" || admin {
		t.Fatalf("Admin = %q, %v, %v; want user-42 without admin", uid, admin, err)
	}
}

func TestProductionRefusesTheSharedSecret(t *testing.T) {
	// Even a deployment that still has the secret configured must refuse it,
	// and must not fall through to the Firebase path.
	v := New("production", "proj", token).WithServiceAccounts([]string{audience}, []string{agents}, fakeValidate)
	_, err := v.UserID(context.Background(), request(map[string]string{
		ServiceTokenHeader: token, "X-User-ID": "user-42",
	}))
	if err == nil || !strings.Contains(err.Error(), "not accepted in production") {
		t.Fatalf("err = %v; want the shared secret refused", err)
	}
}

func TestProductionWithoutServiceAccountsAdmitsNoService(t *testing.T) {
	v := New("production", "proj", "")
	if _, err := v.UserID(context.Background(), serviceRequest(idToken(t, nil, true), "user-42")); err == nil {
		t.Fatal("with no SERVICE_ACCOUNTS, a service identity must be rejected")
	}
}

func TestDevServiceTokenAssertsUser(t *testing.T) {
	uid, err := New("dev", "", token).UserID(context.Background(), request(map[string]string{
		ServiceTokenHeader: token, "X-User-ID": "user-42",
	}))
	if err != nil || uid != "user-42" {
		t.Fatalf("UserID = %q, %v; want user-42", uid, err)
	}
}

func TestWrongDevServiceTokenDoesNotFallThrough(t *testing.T) {
	// Dev mode trusts X-User-ID alone, so a wrong token must fail as what it
	// is rather than quietly authenticate through the dev branch.
	_, err := New("dev", "", token).UserID(context.Background(), request(map[string]string{
		ServiceTokenHeader: "wrong", "X-User-ID": "user-42",
	}))
	if err == nil || err.Error() != "invalid service token" {
		t.Fatalf("err = %v; want invalid service token", err)
	}
	if _, err := New("dev", "", "").UserID(context.Background(), request(map[string]string{
		ServiceTokenHeader: "anything", "X-User-ID": "user-42",
	})); err == nil {
		t.Fatal("with no SERVICE_TOKEN set, any presented token must be rejected")
	}
}

func TestDevRefusesServiceIDTokens(t *testing.T) {
	v := New("dev", "", token).WithServiceAccounts([]string{audience}, []string{agents}, fakeValidate)
	if _, err := v.UserID(context.Background(), serviceRequest(idToken(t, nil, true), "user-42")); err == nil {
		t.Fatal("dev mode must not accept a service ID token")
	}
}

func TestDevPathUnchanged(t *testing.T) {
	v := New("dev", "", token)
	uid, err := v.UserID(context.Background(), request(map[string]string{"X-User-ID": "dev-user"}))
	if err != nil || uid != "dev-user" {
		t.Fatalf("dev mode should still trust X-User-ID; got %q, %v", uid, err)
	}
	uid, admin, err := v.Admin(context.Background(), request(map[string]string{
		"X-User-ID": "dev-user", "X-Admin": "true",
	}))
	if err != nil || uid != "dev-user" || !admin {
		t.Fatalf("dev path got %q, %v, %v; want dev-user with admin", uid, admin, err)
	}
	_, admin, err = v.Admin(context.Background(), request(map[string]string{
		ServiceTokenHeader: token, "X-User-ID": "dev-user", "X-Admin": "true",
	}))
	if err != nil || admin {
		t.Fatalf("the dev service path must never grant admin; got %v, %v", admin, err)
	}
}

func TestIsServiceAgreesWithIdentity(t *testing.T) {
	v := New("dev", "proj", token)
	cases := map[string]bool{
		"":          false,
		" ":         false,
		" ":         false, // net/http keeps it; TrimSpace removes it
		"wrong":     false,
		token:       true,
		" " + token: true,
	}
	for presented, want := range cases {
		r := request(map[string]string{ServiceTokenHeader: presented, "X-User-ID": "user-42"})
		if got := v.IsService(r); got != want {
			t.Errorf("IsService(%q) = %v, want %v", presented, got, want)
		}
		// A blank header is no credential, so dev's X-User-ID path answers.
		blank := strings.TrimSpace(presented) == ""
		if _, err := v.UserID(context.Background(), r); (err == nil) != (want || blank) {
			t.Errorf("UserID with %q: err = %v", presented, err)
		}
	}
}

// ResolveService records one verdict that later reads reuse, so a token is not
// re-verified per check and every reader agrees.
func TestResolvedVerdictIsReused(t *testing.T) {
	calls := 0
	counting := func(ctx context.Context, tok, aud string) (*idtoken.Payload, error) {
		calls++
		return fakeValidate(ctx, tok, aud)
	}
	v := New("production", "proj", "").WithServiceAccounts([]string{audience}, []string{agents}, counting)
	r := v.ResolveService(serviceRequest(idToken(t, nil, true), "user-42"))
	for i := 0; i < 3; i++ {
		if !v.IsService(r) {
			t.Fatal("verified service lost on re-read")
		}
	}
	if _, err := v.UserID(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("validated %d times, want 1", calls)
	}
}
