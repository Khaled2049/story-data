package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	firebase "firebase.google.com/go/v4"
	firebaseauth "firebase.google.com/go/v4/auth"
	"google.golang.org/api/idtoken"
)

// ValidateFunc checks an ID token's signature and expiry for one audience.
// Production uses idtoken.Validate; tests substitute their own.
type ValidateFunc func(ctx context.Context, token, audience string) (*idtoken.Payload, error)

type Verifier struct {
	mode      string
	projectID string
	// serviceToken is the dev-only shared secret. Production refuses it.
	serviceToken string
	// audiences and accounts admit a production service caller: a
	// Google-signed ID token for one of these audiences, from one of these
	// service accounts. Either empty disables the path.
	audiences []string
	accounts  []string
	validate  ValidateFunc
	once      sync.Once
	client    *firebaseauth.Client
	initErr   error
}

func New(mode, projectID, serviceToken string) *Verifier {
	return &Verifier{mode: mode, projectID: projectID, serviceToken: serviceToken}
}

// WithServiceAccounts admits production service callers. A nil validate uses
// Google's published keys.
func (v *Verifier) WithServiceAccounts(audiences, accounts []string, validate ValidateFunc) *Verifier {
	v.audiences = audiences
	v.accounts = accounts
	v.validate = validate
	if v.validate == nil {
		v.validate = idtoken.Validate
	}
	return v
}

// ServiceTokenHeader carries the dev-only shared secret; ServiceIDTokenHeader
// carries a production caller's Google ID token. Authorization is left to the
// end user's Firebase token, so the two never share a header.
const (
	ServiceTokenHeader   = "X-Service-Token"
	ServiceIDTokenHeader = "X-Service-Authorization"
)

// maxIDTokenBytes bounds what is decoded before any signature check.
const maxIDTokenBytes = 4096

var errServiceCredential = errors.New("invalid service credential")

// ServiceCall is the outcome of checking a request's service credential.
type ServiceCall struct {
	// Present is true when the request carried a service credential at all.
	Present bool
	// Caller names the verified service: its account email, or "service-token"
	// for the dev secret.
	Caller string
	Err    error
}

func (c ServiceCall) Verified() bool { return c.Present && c.Err == nil }

type ctxKey int

const serviceCallKey ctxKey = 0

// ResolveService checks the service credential once and records the outcome
// on the request, so the limiter, handlers and log agree without re-verifying.
func (v *Verifier) ResolveService(r *http.Request) *http.Request {
	call := v.resolveService(r)
	return r.WithContext(context.WithValue(r.Context(), serviceCallKey, call))
}

// Service returns the outcome ResolveService recorded, resolving it here when
// the request did not pass through that middleware.
func (v *Verifier) Service(r *http.Request) ServiceCall {
	if call, ok := r.Context().Value(serviceCallKey).(ServiceCall); ok {
		return call
	}
	return v.resolveService(r)
}

func (v *Verifier) resolveService(r *http.Request) ServiceCall {
	secret := strings.TrimSpace(r.Header.Get(ServiceTokenHeader))
	bearer := strings.TrimSpace(r.Header.Get(ServiceIDTokenHeader))
	if secret == "" && bearer == "" {
		return ServiceCall{}
	}
	call := ServiceCall{Present: true}
	switch {
	case v.mode == "dev" && secret != "":
		if !v.validServiceToken(secret) {
			call.Err = fmt.Errorf("invalid service token")
			return call
		}
		call.Caller = "service-token"
	case v.mode == "dev":
		call.Err = fmt.Errorf("service ID tokens are verified only in production")
	case secret != "":
		// The shared secret is a credential anyone holding it can use from
		// anywhere, so production accepts only a Google-signed identity.
		call.Err = fmt.Errorf("service token is not accepted in production")
	default:
		call.Caller, call.Err = v.verifyServiceIDToken(r.Context(), strings.TrimPrefix(bearer, "Bearer "))
	}
	return call
}

