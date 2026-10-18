package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
)

func TestOwnedRootBootstrapIntentPersistsAndRefusesAliasOrReplay(t *testing.T) {
	state := t.TempDir()
	if os.Chmod(state, 0700) != nil {
		t.Fatal("private state")
	}
	if err := writeOwnedBootstrapIntent(state, "campaign"); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnedBootstrapIntent(state, "campaign"); err == nil {
		t.Fatal("attempt rearmed")
	}
	raw, err := os.ReadFile(filepath.Join(state, "owned-budget-attempt"))
	if err != nil || string(raw) != "campaign" {
		t.Fatal("intent lost", err)
	}
	outside := t.TempDir()
	alias := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnedBootstrapIntent(alias, "campaign"); err == nil {
		t.Fatal("aliased state accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "owned-budget-attempt")); !os.IsNotExist(err) {
		t.Fatal("alias wrote outside original state")
	}
}

func TestOwnedRootHomePreflightBeforeIrreversibleIntent(t *testing.T) {
	home := t.TempDir()
	if os.Mkdir(filepath.Join(home, ".codex"), 0700) != nil {
		t.Fatal("native home")
	}
	file := filepath.Join(home, ".codex", "recovery.json")
	if os.WriteFile(file, []byte("approved"), 0600) != nil {
		t.Fatal("recovery")
	}
	baseline, err := hostexecutor.NativeHomeBaseline(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOwnedRootHome(home, "home-original", baseline); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("unapproved"), 0600); err != nil {
		t.Fatal(err)
	}
	if validateOwnedRootHome(home, "home-original", baseline) == nil {
		t.Fatal("drifted baseline")
	}
	if validateOwnedRootHome(home, "", baseline) == nil {
		t.Fatal("missing home authority")
	}
	if _, err := os.Stat(filepath.Join(home, ".agentd", "owned-budget-attempt")); !os.IsNotExist(err) {
		t.Fatal("preflight consumed intent")
	}
}

// Hosted CI invokes this as nonroot PID1; ordinary local suites do not pretend
// that a callback bypasses the production kernel/prelaunch prerequisites.
func TestOwnedRootBootstrapPID1(t *testing.T) {
	if os.Getpid() != 1 {
		if os.Getenv("AGENTD_TEST_REQUIRE_PID1") == "true" {
			t.Fatal("bootstrap fixture requires PID1")
		}
		t.Skip("requires hosted bounded PID namespace")
	}
	if os.Geteuid() != 1000 {
		t.Fatal("bootstrap fixture requires nonroot1000")
	}
	home := t.TempDir()
	state := filepath.Join(home, ".agentd")
	if os.Mkdir(state, 0700) != nil || os.Mkdir(filepath.Join(home, ".codex"), 0700) != nil {
		t.Fatal("private directories")
	}
	var calls atomic.Int32
	spec := apiv1.TaskBudgetSpec{SuccessCondition: "inert bootstrap ordering", OverallSeconds: 120, EffortSeconds: 180, CheckpointSeconds: 30}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if _, err := os.Stat(filepath.Join(state, "owned-budget-attempt")); err != nil {
			t.Error("API create preceded durable intent")
		}
		var req apiv1.CreateTaskBudgetRequest
		if r.Method != http.MethodPost || r.URL.Path != apiv1.TaskBudgetsPath || json.NewDecoder(r.Body).Decode(&req) != nil || req.TaskUID != "campaign" || req.Spec != spec {
			t.Error("create request changed")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(apiv1.TaskBudgetStatus{Observed: true, Binding: apiv1.TaskBudgetBinding{TaskUID: "campaign", Epoch: 1, HostID: "host", PodUID: "pod", Deadline: time.Now().Add(119 * time.Second)}})
	}))
	defer server.Close()
	remote := &apiv1.HTTPTaskBudgetGate{BaseURL: server.URL, Client: server.Client(), Token: func(context.Context) (string, error) { return "synthetic", nil }}
	gate := &ownedBudgetGate{remote: remote}
	raw, _ := json.Marshal(spec)
	values := map[string]string{"AGENTD_OWNED_BUDGET_SPEC": string(raw), "AGENTD_OWNED_TASK_UID": "campaign", "AGENTD_OWNED_HOST_ID": "host", "DEV_ENV_POD_UID": "pod", "AGENTD_OWNED_HOME_UID": "home-original", "AGENTD_OWNED_HOME_BASELINE": strings.Repeat("a", 64)}
	getenv := func(key string) string { return values[key] }
	settings := agentd.Settings{Home: home, StateDir: state, CodexHome: filepath.Join(home, ".codex")}
	if _, _, err := ownedRootBootstrap(context.Background(), settings, gate, getenv); err == nil {
		t.Fatal("wrong HOME admitted")
	}
	if calls.Load() != 0 {
		t.Fatal("wrong HOME spent API create")
	}
	if _, err := os.Stat(filepath.Join(state, "owned-budget-attempt")); !os.IsNotExist(err) {
		t.Fatal("wrong HOME consumed intent")
	}
	baseline, err := hostexecutor.NativeHomeBaseline(home)
	if err != nil {
		t.Fatal(err)
	}
	values["AGENTD_OWNED_HOME_BASELINE"] = baseline
	b, e, err := ownedRootBootstrap(context.Background(), settings, gate, getenv)
	if err != nil || b.TaskUID != "campaign" || e == nil || !e.RequirePID1 || calls.Load() != 1 {
		t.Fatal("one positive bootstrap", b, err)
	}
	if _, _, err := ownedRootBootstrap(context.Background(), settings, gate, getenv); err == nil || calls.Load() != 1 {
		t.Fatal("bootstrap rearmed", err, calls.Load())
	}
}
