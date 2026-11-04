package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// The real agent-run schedule client (S5a, internal/agentrun/schedule.go)
// over TLS into these handlers on the fake client: every command it has
// decodes the route's answer, and its refusals map the routes' statuses.
func TestScheduledWorkCLIRun(t *testing.T) {
	f := scheduledWorkFixture(t)
	owner := f.sessionPod("dev-env-1104-010332", "full", 0)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		f.h.ServeHTTP(w, r)
	}))
	defer server.Close()

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(token string, want int, args ...string) (string, string) {
		t.Helper()
		if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		env := agentrun.DefaultEnv()
		env.Stdout, env.Stderr = &stdout, &stderr
		env.Stdin = strings.NewReader("")
		env.Getenv = func(string) string { return "" }
		env.Now = func() time.Time { return f.now }
		env.SessionTokenFile = filepath.Join(dir, "absent-session")
		env.ServiceAccountDir = filepath.Join(dir, "absent-sa")
		env.LookPath = func(string) (string, error) { return "", errors.New("synthetic: no host command") }
		env.Run = func(context.Context, []string, io.Reader) (int, error) {
			return 0, errors.New("synthetic host process refused")
		}
		env.Output = func(context.Context, []string) ([]byte, error) {
			return nil, errors.New("synthetic host process refused")
		}
		env.ExecOutput = func(context.Context, []string) (int, string, string, error) {
			return 0, "", "", errors.New("synthetic host process refused")
		}
		env.PortForward = func(context.Context, []string) (string, func(), error) {
			return "", func() {}, errors.New("synthetic port-forward refused")
		}
		env.Sleep = func(context.Context, time.Duration) error {
			t.Errorf("schedule client retried: %v", args)
			return errors.New("synthetic retry refused")
		}
		env.NewKey = func() string { return "agent-run-synthetic" }
		args = append(append([]string{}, args...), "--api-url", server.URL, "--token-file", tokenPath, "--ca-file", caPath)
		if code := agentrun.Run(context.Background(), args, env); code != want {
			t.Fatalf("%v: exit %d want %d\nstdout %s\nstderr %s", args[:len(args)-6], code, want, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), token) {
			t.Fatal("the client printed its token")
		}
		return stdout.String(), stderr.String()
	}
	view := func(raw string) apiv1.ScheduledWork {
		t.Helper()
		var v apiv1.ScheduledWork
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
		return v
	}

	// create, as the owner session; the key makes a replay the same item.
	createArgs := []string{"schedule", "create", "Loki retention trend audit", "--due", "2026-10-20T09:00", "--tz", "America/New_York",
		"--not-after", "2026-12-31T00:00:00Z", "--mode", "headless", "--template", "ops-audit", "--repo", "haynes-ops",
		"--prompt", "Audit Loki retention.", "--repeat", "weekly:MO,TH", "--notify", "pushover:default", "--key", "loki-audit", "-o", "json"}
	stdout, _ := run(owner, agentrun.ExitOK, createArgs...)
	created := view(stdout)
	if created.Spec.Owner != sessionPrincipal(f, "dev-env-1104-010332") || created.Spec.Recurrence == nil || created.SpecHash == "" {
		t.Fatalf("create: %+v", created)
	}
	if again := view(func() string { s, _ := run(owner, agentrun.ExitOK, createArgs...); return s }()); again.Name != created.Name {
		t.Fatalf("a replayed create made %s, not %s", again.Name, created.Name)
	}
	stdout, _ = run(owner, agentrun.ExitOK, createArgs[:len(createArgs)-2]...)
	if !strings.Contains(stdout, "created "+created.Name) || !strings.Contains(stdout, "note: the owner is a session") {
		t.Errorf("create text: %s", stdout)
	}
	// A field refusal names the flag.
	_, stderr := run(owner, agentrun.ExitFailed, "schedule", "create", "x", "--due", "2026-10-20T09:00", "--tz", "UTC", "--not-after", "2026-12-31T00:00:00Z",
		"--mode", "headless", "--template", "ops-audit", "--repo", "haynes-ops", "--prompt", "p", "--notify", "pushover:ops")
	if !strings.Contains(stderr, "--notify (notify)") {
		t.Errorf("a refused notify: %s", stderr)
	}

	// list and show.
	stdout, _ = run(owner, agentrun.ExitOK, "schedule", "list", "-o", "json")
	var list apiv1.ScheduledWorkList
	if err := json.Unmarshal([]byte(stdout), &list); err != nil || len(list.Items) != 1 || list.Items[0].Name != created.Name {
		t.Fatalf("list %q: %v", stdout, err)
	}
	stdout, _ = run(owner, agentrun.ExitOK, "schedule", "list")
	if !strings.Contains(stdout, created.Name) || !strings.Contains(stdout, "weekly on MO,TH") {
		t.Errorf("list text: %s", stdout)
	}
	_, stderr = run(owner, agentrun.ExitAuth, "schedule", "list", "--all")
	if !strings.Contains(stderr, "list --all is for a human or a coordinator") {
		t.Errorf("list --all as a session: %s", stderr)
	}
	stdout, _ = run(owner, agentrun.ExitOK, "schedule", "show", created.Name)
	for _, want := range []string{created.SpecHash, "approval:   none", "Audit Loki retention."} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show text has no %q:\n%s", want, stdout)
		}
	}

	// edit, as the owner: the hash moves and the editor stays the owner.
	stdout, _ = run(owner, agentrun.ExitOK, "schedule", "edit", created.Name, "--title", "Loki retention, weekly", "-o", "json")
	edited := view(stdout)
	if edited.Spec.Title != "Loki retention, weekly" || edited.SpecHash == created.SpecHash || edited.Spec.Editor != created.Spec.Owner {
		t.Fatalf("edit: %+v", edited)
	}

	// approve: the session cannot; Tom can, with the hash he read.
	_, stderr = run(owner, agentrun.ExitAuth, "schedule", "approve", created.Name, "--spec-hash", edited.SpecHash, "--yes")
	if !strings.Contains(stderr, "approve and retry are for a human (Tom) only") {
		t.Errorf("approve as a session: %s", stderr)
	}
	stdout, _ = run(tokHuman, agentrun.ExitOK, "schedule", "approve", created.Name, "--spec-hash", edited.SpecHash, "--yes", "-o", "json")
	approved := view(stdout)
	if a := approved.Approval; a == nil || a.By != humanPrincipal || a.SpecHash != edited.SpecHash || a.TemplateDigest != f.templateDigest("ops-audit") {
		t.Fatalf("approve: %+v", approved.Approval)
	}
	// A hash the client read before an edit is refused before anything is sent.
	before := requests.Load()
	_, stderr = run(tokHuman, agentrun.ExitFailed, "schedule", "approve", created.Name, "--spec-hash", created.SpecHash, "--yes")
	if !strings.Contains(stderr, "changed after you saw it") || requests.Load() != before+1 {
		t.Errorf("a stale hash: %d requests, %s", requests.Load()-before, stderr)
	}

	// retry, of an occurrence the executor parked.
	at := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	f.setStatus(created.Name, func(st *v1alpha1.ScheduledWorkStatus) {
		st.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(at), State: v1alpha1.OccurrenceParked, Reason: "no standing authority"}}
	})
	stdout, _ = run(tokHuman, agentrun.ExitOK, "schedule", "retry", created.Name, "--at", at.Format(time.RFC3339), "--spec-hash", edited.SpecHash, "--yes", "-o", "json")
	retried := view(stdout)
	if len(retried.Occurrences) != 1 || retried.Occurrences[0].State != apiv1.ScheduledOccurrencePending || retried.Occurrences[0].Attempt != 1 {
		t.Fatalf("retry: %+v", retried.Occurrences)
	}
	// The same retry again is refused: it is no longer Parked.
	f.setStatus(created.Name, func(st *v1alpha1.ScheduledWorkStatus) {
		st.Occurrences[0].State = v1alpha1.OccurrenceParked
		st.Occurrences[0].Attempt = v1alpha1.ScheduledWorkMaxAttempt
	})
	_, stderr = run(tokHuman, agentrun.ExitFailed, "schedule", "retry", created.Name, "--at", at.Format(time.RFC3339), "--spec-hash", edited.SpecHash, "--yes")
	if !strings.Contains(stderr, "used its 3 retries") {
		t.Errorf("a fourth retry: %s", stderr)
	}

	// cancel, then a second cancel, then show the cancelled item.
	stdout, _ = run(owner, agentrun.ExitOK, "schedule", "cancel", created.Name, "-o", "json")
	if c := view(stdout); c.State != apiv1.ScheduledWorkCancelled || !c.Spec.Cancelled {
		t.Fatalf("cancel: %+v", c)
	}
	stdout, _ = run(owner, agentrun.ExitOK, "schedule", "cancel", created.Name)
	if !strings.Contains(stdout, "cancelled "+created.Name) {
		t.Errorf("a second cancel: %s", stdout)
	}
	_, stderr = run(owner, agentrun.ExitFailed, "schedule", "edit", created.Name, "--title", "x")
	if !strings.Contains(stderr, "cancelled") {
		t.Errorf("edit of a cancelled item: %s", stderr)
	}
	// Another session cannot see it.
	other := f.sessionPod("s-other", "full", 0)
	run(other, agentrun.ExitNotFound, "schedule", "show", created.Name)
}
