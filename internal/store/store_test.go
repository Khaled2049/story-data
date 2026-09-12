package store

import (
	"errors"
	"testing"
	"time"
)

func TestWordCount(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"", 0},
		{"one", 1},
		{" one\n two\tthree ", 3},
	} {
		if got := wordCount(tc.text); got != tc.want {
			t.Fatalf("wordCount(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestPublicStoryCursorRoundTrip(t *testing.T) {
	story := PublicStory{
		ID:        "0198f649-2f2e-7b5c-8d66-3b3eb71761cd",
		UpdatedAt: time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC),
	}
	encoded, err := encodePublicStoryCursor(story)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePublicStoryCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != story.ID || !decoded.UpdatedAt.Equal(story.UpdatedAt) {
		t.Fatalf("cursor round trip = %#v, want %#v", decoded, story)
	}
}

func TestPublicStoryCursorRejectsMalformedValues(t *testing.T) {
	if _, err := decodePublicStoryCursor("not-a-cursor"); err == nil {
		t.Fatal("expected malformed cursor to fail")
	}
}

func TestProfileValidation(t *testing.T) {
	if _, ok := normalizeUsername("Alice_1"); !ok {
		t.Fatal("expected valid username")
	}
	if _, ok := normalizeUsername("two words"); ok {
		t.Fatal("expected invalid username")
	}
	if got, ok := normalizeWallet("0xAbCd000000000000000000000000000000000000"); !ok || got != "0xabcd000000000000000000000000000000000000" {
		t.Fatalf("wallet normalization = %q, %v", got, ok)
	}
	if _, ok := normalizeWallet("not-a-wallet"); ok {
		t.Fatal("expected invalid wallet")
	}
}

func TestLimitErrfMatchesErrLimit(t *testing.T) {
	err := limitErrf("a chapter can hold at most %d words", wordLimit)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("limitErrf() does not match ErrLimit; httpapi would return 500 instead of 422")
	}
	if got := err.Error(); got != "a chapter can hold at most 5000 words" {
		t.Fatalf("limitErrf() = %q, want the formatted message alone", got)
	}
	if errors.Is(err, ErrValidation) {
		t.Fatal("limitErrf() must not match unrelated sentinels")
	}
}

func TestConflictErrfMatchesErrConflict(t *testing.T) {
	err := conflictErrf("a chapter already occupies position %v in this story", float64(2))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("conflictErrf() does not match ErrConflict; httpapi would return 500 instead of 409")
	}
	if got := err.Error(); got != "a chapter already occupies position 2 in this story" {
		t.Fatalf("conflictErrf() = %q, want the formatted message alone", got)
	}
	if errors.Is(err, ErrLimit) {
		t.Fatal("conflictErrf() must not match unrelated sentinels")
	}
}
