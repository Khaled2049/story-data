package httpapi_test

// The service-token path end to end through the real router. The suite runs
// AUTH_MODE=dev, where X-User-ID is trusted anyway, so the load-bearing case
// here is the negative one: a wrong token must fail closed *before* the dev
// branch would have accepted the same X-User-ID.

import (
	"net/http"
	"testing"
)

func serviceHeaders(token, uid string) map[string]string {
	return map[string]string{"X-Service-Token": token, "X-User-ID": uid}
}

func TestServiceTokenReadsOwnerScopedData(t *testing.T) {
	reset(t)

	newStory(t, alice, "Salt and Sextant")

	// Passing uid "" so the helper does not set X-User-ID itself; the service
	// headers carry the asserted identity instead.
	body := call(t, "GET", "/v1/stories", "", nil,
		serviceHeaders(testServiceToken, alice)).expect(http.StatusOK).list()
	if len(body) != 1 {
		t.Fatalf("expected alice's one story, got %d", len(body))
	}
	if body[0]["title"] != "Salt and Sextant" {
		t.Errorf("title = %v, want Salt and Sextant", body[0]["title"])
	}

	// The asserted uid scopes the read: bob sees his own (empty) list, not alice's.
	other := call(t, "GET", "/v1/stories", "", nil,
		serviceHeaders(testServiceToken, bob)).expect(http.StatusOK).list()
	if len(other) != 0 {
		t.Fatalf("asserting bob must not return alice's stories, got %d", len(other))
	}
}

func TestWrongServiceTokenIsRejected(t *testing.T) {
	reset(t)
	newStory(t, alice, "Salt and Sextant")

	// X-User-ID alone would succeed in dev mode, so a 401 here proves the wrong
	// token short-circuits rather than falling through.
	call(t, "GET", "/v1/stories", "", nil,
		serviceHeaders("not-the-token", alice)).expect(http.StatusUnauthorized)
}

func TestServiceTokenWithoutUserIDIsRejected(t *testing.T) {
	reset(t)
	call(t, "GET", "/v1/stories", "", nil,
		map[string]string{"X-Service-Token": testServiceToken}).expect(http.StatusUnauthorized)
}

func TestServiceTokenCannotClaimAdmin(t *testing.T) {
	reset(t)
	headers := serviceHeaders(testServiceToken, alice)
	headers["X-Admin"] = "true"
	call(t, "POST", "/v1/admin/token-grants", "", map[string]any{
		"userId": bob, "amount": "1000", "idempotencyKey": "svc-admin-probe",
	}, headers).expect(http.StatusForbidden)
}
