package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/kh1011/novelsync-story-data/internal/store"
)

func TestWriterAgreementRequiredAndRecorded(t *testing.T) {
	reset(t)
	const writer = "new-writer"
	get(t, "/v1/me/writer-agreement", "").expect(http.StatusUnauthorized)
	state := get(t, "/v1/me/writer-agreement", writer).expect(http.StatusOK).json()
	if state["accepted"] != false || state["attestation"] != store.WriterAttestation {
		t.Fatalf("unexpected initial agreement: %v", state)
	}
	call(t, "POST", "/v1/stories", writer, map[string]any{"title": "Blocked"}).expect(http.StatusForbidden)
	body := map[string]any{
		"termsVersion": store.TermsVersion, "privacyVersion": store.PrivacyVersion,
		"attestationVersion": store.AttestationVersion,
		"agreeTerms":         true, "acknowledgePrivacy": true, "attestRights": true, "adult": true,
	}
	call(t, "POST", "/v1/me/writer-agreement", "", body).expect(http.StatusUnauthorized)
	call(t, "POST", "/v1/me/writer-agreement", "", body, serviceHeaders(testServiceToken, writer)).expect(http.StatusForbidden)
	for _, field := range []string{"agreeTerms", "acknowledgePrivacy", "attestRights", "adult"} {
		body[field] = false
		call(t, "POST", "/v1/me/writer-agreement", writer, body).expect(http.StatusUnprocessableEntity)
		body[field] = true
	}
	for _, field := range []string{"termsVersion", "privacyVersion", "attestationVersion"} {
		original := body[field]
		body[field] = "old-version"
		call(t, "POST", "/v1/me/writer-agreement", writer, body).expect(http.StatusUnprocessableEntity)
		body[field] = original
	}
	accepted := call(t, "POST", "/v1/me/writer-agreement", writer, body).expect(http.StatusOK).json()
	if accepted["accepted"] != true || accepted["acceptedAt"] == nil {
		t.Fatalf("missing acceptance record: %v", accepted)
	}
	again := call(t, "POST", "/v1/me/writer-agreement", writer, body).expect(http.StatusOK).json()
	if again["acceptedAt"] != accepted["acceptedAt"] {
		t.Fatal("retry changed original acceptance time")
	}
	story := call(t, "POST", "/v1/stories", writer, map[string]any{"title": "Allowed"}).expect(http.StatusCreated).json()
	if n := len(get(t, "/v1/stories", writer).expect(http.StatusOK).list()); n != 1 {
		t.Fatalf("blocked create persisted: %d stories", n)
	}
	if other := get(t, "/v1/me/writer-agreement", "other-writer").expect(http.StatusOK).json(); other["accepted"] != false {
		t.Fatal("acceptance leaked across users")
	}

	// Simulate a policy bump: an earlier version remains evidence, but no
	// longer authorizes writing under the current version.
	if _, err := testPool.Exec(context.Background(), `UPDATE writer_agreements SET terms_version='old-version' WHERE user_id=$1`, writer); err != nil {
		t.Fatal(err)
	}
	get(t, "/v1/me/writer-agreement", writer).expect(http.StatusOK)
	path := "/v1/stories/" + story["id"].(string)
	call(t, "PATCH", path, writer, map[string]any{"title": "Blocked edit"}, ifMatch(1)).expect(http.StatusForbidden)
	call(t, "POST", path+"/chapters", writer, map[string]any{"title": "Blocked chapter", "position": 1}).expect(http.StatusForbidden)
	// Removal stays possible without accepting an updated agreement.
	call(t, "DELETE", path, writer, nil, ifMatch(1)).expect(http.StatusNoContent)
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM writer_agreements WHERE user_id=$1`, writer).Scan(&count); err != nil || count != 1 {
		t.Fatalf("agreement evidence lost: count=%d err=%v", count, err)
	}
}
