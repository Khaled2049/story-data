package httpapi

import "testing"

func TestETagMatches(t *testing.T) {
	const etag = `W/"abc"`
	cases := []struct {
		header string
		want   bool
	}{
		{`W/"abc"`, true},
		{`"abc"`, true},
		{`"xyz", W/"abc"`, true},
		{`*`, true},
		{`W/"xyz"`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := etagMatches(c.header, etag); got != c.want {
			t.Errorf("etagMatches(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}
