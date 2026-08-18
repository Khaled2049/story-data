package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// translatePgError is the backstop for a constraint violation no Go validator
// caught. Without it any such error became a 500 — an attacker-triggerable one,
// which poisons the error rate that is the natural alert for a real outage.
func TestTranslatePgError(t *testing.T) {
	for code, want := range map[string]int{
		"23514": http.StatusUnprocessableEntity, // check_violation
		"23502": http.StatusUnprocessableEntity, // not_null_violation
		"22P02": http.StatusUnprocessableEntity, // invalid_text_representation
		"22001": http.StatusUnprocessableEntity, // string_data_right_truncation
		"22003": http.StatusUnprocessableEntity, // numeric_value_out_of_range
		"23503": http.StatusNotFound,            // foreign_key_violation
		"23505": http.StatusConflict,            // unique_violation
	} {
		status, msg, ok := translatePgError(&pgconn.PgError{Code: code, Message: "duplicate key value violates unique constraint \"x\""})
		if !ok || status != want {
			t.Errorf("SQLSTATE %s = (%d, %v), want %d", code, status, ok, want)
		}
		// The message handed to the client must not carry the detail the
		// driver puts on the error.
		if msg == "" || len(msg) > 40 {
			t.Errorf("SQLSTATE %s produced client message %q", code, msg)
		}
	}

	// Wrapped errors still resolve — store methods return them through
	// fmt.Errorf in places.
	if _, _, ok := translatePgError(fmt.Errorf("saving: %w", &pgconn.PgError{Code: "23514"})); !ok {
		t.Error("a wrapped PgError was not recognised")
	}

	// Anything else falls through to the 500 branch, which is what a genuine
	// server fault should still produce.
	for _, e := range []error{errors.New("boom"), &pgconn.PgError{Code: "08006"}, nil} {
		if _, _, ok := translatePgError(e); ok {
			t.Errorf("%v was translated to a 4xx; only known input violations should be", e)
		}
	}
}
