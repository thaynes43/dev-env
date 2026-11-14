package sessionraise

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeFoldsWords(t *testing.T) {
	yes := WordsHash("yes")
	if WordsHash("Yes.") != yes || WordsHash("Ｙｅｓ") != yes || WordsHash("  yes  ") != yes {
		t.Fatalf("yes, Yes. and fullwidth yes hashed differently: %s %s %s", WordsHash("Yes."), WordsHash("Ｙｅｓ"), yes)
	}
	if Normalize("Yes,  Tom.") != "yes tom" {
		t.Fatalf("normalise %q", Normalize("Yes,  Tom."))
	}
	if WordsProblem("") != "empty" || WordsProblem("...") != "empty" {
		t.Fatal("empty words were accepted")
	}
	long := strings.Repeat("a", MaxWords+1)
	if WordsProblem(long) != "long" || utf8.RuneCountInString(strings.Repeat("á", MaxWords)) != MaxWords {
		t.Fatal("length check")
	}
	if WordsProblem(strings.Repeat("á", MaxWords)) != "" {
		t.Fatal("300 runes were refused")
	}
	if WordsProblem("go\nnow") != "control" || WordsProblem("go\u2028now") != "control" || WordsProblem("go\u200bnow") != "control" {
		t.Fatal("control or format characters were accepted")
	}
}

func TestSecretShapeSeesUnicodeSpaces(t *testing.T) {
	words := "Bearer\u00a0abcdefghijklmnop"
	if got := SecretShape(words); got != "a bearer credential" {
		t.Fatalf("NFKC space: %q", got)
	}
}

func TestSecretShapes(t *testing.T) {
	cases := []struct {
		words string
		want  string
	}{
		{"use ghp_" + strings.Repeat("a", 20), "a GitHub token"},
		{"use github_pat_" + strings.Repeat("a", 20), "a GitHub token"},
		{"-----BEGIN PRIVATE KEY-----", "a private key"},
		{"eyJabcdefgh.abcdefgh.", "a JSON web token"},
		{"https://user:pass@example.com/a", "a URL with credentials"},
		{"Bearer abcdefghijkl", "a bearer credential"},
		{"password=hunter", "a credential assignment"},
		{"xoxb-" + strings.Repeat("a", 10), "a Slack token"},
		{"AKIA1234567890ABCDEF", "an AWS key"},
		{"call 10.1.2.3 now", "a private network address"},
		{"call fd12::1 now", "a private network address"},
		{"host api.svc today", "an in-cluster name"},
		{"host x.cluster.local today", "an in-cluster name"},
		{"opapi-" + strings.Repeat("a", 16), "an OpenProject token"},
		{"raise workbench please", ""},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		got := SecretShape(tc.words)
		if got != tc.want {
			t.Errorf("%q: got %q want %q", tc.words, got, tc.want)
		}
		if tc.want != "" {
			seen[tc.want] = true
		}
	}
	for _, name := range SecretShapeNames() {
		if !seen[name] {
			t.Errorf("shape %q had no example", name)
		}
	}
}
