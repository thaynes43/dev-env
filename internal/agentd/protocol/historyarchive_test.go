package protocol

import (
	"bytes"
	"encoding/json"
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

// canonicalJSONGeneric is CanonicalJSON as it was through 2.26: encode, decode
// into generic values with UseNumber, encode again. Its bytes are the
// contract; CanonicalJSON now streams instead (DESIGN-025 5.4), and must give
// the same bytes for every input, or an operator and an agentd of different
// releases would disagree on a digest.
func canonicalJSONGeneric(v any) ([]byte, error) {
	var first bytes.Buffer
	enc := json.NewEncoder(&first)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(&first)
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc = json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// canonicalCorpus is JSON that exercises every rule of the generic encoding:
// key order (bytes, not runes or case), duplicate keys (the last wins),
// escapes, HTML characters, U+2028, invalid UTF-8, numbers as written, empty
// containers, null and deep nesting.
var canonicalCorpus = []string{
	`{}`, `[]`, `null`, `true`, `false`, `0`, `-0`, `1.50`, `1e10`, `-1.25E-3`, `12345678901234567890123`,
	`""`, `"<&>"`, `"  "`, `"tab\there\nnew \"quoted\" back\\slash"`, `"\u0000\u001f\u007f"`, "\"\xff\xfe invalid\"",
	`{"z":1,"a":2,"M":3,"_":4,"~":5,"é":6,"e":7,"":8}`,
	`{"a":1,"b":2,"a":3}`, `{"a":{"y":1,"x":2},"a":[3]}`,
	`{"files":[{"path":"a/b","size":3,"blocks":["ff","00"]},{"path":"<c>","size":0,"blocks":[]}],"bytes":3,"format":"x"}`,
	`[[[[[[{"k":[{"j":null}]}]]]]]]`, `[1,"two",{"three":3},[4],null,true]`,
	`{"é":1,"é":2}`, `{"key with \"quotes\"":"v","key\\back":"w"}`,
}

func TestCanonicalJSONMatchesTheGenericEncoding(t *testing.T) {
	for _, raw := range canonicalCorpus {
		checkCanonicalAgainstGeneric(t, json.RawMessage(raw))
	}
	at := time.Date(2026, 10, 12, 1, 2, 3, 4, time.UTC)
	plan := HistoryExportPlan{Format: HistoryExportPlanFormat, Session: "s-1",
		Roots:    []HistoryRoot{{Name: HistoryRootClaude, Dir: ".claude"}, {Name: HistoryRootCodex, Dir: ".codex", Absent: true}},
		Files:    []HistoryFile{{Root: HistoryRootClaude, Path: "projects/-home-dev/<x>&.jsonl", Bytes: 3, Mode: 0o600, ModTime: at, SHA256: strings.Repeat("ab", 32), Blocks: []string{strings.Repeat("cd", 32)}}},
		Excluded: []HistoryExcluded{{Root: HistoryRootClaude, Path: ".credentials.json", Class: HistoryClassCredential}, {Root: HistoryRootClaude, Path: "file-history", Class: HistoryClassUserCopy, Bytes: 9, Partial: true}},
		Bytes:    3}
	content := HistoryContent{Format: HistoryArchiveFormat, Session: HistorySession{Namespace: "dev-agents", Name: "s-1", UID: "u"}, Provider: HistoryProviderClaude,
		Terminal: HistoryTerminal{PodUID: "p", Phase: "Failed", ObservedAt: at}, Rescue: HistoryRescue{Stamp: "20261012-0102", Result: HistoryRescueCleanAndPushed}}
	HistoryContentFromPlan(&content, plan, strings.Repeat("ef", 32))
	for _, v := range []any{plan, content, HistoryError{Code: HistoryErrInvalid, Message: "a <message>"}, []string{}, map[string]any{"b": []any{}, "a": map[string]any{}}} {
		checkCanonicalAgainstGeneric(t, v)
	}
}

// FuzzCanonicalJSONMatchesTheGenericEncoding runs its seeds (the corpus) with
// every `go test`; `go test -fuzz` explores further, off the shared nodes.
func FuzzCanonicalJSONMatchesTheGenericEncoding(f *testing.F) {
	for _, raw := range canonicalCorpus {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if !json.Valid(data) {
			return
		}
		checkCanonicalAgainstGeneric(t, json.RawMessage(data))
	})
}

func checkCanonicalAgainstGeneric(t *testing.T, v any) {
	t.Helper()
	want, wantErr := canonicalJSONGeneric(v)
	got, err := CanonicalJSON(v)
	if (err != nil) != (wantErr != nil) || !bytes.Equal(got, want) {
		t.Errorf("CanonicalJSON(%v) = %q, %v; the generic encoding gives %q, %v", v, got, err, want, wantErr)
	}
}
