package main

import (
	"bytes"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// versionLine matches "agent-run <version> (commit <commit>, <go>, <os>/<arch>)".
var versionLine = regexp.MustCompile(`^agent-run \S+ \(commit \S+, go\S+, [a-z0-9]+/[a-z0-9]+\)\n$`)

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{arg}, &stdout, &stderr)

			if code != exitOK {
				t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
			out := stdout.String()
			if !versionLine.MatchString(out) {
				t.Errorf("stdout = %q, want a line matching %s", out, versionLine)
			}
			if !strings.Contains(out, runtime.Version()) || !strings.Contains(out, runtime.GOOS+"/"+runtime.GOARCH) {
				t.Errorf("stdout = %q, want the Go version %s and platform %s/%s", out, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			}
		})
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version", "--json"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "version takes no arguments") {
		t.Errorf("stderr = %q, want the argument error", stderr.String())
	}
}

func TestUsage(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout bool // usage on stdout (asked for) rather than stderr (an error)
		wantInErr  string
	}{
		{name: "no arguments", args: nil, wantCode: exitUsage},
		{name: "help", args: []string{"help"}, wantCode: exitOK, wantStdout: true},
		{name: "--help", args: []string{"--help"}, wantCode: exitOK, wantStdout: true},
		{name: "a verb not built yet", args: []string{"list"}, wantCode: exitUsage, wantInErr: `unknown command "list"`},
		{name: "a v1 flag not built yet", args: []string{"-p", "fix the docs"}, wantCode: exitUsage, wantInErr: `unknown command "-p"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d", code, tt.wantCode)
			}

			usageOut, other := &stderr, &stdout
			if tt.wantStdout {
				usageOut, other = &stdout, &stderr
			}
			if !strings.Contains(usageOut.String(), "Usage: agent-run <command>") {
				t.Errorf("usage missing from the expected stream; got %q", usageOut.String())
			}
			if other.Len() != 0 {
				t.Errorf("unexpected output on the other stream: %q", other.String())
			}
			if tt.wantInErr != "" && !strings.Contains(stderr.String(), tt.wantInErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantInErr)
			}
		})
	}
}
