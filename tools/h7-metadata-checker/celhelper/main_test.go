package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFullGrammarComparison(t *testing.T) {
	cases := []struct {
		name, actual, expected string
		valid, equal           bool
	}{
		{"whitespace", "a\t ==\n b", "a == b", true, true},
		{"optional", "x.?field.orValue(0) == m[? 'key'].orValue(0)", "x .? field . orValue ( 0 ) == m [ ? 'key' ] .orValue(0)", true, true},
		{"comment retained", "a // same comment\n == b", "a // same comment\n== b", true, true},
		{"comment changed", "a // first\n == b", "a // second\n == b", true, false},
		{"raw string", `r'a b' == 'a b'`, `r'ab' == 'a b'`, true, false},
		{"triple string interior quote", `'''a' b''' == 'value'`, `'''a'b''' == 'value'`, true, false},
		{"triple multiline", "'''a\n b'''", "'''a\nb'''", true, false},
		{"bytes prefix", `b'abc'`, `B'abc'`, true, false},
		{"unicode literal bytes", `'é'`, `'é'`, true, false},
		{"escape bytes", `'\u0041'`, `'A'`, true, false},
		{"numeric lexemes", "1e3", "1000.0", true, false},
		{"hex lexemes", "0x10", "16", true, false},
		{"uint token type", "1u", "1", true, false},
		{"operator", "a == b", "a != b", true, false},
		{"identifier boundary", "ab == 1", "a == 1", true, false},
		{"quoted identifier", "x.`some-field` == 0", "x . `some-field` == 0", true, true},
		{"invalid numeric boundary", "1 e3", "1e3", false, false},
		{"invalid syntax", "a ==", "a ==", false, false},
		{"unrecognized character", "a @ b", "a @ b", false, false},
		{"nongrammar whitespace", "a\v==b", "a == b", false, false},
		{"unclosed string", "'abc", "'abc", false, false},
		{"invalid expected", "true", "a @ b", false, false},
		{"invalid escape", `'\q'`, `'\q'`, false, false},
		{"trailing expression", "true false", "true false", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := compare(c.actual, c.expected)
			if out.OK != c.valid || c.valid && (out.Equal == nil || *out.Equal != c.equal) {
				t.Fatalf("unexpected comparison outcome: valid=%v equal=%v", out.OK, out.Equal != nil && *out.Equal)
			}
		})
	}
}

func TestBoundedSanitizedProtocol(t *testing.T) {
	for _, in := range []string{
		`{"op":"compare","actual":"a @ fixture-private","expected":"true"}`,
		`{"op":"get","path":"fixture-private"}`,
		`{"op":"hello","unknown":"fixture-private"}`,
		`{"op":"hello"} {"op":"hello"}`,
		strings.Repeat("a", maxInputLine+1),
	} {
		var out bytes.Buffer
		if serve(strings.NewReader(in+"\n"), &out) || strings.Contains(out.String(), "fixture-private") || out.Len() > 256 {
			t.Fatal("invalid input accepted or reflected")
		}
	}
	var out bytes.Buffer
	if !serve(strings.NewReader("{\"op\":\"hello\"}\n{\"op\":\"compare\",\"actual\":\"true\",\"expected\":\"true\"}\n"), &out) {
		t.Fatal("valid offline protocol refused")
	}
	dec := json.NewDecoder(&out)
	var hello, compared reply
	if dec.Decode(&hello) != nil || dec.Decode(&compared) != nil || !hello.OK || !compared.OK || !*compared.Equal {
		t.Fatal("bad replies")
	}
	if compare(strings.Repeat("a", maxExpressionBytes+1), "true").OK {
		t.Fatal("oversized expression accepted")
	}
}
