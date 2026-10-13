package main

import (
	"context"
	"strings"
	"testing"
)

const syntheticConfig = `apiVersion: v1
kind: Config
current-context: haynes-haynes-ops-machine
contexts:
  - name: haynes-haynes-ops-machine
    context: {cluster: fixture, user: fixture}
clusters:
  - name: fixture
    cluster: {server: "https://fixture.invalid"}
users:
  - name: fixture
    user: {token: synthetic-token}
`

func TestStaticConfigAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
		valid                  bool
	}{
		{"static", "", "", true},
		{"wrong-context", "current-context: haynes-haynes-ops-machine", "current-context: other", false},
		{"plugin", "user: {token: synthetic-token}", "user: {exec: {command: private}}", false},
		{"extra-auth", "user: {token: synthetic-token}", "user: {token: synthetic-token, impersonate: private}", false},
		{"insecure", "cluster: {server: \"https://fixture.invalid\"}", "cluster: {server: \"https://fixture.invalid\", insecure-skip-tls-verify: true}", false},
		{"proxy", "cluster: {server: \"https://fixture.invalid\"}", "cluster: {server: \"https://fixture.invalid\", proxy-url: \"http://private.invalid\"}", false},
		{"ca-file", "cluster: {server: \"https://fixture.invalid\"}", "cluster: {server: \"https://fixture.invalid\", certificate-authority: private}", false},
		{"http", "https://fixture.invalid", "http://fixture.invalid", false},
		{"userinfo", "https://fixture.invalid", "https://private@fixture.invalid", false},
		{"path", "https://fixture.invalid", "https://fixture.invalid/private", false},
		{"query", "https://fixture.invalid", "https://fixture.invalid/?private=x", false},
		{"empty-query", "https://fixture.invalid", "https://fixture.invalid/?", false},
		{"fragment", "https://fixture.invalid", "https://fixture.invalid/#private", false},
		{"duplicate-user", "user: {token: synthetic-token}", "user: {token: synthetic-token}\n  - name: fixture\n    user: {token: other}", false},
		{"second-document", "user: {token: synthetic-token}", "user: {token: synthetic-token}\n---\nprivate: second-document", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := syntheticConfig
			if tc.old != "" {
				b = strings.Replace(b, tc.old, tc.replacement, 1)
			}
			a, code := parseConfig(context.Background(), []byte(b))
			if (a != nil && code == "") != tc.valid {
				t.Fatalf("config shape valid=%v code=%s", tc.valid, code)
			}
		})
	}
}
