package main

import (
	"bytes"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentrun"
)

// versionLine matches "agent-run <version> (commit <commit>, <go>, <os>/<arch>)".
var versionLine = regexp.MustCompile(`^agent-run \S+ \(commit \S+, go\S+, [a-z0-9]+/[a-z0-9]+\)\n$`)

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{arg}, &stdout, &stderr)

			if code != agentrun.ExitOK {
				t.Fatalf("exit code = %d, want %d (stderr: %q)", code, agentrun.ExitOK, stderr.String())
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
	if code := run([]string{"version", "--json"}, &stdout, &stderr); code != agentrun.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, agentrun.ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "version takes no arguments") {
		t.Errorf("stderr = %q, want the argument error", stderr.String())
	}
}

// The commands themselves are tested in internal/agentrun; this checks that
// main hands the arguments and streams through.
func TestUsage(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout bool // usage on stdout (asked for) rather than stderr (an error)
	}{
		{name: "no arguments", args: nil, wantCode: agentrun.ExitUsage},
		{name: "help", args: []string{"help"}, wantCode: agentrun.ExitOK, wantStdout: true},
		{name: "--help", args: []string{"--help"}, wantCode: agentrun.ExitOK, wantStdout: true},
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
			if !strings.Contains(usageOut.String(), "agent-run [--repo] <repo> -p") {
				t.Errorf("usage missing from the expected stream; got %q", usageOut.String())
			}
			if other.Len() != 0 {
				t.Errorf("unexpected output on the other stream: %q", other.String())
			}
		})
	}
}
