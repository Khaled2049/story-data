package store

import "testing"

func TestPassageText(t *testing.T) {
	cases := map[string]string{
		"<p>Quiet &amp; still.</p><p>Then rain.</p>": "Quiet & still. Then rain.",
		`ss="lead">The door opened.</p>`:             "The door opened.",
		"<p>She waited.</p><p cla":                   "She waited.",
		"<h2>One</h2>\n\n<p>  spaced   out </p>":     "One spaced out",
		"<p></p>":                                    "",
	}
	for in, want := range cases {
		if got := passageText(in); got != want {
			t.Errorf("passageText(%q) = %q, want %q", in, got, want)
		}
	}
}
