package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"sync"

	firebase "firebase.google.com/go/v4"
	firebaseauth "firebase.google.com/go/v4/auth"
)

type Verifier struct {
	mode         string
	projectID    string
	serviceToken string
	once         sync.Once
	client       *firebaseauth.Client
	initErr      error
}

func New(mode, projectID, serviceToken string) *Verifier {
	return &Verifier{mode: mode, projectID: projectID, serviceToken: serviceToken}
}

func (v *Verifier) UserID(ctx context.Context, r *http.Request) (string, error) {
	uid, _, err := v.identity(ctx, r)
	return uid, err
}

func (v *Verifier) IsAdmin(ctx context.Context, r *http.Request) (bool, error) {
	_, claims, err := v.identity(ctx, r)
	if err != nil {
		return false, err
	}
	admin, _ := claims["admin"].(bool)
	return admin, nil
}

func (v *Verifier) identity(ctx context.Context, r *http.Request) (string, map[string]any, error) {
	// A trusted backend (the MCP server) holds the caller's uid but not their
	// Firebase token, so it presents a shared secret and asserts the uid. Checked
	// before the mode split so it works identically in dev and production.
	//
	// Deliberately never grants admin: a service that can name any user must not
	// also be able to claim privilege for them, so X-Admin is ignored here.
	if presented := strings.TrimSpace(r.Header.Get("X-Service-Token")); presented != "" {
		if v.serviceToken == "" ||
			subtle.ConstantTimeCompare([]byte(presented), []byte(v.serviceToken)) != 1 {
			// Fails closed rather than falling through to the Firebase path: a
			// wrong secret is a misconfigured caller, and reporting it as a
			// missing bearer token would send them chasing the wrong bug.
			return "", nil, fmt.Errorf("invalid service token")
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
	verified, err := v.client.VerifyIDToken(ctx, token)
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
