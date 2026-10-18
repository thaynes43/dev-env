package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

// Exercise the shipped CLI through TLS, real caller policy and retained ledger.
// No native inspector/provider process is installed: campaign creation is not admission.
func TestBudgetCLIAuthenticatedRetainedWorkflow(t *testing.T) {
	f := coordinatorFixture(t)
	f.srv.AssignedTaskBudgets = map[string]string{"host-a": "campaign-a"}
	store := taskbudget.KubeStore{Client: f.c, Live: f.c, Namespace: "budget-system"}
	f.srv.TaskBudgets = &taskbudget.Service{Store: store, Now: func() time.Time { return f.now }}
	var paths []string
	var mu sync.Mutex
	handler := f.srv.Handler()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	setToken := func(value string) {
		t.Helper()
		if err := os.WriteFile(token, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	setToken(tokCoordinator)
	run := func(want int, args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		env := agentrun.DefaultEnv()
		env.Stdout = &stdout
		env.Stderr = &stderr
		env.Getenv = func(string) string { return "" }
		env.SessionTokenFile = filepath.Join(dir, "absent-session")
		env.ServiceAccountDir = filepath.Join(dir, "absent-sa")
		args = append(args, "--api-url", server.URL, "--token-file", token, "--ca-file", ca)
		if code := agentrun.Run(context.Background(), args, env); code != want {
			t.Fatalf("%v: exit=%d want=%d stdout=%s stderr=%s", args, code, want, &stdout, &stderr)
		}
		return stdout.String()
	}
	// A different authenticated caller and a mismatched assignment cannot create.
	setToken(tokClient)
	run(agentrun.ExitUnconfirmed, "budget", "create", "campaign-a", "--success", "result")
	setToken(tokCoordinator)
	run(agentrun.ExitAuth, "budget", "create", "other-campaign", "--success", "result")
	raw := run(agentrun.ExitOK, "budget", "create", "campaign-a", "--success", "result", "--overall", "2m", "--effort", "3m", "--checkpoint", "30s", "-o", "json")
	var original apiv1.TaskBudgetStatus
	if err := json.Unmarshal([]byte(raw), &original); err != nil {
		t.Fatal(err)
	}
	if original.Admitted || original.Binding.HostID != "host-a" || original.Binding.Epoch != 1 || !original.Binding.Deadline.Equal(f.now.Add(2*time.Minute)) {
		t.Fatalf("unexpected initial receipt: %+v", original)
	}
	// A repeated create conflicts and cannot reset the original limits.
	run(agentrun.ExitFailed, "budget", "create", "campaign-a", "--success", "replacement")
	mu.Lock()
	f.now = f.now.Add(30 * time.Second)
	// Reconstruct the service to prove that observation uses retained authority.
	f.srv.TaskBudgets = &taskbudget.Service{Store: store, Now: func() time.Time { return f.now }}
	mu.Unlock()
	raw = run(agentrun.ExitOK, "budget", "show", "campaign-a", "-o", "json")
	var stopped apiv1.TaskBudgetStatus
	if err := json.Unmarshal([]byte(raw), &stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.Binding != original.Binding || !stopped.Latched || stopped.Reason != "Checkpoint" || stopped.Admitted {
		t.Fatalf("retained latch missing: %+v", stopped)
	}
	mu.Lock()
	defer mu.Unlock()
	l, _, err := store.Read(context.Background(), "campaign-a")
	if err != nil || !l.Latched || len(l.Workers) != 0 || l.Spec.SuccessCondition != "result" {
		t.Fatalf("ledger reset or admitted: %+v err=%v", l, err)
	}
	for _, p := range paths {
		if p != "POST /v1/task-budgets" && p != "GET /v1/task-budgets/campaign-a" {
			t.Fatalf("unexpected authority route %s", p)
		}
	}
	t.Log("CLI -> authenticated TLS API -> retained ledger: custom finite bounds, caller refusal, duplicate refusal, restart-safe checkpoint latch; zero native/provider actors")
}
