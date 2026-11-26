//go:build linux

package allowancecollector

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/internal/usagecollector"
)

// Exercise the fixed delivery bootstrap using a test-built ELF. The binary is
// anonymous, digest-checked and sealed.
func TestAnonymousBootstrapExecutesExactVerifiedELFAndRejectsDigestMismatch(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	binary := buildBootstrapFixture(t)
	for _, valid := range []bool{true, false} {
		sha := digest(binary)
		if !valid {
			sha = "0000000000000000000000000000000000000000000000000000000000000000"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-c", bootstrap, strconv.Itoa(len(binary)), sha)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		cmd.Stdin = io.MultiReader(bytes.NewReader(binary), bytes.NewReader([]byte("unconsumed request")))
		out, err := cmd.Output()
		cancel()
		if valid && (err != nil || string(out) != "internal-allowance-capture\n") {
			t.Fatal("sealed anonymous execution failed", err)
		}
		if !valid && (err == nil || len(out) != 0) {
			t.Fatal("wrong digest executed")
		}
	}
}

func buildBootstrapFixture(t *testing.T) []byte {
	t.Helper()
	// The bootstrap sets argv[0] to agent-run. A multicall /bin/echo would
	// interpret that name as the command to dispatch.
	const source = `package main

import "os"

func main() {
	os.Stdout.WriteString(os.Args[1] + "\n")
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "echo-argument")
	cmd := exec.Command("go", "build", "-p", "1", "-o", fixture, path)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "GOENV=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOMAXPROCS=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build bootstrap fixture: %v\n%s", err, out)
	}
	binary, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

type diagnosticExecutor struct {
	stderr string
}

func (e diagnosticExecutor) Run(_ context.Context, _, _, _ string, _ []string, _ io.Reader, out, diagnostic io.Writer) error {
	if _, err := io.WriteString(out, "unchanged-machine-receipt"); err != nil {
		return err
	}
	_, err := io.WriteString(diagnostic, e.stderr)
	return err
}

func TestRemoteLogsOnlyClosedDiagnosticsAndKeepsMachineOutput(t *testing.T) {
	good := "{\"schema\":\"dev-env.allowance-diagnostic.v1\",\"provider\":\"anthropic\",\"stage\":\"version\",\"reason\":\"version_unqualified\",\"clientVersion\":\"2.1.312\"}\n"
	for _, test := range []struct {
		stderr  string
		wantLog bool
		wantErr bool
	}{
		{good, true, false},
		{"private-provider-error\n", false, false},
		{good + "private-provider-error\n", false, false},
		{strings.Replace(good, "2.1.312", "private-provider-error", 1), false, false},
		{strings.Repeat("private-provider-error", usagecollector.DiagnosticMaxBytes), false, false},
	} {
		var lines []string
		logger := funcr.New(func(prefix, args string) { lines = append(lines, prefix+args) }, funcr.Options{})
		ctx := log.IntoContext(context.Background(), logger)
		remote := &podRemote{exec: diagnosticExecutor{test.stderr}, binary: []byte("public-fixture"), sha: strings.Repeat("a", 64)}
		out, err := remote.Run(ctx, Host{Name: "owned-fixture"}, usagecollector.RemoteRequest{Version: 1, Cycle: 7})
		if (err != nil) != test.wantErr || err == nil && string(out) != "unchanged-machine-receipt" {
			t.Fatal("diagnostic side channel changed machine output/error contract", err)
		}
		if (len(lines) == 1) != test.wantLog || strings.Contains(strings.Join(lines, ""), "private-provider-error") {
			t.Fatal("unknown/provider stderr reached operator log")
		}
		if test.wantLog && !strings.Contains(lines[0], "version_unqualified") {
			t.Fatal("closed diagnostic missing")
		}
	}
}

func TestRemoteBufferLimitIncludesOptimizedStringAndCopyPaths(t *testing.T) {
	for _, write := range []func(io.Writer) error{
		func(w io.Writer) error { _, err := io.WriteString(w, "longer-than-limit"); return err },
		func(w io.Writer) error { _, err := io.Copy(w, strings.NewReader("longer-than-limit")); return err },
		func(w io.Writer) error {
			_, err := io.Copy(w, io.LimitReader(strings.NewReader("longer-than-limit"), 17))
			return err
		},
	} {
		buf := &boundedBuffer{max: 8}
		if err := write(buf); err == nil || !buf.failed || len(buf.Bytes()) > 8 {
			t.Fatal("optimized IO bypassed remote byte limit")
		}
	}
}

func TestDiagnosticOverflowConsumesLaterWritesWithoutChangingTransport(t *testing.T) {
	buf := &boundedBuffer{max: 8, discardOverflow: true}
	for _, raw := range []string{"prefix", "too-long", "later-frame"} {
		if n, err := io.WriteString(buf, raw); n != len(raw) || err != nil {
			t.Fatal("optional diagnostics failed transport", n, err)
		}
	}
	if !buf.failed || string(buf.Bytes()) != "prefix" {
		t.Fatal("overflow was not sticky and bounded")
	}
}