// verifyServiceIDToken returns the calling service account. idtoken skips the
// audience check for an empty audience and never checks the issuer, so both
// are checked here.
func (v *Verifier) verifyServiceIDToken(ctx context.Context, raw string) (string, error) {
	if len(v.accounts) == 0 || len(v.audiences) == 0 || v.validate == nil {
		return "", fmt.Errorf("service callers are not configured")
	}
	if raw == "" || len(raw) > maxIDTokenBytes {
		return "", errServiceCredential
	}
	claimed, err := idtoken.ParsePayload(raw)
	if err != nil || claimed.Audience == "" || !slices.Contains(v.audiences, claimed.Audience) {
		return "", errServiceCredential
	}
	payload, err := v.validate(ctx, raw, claimed.Audience)
	if err != nil || payload.Audience != claimed.Audience {
		return "", errServiceCredential
	}
	if payload.Issuer != "https://accounts.google.com" && payload.Issuer != "accounts.google.com" {
		return "", errServiceCredential
	}
	email, _ := payload.Claims["email"].(string)
	verified, _ := payload.Claims["email_verified"].(bool)
	if !verified || email == "" || !slices.Contains(v.accounts, email) {
		return "", errServiceCredential
	}
	return email, nil
}

func (v *Verifier) UserID(ctx context.Context, r *http.Request) (string, error) {
	uid, _, err := v.identity(ctx, r, false)
	return uid, err
}

func (v *Verifier) IsAdmin(ctx context.Context, r *http.Request) (bool, error) {
	_, claims, err := v.identity(ctx, r, false)
	if err != nil {
		return false, err
	}
	admin, _ := claims["admin"].(bool)
	return admin, nil
}

// Admin verifies once for the /v1/admin routes and also checks that the token
// was not revoked, so removing the claim and revoking sessions takes effect now.
func (v *Verifier) Admin(ctx context.Context, r *http.Request) (string, bool, error) {
	uid, claims, err := v.identity(ctx, r, true)
	if err != nil {
		return "", false, err
	}
	admin, _ := claims["admin"].(bool)
	return uid, admin, nil
}

// IsService reports whether the request carries a verified service credential.
// Handlers must use this, not the raw headers, so they agree with identity().
func (v *Verifier) IsService(r *http.Request) bool {
	return v.Service(r).Verified()
}

// Caller names the credential identity() accepts for this request and, for a
// service, which one. For the access log; meaningful only after identity().
func (v *Verifier) Caller(r *http.Request) (class, service string) {
	if call := v.Service(r); call.Verified() {
		return "service", call.Caller
	}
	if v.mode == "dev" {
		return "dev", ""
	}
	return "firebase", ""
}

func (v *Verifier) validServiceToken(presented string) bool {
	return v.serviceToken != "" &&
		subtle.ConstantTimeCompare([]byte(presented), []byte(v.serviceToken)) == 1
}

func (v *Verifier) identity(ctx context.Context, r *http.Request, checkRevoked bool) (string, map[string]any, error) {
	// A trusted backend (MCP, the assistant) holds the caller's uid but not
	// their Firebase token, so it proves its own identity and asserts the uid.
	// It never grants admin: a service that can name any user must not also
	// claim privilege for them. A bad credential fails closed rather than
	// falling through to the user paths.
	if call := v.Service(r); call.Present {
		if call.Err != nil {
			return "", nil, call.Err
		}
		uid := strings.TrimSpace(r.Header.Get("X-User-ID"))
		if uid == "" {
			return "", nil, fmt.Errorf("missing X-User-ID")
		}
		return uid, map[string]any{"admin": false}, nil
	}
	if v.mode == "dev" {
		uid := strings.TrimSpace(r.Header.Get("X-User-ID"))
		if uid == "" {
			return "", nil, fmt.Errorf("missing X-User-ID")
		}
		return uid, map[string]any{"admin": r.Header.Get("X-Admin") == "true"}, nil
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return "", nil, fmt.Errorf("missing Firebase bearer token")
	}
	if err := v.ensure(); err != nil {
		return "", nil, err
	}
	verify := v.client.VerifyIDToken
	if checkRevoked {
		verify = v.client.VerifyIDTokenAndCheckRevoked
	}
	verified, err := verify(ctx, token)
	if err != nil {
		return "", nil, fmt.Errorf("invalid Firebase bearer token")
	}
	return verified.UID, verified.Claims, nil
}

func (v *Verifier) ensure() error {
	v.once.Do(func() {
		app, err := firebase.NewApp(context.Background(), &firebase.Config{ProjectID: v.projectID})
		if err != nil {
			v.initErr = err
			return
		}
		v.client, v.initErr = app.Auth(context.Background())
	})
	return v.initErr
}
