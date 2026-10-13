package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestCanonicalJSONSortsKeysAndEscapesNoHTML(t *testing.T) {
	got, err := CanonicalJSON(struct {
		Z string    `json:"z"`
		A string    `json:"a"`
		N int64     `json:"n"`
		T time.Time `json:"t"`
	}{Z: "<&>", A: "x", N: 12345678901234, T: time.Date(2026, 10, 12, 1, 2, 3, 4, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":"x","n":12345678901234,"t":"2026-10-12T01:02:03.000000004Z","z":"<&>"}`
	if string(got) != want {
		t.Errorf("canonical JSON:\n%s\nwant\n%s", got, want)
	}
	if HistoryBlocksSHA256(nil) != HistoryBlocksSHA256([]string{}) {
		t.Errorf("an empty block list has two digests")
	}
}

func TestIsCredentialName(t *testing.T) {
	for _, n := range []string{"auth.json", "AUTH.JSON", ".credentials.json", ".credentials.json.tmp.e5023036", "auth.json.bak",
		"credentials.json", "access.json", ".git-credentials", ".netrc", "kubeconfig", "gh_token", "hosts.yml", "dev-env-hw",
		".env", ".env.local", "id_rsa", "id_ed25519.pub", "id_ecdsa", "server.pem", "tls.key", "cert.p12", "x.PFX"} {
		if !IsCredentialName(n) {
			t.Errorf("%s is not a credential's name", n)
		}
	}
	for _, n := range []string{"history.jsonl", "rollout-x.jsonl", "auth.jsonl", "my.env", "environment", "keys.json", "state_5.sqlite"} {
		if IsCredentialName(n) {
			t.Errorf("%s is a credential's name", n)
		}
	}
}

func TestValidRescueStamp(t *testing.T) {
	for s, want := range map[string]bool{"20261012-0102": true, "20261012-0102-2": false, "2026101-0102": false, "../x": false, "": false} {
		if ValidRescueStamp(s) != want {
			t.Errorf("%q: %v, want %v", s, !want, want)
		}
	}
}

func TestCheckHistoryPath(t *testing.T) {
	for p, want := range map[string]string{
		"projects/-home-dev/t.jsonl":           "",
		"":                                     HistoryErrInvalid,
		"/etc/passwd":                          HistoryErrInvalid,
		"a/../b":                               HistoryErrInvalid,
		"a//b":                                 HistoryErrInvalid,
		"./a":                                  HistoryErrInvalid,
		`a\b`:                                  HistoryErrInvalid,
		strings.Repeat("a/", 64) + "f":         HistoryErrInvalid,
		strings.Repeat("x", 1025):              HistoryErrInvalid,
		"sessions/2026/auth.json":              HistoryErrCredentialInTree,
		".env/x":                               HistoryErrCredentialInTree,
		"projects/p/.credentials.json.tmp.abc": HistoryErrCredentialInTree,
		"sessions/rollout-\xff.jsonl":          HistoryErrInvalid,
		"sessions/r\u00e9sum\u00e9.jsonl":      "",
	} {
		if got, _ := CheckHistoryPath(p); got != want {
			t.Errorf("%q: %q, want %q", p, got, want)
		}
	}
}
