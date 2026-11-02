package usagecollector

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"golang.org/x/sys/unix"
)

func TestClaudeCallbacksSanitizeBoundRetentionAndPreserveClocks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)
	raw := []byte(`{"transcript_path":"SECRET-PRIVATE-TRANSCRIPT","workspace":{"current_dir":"SECRET-WORKSPACE"},"rate_limits":{"five_hour":{"used_percentage":0,"resets_at":1793649600},"seven_day":null,"spend_limit":{"used_usd":123}}}`)
	for i := 0; i < 3; i++ {
		if err := RecordClaude(context.Background(), dir, "review-claude", "invocation-1", raw, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, ClaudeFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("SECRET")) || bytes.Contains(body, []byte("spend_limit")) {
		t.Fatal("private or unrelated field retained")
	}
	var j journal
	if json.Unmarshal(body, &j) != nil || len(j.Captures) != 2 || j.Captures[0].Sequence != 2 || j.Captures[1].Sequence != 3 {
		t.Fatalf("retention %s", body)
	}
	trust := providerallowance.AggregateTrust{MaxAge: 1800, Registry: providerallowance.NewStaticRegistry(providerallowance.RegistryParts{})}
	doc, err := providerallowance.AssembleCaptureBundle(body, trust, now.Add(3*time.Second))
	if err != nil || len(doc.Items) != 1 {
		t.Fatalf("assembler %v", err)
	}
	obs := doc.Items[0].Observation
	if obs.ProviderObservedAt != nil || obs.CapturedAt != now.Add(2*time.Second).Format(time.RFC3339Nano) || obs.Measures[0].Value.Decimal == nil || *obs.Measures[0].Value.Decimal != "0" || obs.Measures[1].Value.State != providerallowance.ValueUnknown {
		t.Fatalf("clocks or null/zero lost: %+v", obs)
	}
	info, _ := os.Stat(filepath.Join(dir, ClaudeFile))
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe artifact mode")
	}
	original := append([]byte(nil), body...)
	for _, invalid := range []string{`{"rate_limits":{"five_hour":{"used_percentage":999}}}`, `{"rate_limits":{},"rate_limits":{}}`, `{"rate_limits":{"five_hour":{"used_percentage":1,"used_percentage":2}}}`, strings.Repeat(" ", providerallowance.FixtureParseLimits.MaxBytes+1)} {
		if RecordClaude(context.Background(), dir, "review-claude", "invocation-1", []byte(invalid), now.Add(time.Minute)) == nil {
			t.Fatal("invalid callback accepted")
		}
		got, _ := os.ReadFile(filepath.Join(dir, ClaudeFile))
		if !bytes.Equal(got, original) {
			t.Fatal("invalid callback refreshed previous artifact")
		}
	}
	if RecordClaude(context.Background(), dir, "review-claude", "different-invocation", raw, now.Add(time.Minute)) == nil {
		t.Fatal("source incarnation changed silently")
	}
	if RecordClaude(context.Background(), dir, "review-claude", "invocation-1", raw, now) == nil {
		t.Fatal("clock rollback accepted")
	}
}

func TestClaudeMissingWindowsAndUnsafeState(t *testing.T) {
	now := time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	if RecordClaude(context.Background(), dir, "claude", "i-1", []byte(`{"model":{"id":"not-retained"}}`), now) != nil {
		t.Fatal("absent windows refused")
	}
	body, _ := os.ReadFile(filepath.Join(dir, ClaudeFile))
	if !bytes.Contains(body, []byte(`"five_hour":null`)) || !bytes.Contains(body, []byte(`"seven_day":null`)) {
		t.Fatal("absent windows invented")
	}
	if err := os.WriteFile(filepath.Join(dir, ClaudeFile), []byte(`malformed-secret`), 0600); err != nil {
		t.Fatal(err)
	}
	if RecordClaude(context.Background(), dir, "claude", "i-1", []byte(`{}`), now.Add(time.Minute)) == nil {
		t.Fatal("corrupt journal overwritten")
	}
	_ = os.Remove(filepath.Join(dir, ClaudeFile))
	outside := filepath.Join(t.TempDir(), "outside")
	_ = os.WriteFile(outside, []byte("private"), 0600)
	if err := os.Symlink(outside, filepath.Join(dir, ClaudeFile)); err != nil {
		t.Fatal(err)
	}
	if RecordClaude(context.Background(), dir, "claude", "i-1", []byte(`{}`), now) == nil {
		t.Fatal("symlink accepted")
	}
	got, _ := os.ReadFile(outside)
	if string(got) != "private" {
		t.Fatal("symlink target changed")
	}
	_ = os.Remove(filepath.Join(dir, ClaudeFile))
	_ = os.Chmod(dir, 0755)
	if RecordClaude(context.Background(), dir, "claude", "i-1", []byte(`{}`), now) == nil {
		t.Fatal("public directory accepted")
	}
}

func TestClaudeLockCancellationDoesNotBlockOrWrite(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	lock, err := os.OpenFile(filepath.Join(dir, "claude-captures.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if RecordClaude(ctx, dir, "claude", "i-1", []byte(`{}`), time.Now()) == nil {
		t.Fatal("canceled callback accepted")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if RecordClaude(ctx, dir, "claude", "i-1", []byte(`{}`), time.Now()) == nil {
		t.Fatal("contended lock accepted")
	}
	if _, err = os.Stat(filepath.Join(dir, ClaudeFile)); !os.IsNotExist(err) {
		t.Fatal("contended callback wrote artifact")
	}
}
