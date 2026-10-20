package keeper

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/internal/claudelogin"
)

// NB-01: the workbench asks the keeper's status over the login helper. The
// keeper adds two times to the answer, and only times.
func TestClaudeLoginStatusReportsExpiryOnly(t *testing.T) {
	f := newLoginFix(t)
	ctx := context.Background()
	r, err := claudelogin.Control(ctx, f.dir, "status", "")
	if err != nil || r.Code != "NeedsLogin" || r.AccessExpiresAt != nil || r.LoginExpiresAt != nil {
		t.Fatalf("no login: %+v %v", r, err)
	}
	f.seed(t, syntheticClaudeRecord(1))
	f.tick()
	r, err = claudelogin.Control(ctx, f.dir, "status", "")
	if err != nil || r.Code != "Ready" || r.Generation != 1 {
		t.Fatalf("ready: %+v %v", r, err)
	}
	if r.AccessExpiresAt == nil || !r.AccessExpiresAt.Equal(claudeNow.Add(8*time.Hour)) || r.LoginExpiresAt == nil || !r.LoginExpiresAt.Equal(claudeNow.Add(30*24*time.Hour)) {
		t.Fatalf("expiry = %v / %v", r.AccessExpiresAt, r.LoginExpiresAt)
	}
	raw, _ := json.Marshal(r)
	f.assertNoSecrets(t, string(raw))
	// A provider that gave no login expiry reports the access expiry alone.
	rec := syntheticClaudeRecord(2)
	rec.RefreshExpiresAt = time.Time{}
	f.seed(t, rec)
	if r, _ = claudelogin.Control(ctx, f.dir, "status", ""); r.AccessExpiresAt == nil || r.LoginExpiresAt != nil {
		t.Fatalf("no login expiry from the provider: %+v", r)
	}
}

// lockedWriter lets a test watch a helper's output while it runs.
type lockedWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// NB-01: a ceremony that is started and then abandoned (the operator closes the
// terminal; the helper's context ends) changes nothing: not the journal, not the
// published projection. The attempt is cancelled, its staging wiped, and a new
// ceremony may begin.
func TestClaudeLoginAbandonedCeremonyLeavesJournalAndProjectionAlone(t *testing.T) {
	f := newLoginFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.tick()
	snapshot := func() (string, string, uint64) {
		var journal, live corev1.Secret
		if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: "system", Name: DefaultClaudeJournalSecret}, &journal); err != nil {
			t.Fatal(err)
		}
		if err := f.c.Get(context.Background(), f.live, &live); err != nil {
			t.Fatal(err)
		}
		return journal.ResourceVersion, live.ResourceVersion, f.liveGeneration(t)
	}
	j0, l0, g0 := snapshot()
	before := f.journal(t)

	bin, _ := fakeClaude(t, "ok", loginCredentials(t, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedWriter{}
	// The terminal never types a code: the stdin never delivers one. An *os.File,
	// as the exec terminal is, so the CLI gets the descriptor itself.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pw.Close(); _ = pr.Close() }()
	done := make(chan claudelogin.Response, 1)
	go func() {
		res, _ := claudelogin.Helper(ctx, f.dir, claudelogin.Options{Bin: bin}, pr, out)
		done <- res
	}()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(out.String(), "signInUrl") {
		select {
		case <-deadline:
			t.Fatal("no sign-in link shown")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel() // abandoned: the terminal went away
	var res claudelogin.Response
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the helper did not end after its terminal went away")
	}
	if res.OK || res.Code == "Adopted" {
		t.Fatalf("an abandoned ceremony adopted: %+v", res)
	}
	j1, l1, g1 := snapshot()
	if j1 != j0 || l1 != l0 || g1 != g0 {
		t.Fatalf("journal or projection written: journal %s->%s, projection %s->%s, generation %d->%d", j0, j1, l0, l1, g0, g1)
	}
	after := f.journal(t)
	if after.Access.Generation != before.Access.Generation || after.Stage != before.Stage || after.Refresh.Reveal() != before.Refresh.Reveal() {
		t.Fatalf("journal record changed: %+v", after.Access)
	}
	if left := f.leftovers(t); len(left) != 0 {
		t.Fatalf("abandoned ceremony left staging: %v", left)
	}
	if r, _ := claudelogin.Control(context.Background(), f.dir, "begin", ""); r.Code != "Started" {
		t.Fatalf("the abandoned attempt still blocks a new one: %+v", r)
	}
	f.assertNoSecrets(t, out.String())
}
