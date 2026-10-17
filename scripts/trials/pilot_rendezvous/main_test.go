package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

const aUID = "22222222-2222-4222-8222-222222222222"
const podUID = "44444444-4444-4444-8444-444444444444"
const bUID = "33333333-3333-4333-8333-333333333333"

type fixture struct {
	c           Config
	o           Observation
	now         time.Time
	events      []string
	calls       int
	attempts    int
	sleeps      int
	dispatchErr error
	observeErr  error
	reserveErr  error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	p := testutil.NewPilot(t).Policy
	p.NotBefore = time.Date(2026, 10, 16, 17, 50, 0, 0, time.UTC)
	p.ExpiresAt = p.NotBefore.Add(30 * time.Minute)
	p.Cohort = "synthetic-rendezvous"
	f := &fixture{now: p.NotBefore.Add(4*time.Minute + 43*time.Second)}
	f.c = Config{Policy: *p, AUID: aUID, APodUID: podUID, Head: strings.Repeat("a", 40), CommonMarker: "synthetic-common-marker", RepositoryMarker: "synthetic-repo-marker", ATaskStarted: p.NotBefore.Add(4*time.Minute + 4*time.Second), ReceiptDir: t.TempDir()}
	put := func(name, text string) (string, string) {
		t.Helper()
		path := filepath.Join(f.c.ReceiptDir, name)
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(text))
		return path, hex.EncodeToString(sum[:])
	}
	f.c.Binary, f.c.BinarySHA256 = put("not-an-executable", "OFFLINE: never execute")
	f.c.CAFile, f.c.CASHA256 = put("ca.pem", "synthetic CA, not live credentials")
	f.c.PromptFile, _ = put("prompt.txt", p.Prompts[1])
	beat := f.now
	f.o.Session = apiv1.Session{UID: aUID, Name: p.SessionName(0), Repo: "dev-env", Agent: "codex", Mode: "task", Model: p.Request(0).Model, Effort: "high", Size: "S", Profile: "rule-canary-codex", Limits: &apiv1.Limits{Timeout: "15m0s"}, IdempotencyKey: p.Key(0), Prompt: p.Prompts[0], CreatedAt: p.NotBefore.Add(21 * time.Second), PrivateHistory: &apiv1.PrivateHistory{SessionUID: aUID, StopState: "Unknown", LastExecutor: &apiv1.PrivateExecutor{PodUID: podUID}}, AgentStatus: &apiv1.AgentStatus{State: "running", Boot: "ready", Head: f.c.Head, LastHeartbeat: &beat, ProjectBootstrap: &apiv1.ProjectBootstrap{State: "Ready", CatalogRevision: p.CatalogRevision}}}
	claim := apiv1.TaskClaimResult{Outcome: "Created", Claim: apiv1.TaskClaim{UID: "55555555-5555-4555-8555-555555555555", Identity: apiv1.TaskClaimIdentity{Project: "dev-env", Repository: "dev-env", TaskKey: "task:" + p.Key(0)}, Owner: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: p.SessionName(0), UID: aUID}, Actor: apiv1.TaskClaimActor{Kind: "session", PodUID: podUID, PodName: p.SessionName(0)}, Provenance: apiv1.TaskClaimProvenance{CatalogRevision: p.CatalogRevision, RulesRevision: p.RulesRevision, ReportedHead: f.c.Head}, CreatedAt: p.NotBefore.Add(4*time.Minute + 22*time.Second)}}
	f.o.Log = apiv1.SessionLog{Session: p.SessionName(0), Text: "[agentd] task " + p.SessionName(0) + " started " + f.c.ATaskStarted.Format(time.RFC3339) + ", conversation\n" + f.c.CommonMarker + " " + f.c.RepositoryMarker + "\n" + claimLine(t, claim)}
	if err := f.c.validate(); err != nil {
		t.Fatal(err)
	}
	return f
}
func claimLine(t *testing.T, r apiv1.TaskClaimResult) string {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := json.Marshal(map[string]any{"operation": "task-claim-create", "exitCode": 0, "stdout": string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	return string(entry) + "\n"
}
func (f *fixture) adapters(t *testing.T) adapters {
	t.Helper()
	return adapters{now: func() time.Time { return f.now }, sleep: func(_ context.Context, d time.Duration) error {
		f.events = append(f.events, "sleep")
		f.sleeps++
		f.now = f.now.Add(d)
		return nil
	}, observe: func(context.Context) (Observation, error) {
		f.events = append(f.events, "observe")
		return f.o, f.observeErr
	}, claimAttempt: func(o Observation, d Deadlines) error {
		f.events = append(f.events, "reserve")
		f.attempts++
		return f.reserveErr
	}, dispatch: func(_ context.Context, args []string) ([]byte, error) {
		f.events = append(f.events, "dispatch")
		f.calls++
		if !reflect.DeepEqual(args, f.c.dispatchArgs()) {
			t.Error("changed prebound dispatch")
		}
		raw, _ := json.Marshal(apiv1.Session{UID: bUID, Name: f.c.Policy.SessionName(1), IdempotencyKey: f.c.Policy.Key(1)})
		return raw, f.dispatchErr
	}}
}
func TestGateInvokesPreboundDispatchWithoutReportingTurn(t *testing.T) {
	f := newFixture(t)
	r, err := run(context.Background(), f.c, f.adapters(t))
	if err != nil || r.State != "B_CREATE_ACCEPTED_NOT_PILOT_ACCEPTANCE" || f.calls != 1 || !reflect.DeepEqual(f.events, []string{"observe", "reserve", "dispatch"}) || !r.DispatchAt.Equal(r.GateAt) {
		t.Fatalf("%+v %v events=%v", r, err, f.events)
	}
	// Keep private executor stop distinct from task/semantic success.
	if f.o.Session.PrivateHistory.StopState != "Unknown" {
		t.Fatal("invented terminal acceptance")
	}
}
func TestCapturedTimingAndColdStartReplay(t *testing.T) {
	f := newFixture(t)
	claim := f.c.Policy.NotBefore.Add(4*time.Minute + 22*time.Second)
	// Original trace: 69s Aclaim->Bcreate +53s Bstartup +49s tools =171s.
	// A's combined eight RPC reads ended at153s: this trace MUST fail.
	if oldMessage, oldAExit := claim.Add(171*time.Second), claim.Add(153*time.Second); !oldMessage.After(oldAExit) {
		t.Fatal("old counterexample lost")
	}
	r, err := run(context.Background(), f.c, f.adapters(t))
	if err != nil {
		t.Fatal(err)
	}
	// Retain the measured21.485878s claim-to-capture, remove48.154352s
	// model/reporting handoff; dispatch is in this same synchronous call.
	capture := claim.Add(21485878 * time.Microsecond)
	for _, tc := range []struct {
		name    string
		startup time.Duration
	}{{"captured-warm", 53 * time.Second}, {"captured-cold-envelope", 199*time.Second + 20*time.Second}} {
		t.Run(tc.name, func(t *testing.T) {
			message := capture.Add(tc.startup + 49*time.Second)
			round := claim.Add(45 * time.Second)
			rounds := 1
			for round.Before(message) {
				round = round.Add(45 * time.Second)
				rounds++
			}
			if rounds > 8 || !round.Before(r.Deadlines.Startup) {
				t.Fatalf("readiness exceeded bound: %v rounds%d", round, rounds)
			}
			// Two-way ACK at next bounded30s read, then normal task-terminal and
			// authorized handoff read; this synthetic trace is NOT runtime proof.
			ack := round.Add(30 * time.Second)
			terminal := ack.Add(30 * time.Second)
			if !ack.Before(r.Deadlines.ACK) || !terminal.Before(r.Deadlines.Terminal) {
				t.Fatal("phase overrun")
			}
		})
	}
}
func TestGateRefusesWrongBindingsAndTerminalFacts(t *testing.T) {
	for _, mode := range []string{"sessionUID", "podUID", "head", "catalog", "prompt", "model", "endedTask", "staleHeartbeat", "wrongLogSession", "wrongStart", "wrongOwner", "wrongClaimPod", "wrongRules", "duplicateClaimReceipt", "staleClaim"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			switch mode {
			case "sessionUID":
				f.o.Session.UID = bUID
			case "podUID":
				f.o.Session.PrivateHistory.LastExecutor.PodUID = bUID
			case "head":
				f.o.Session.AgentStatus.Head = strings.Repeat("b", 40)
			case "catalog":
				f.o.Session.AgentStatus.ProjectBootstrap.CatalogRevision = "wrong"
			case "prompt":
				f.o.Session.Prompt = "wrong"
			case "model":
				f.o.Session.Model = "wrong"
			case "endedTask":
				f.o.Session.AgentStatus.Task = &apiv1.TaskResult{}
			case "staleHeartbeat":
				beat := f.now.Add(-maxHeartbeatAge - time.Second)
				f.o.Session.AgentStatus.LastHeartbeat = &beat
			case "wrongLogSession":
				f.o.Log.Session = "wrong"
			case "wrongStart":
				f.o.Log.Text = strings.ReplaceAll(f.o.Log.Text, f.c.ATaskStarted.Format(time.RFC3339), f.c.ATaskStarted.Add(time.Second).Format(time.RFC3339))
			case "wrongOwner":
				f.o.Log.Text = strings.ReplaceAll(f.o.Log.Text, aUID, bUID)
			case "wrongClaimPod":
				f.o.Log.Text = strings.ReplaceAll(f.o.Log.Text, podUID, bUID)
			case "wrongRules":
				f.o.Log.Text = strings.ReplaceAll(f.o.Log.Text, f.c.Policy.RulesRevision, "wrong")
			case "duplicateClaimReceipt":
				f.o.Log.Text += f.o.Log.Text
			case "staleClaim":
				f.now = f.now.Add(15 * time.Second)
				f.o.Session.AgentStatus.LastHeartbeat = &f.now
			}
			_, err := run(context.Background(), f.c, f.adapters(t))
			if err == nil || f.calls != 0 || f.attempts != 0 {
				t.Fatalf("not refused: err%v calls%d attempts%d", err, f.calls, f.attempts)
			}
		})
	}
}
func TestNeverReadyHasFiniteRoundsAndDeadline(t *testing.T) {
	f := newFixture(t)
	f.o.Log.Text = "no claim yet"
	a := f.adapters(t)
	original := a.observe
	a.observe = func(ctx context.Context) (Observation, error) {
		beat := f.now
		f.o.Session.AgentStatus.LastHeartbeat = &beat
		return original(ctx)
	}
	start := f.now
	_, err := run(context.Background(), f.c, a)
	if err == nil || f.calls != 0 || f.attempts != 0 || f.sleeps > 60 || f.now.Sub(start) > 5*time.Minute {
		t.Fatalf("unbounded err%v sleeps%d duration%v", err, f.sleeps, f.now.Sub(start))
	}
}
func TestAllAdapterErrorsAbortWithoutRetry(t *testing.T) {
	for _, mode := range []string{"observe", "reserve", "dispatch", "timeout", "invalidOutput", "lateReserve", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			a := f.adapters(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "observe":
				f.observeErr = errors.New("403 or malformed transport")
			case "reserve":
				f.reserveErr = os.ErrExist
			case "dispatch":
				f.dispatchErr = errors.New("ambiguous CLI/server failure")
			case "timeout":
				f.dispatchErr = context.DeadlineExceeded
			case "invalidOutput":
				a.dispatch = func(context.Context, []string) ([]byte, error) { f.calls++; return []byte(`{}`), nil }
			case "lateReserve":
				a.claimAttempt = func(Observation, Deadlines) error { f.attempts++; f.now = f.now.Add(6 * time.Second); return nil }
			case "cancel":
				cancel()
			}
			_, err := run(ctx, f.c, a)
			if err == nil || f.calls > 1 || f.attempts > 1 || f.sleeps != 0 {
				t.Fatalf("retried %v calls%d attempts%d", err, f.calls, f.attempts)
			}
			if (mode == "observe" || mode == "reserve" || mode == "lateReserve" || mode == "cancel") && f.calls != 0 {
				t.Fatal("dispatch on failed gate")
			}
		})
	}
}
func TestDurableAttemptPreventsDuplicateOrReplacement(t *testing.T) {
	f := newFixture(t)
	a := f.adapters(t)
	a.claimAttempt = liveAdapters(f.c, nil).claimAttempt
	f.dispatchErr = errors.New("unknown result after invocation")
	_, first := run(context.Background(), f.c, a)
	_, second := run(context.Background(), f.c, a)
	if first == nil || !errors.Is(second, os.ErrExist) || f.calls != 1 {
		t.Fatalf("first%v second%v calls%d", first, second, f.calls)
	}
	data, err := os.ReadFile(filepath.Join(f.c.ReceiptDir, f.c.Policy.Key(1)+".attempt.json"))
	if err != nil || !bytes.Contains(data, []byte("ATTEMPT_RESERVED_NO_RETRY")) {
		t.Fatal("attempt not retained")
	}
}
func TestInfeasibleAbsoluteBudgetRefusesDispatch(t *testing.T) {
	f := newFixture(t)
	for _, mode := range []string{"task", "pod", "window", "future", "old"} {
		t.Run(mode, func(t *testing.T) {
			c := f.c
			claim := f.now.Add(-time.Second)
			created := f.o.Session.CreatedAt
			now := f.now
			switch mode {
			case "task":
				c.ATaskStarted = claim.Add(-4 * time.Minute)
			case "pod":
				created = claim.Add(-9 * time.Minute)
			case "window":
				c.Policy.ExpiresAt = claim.Add(12 * time.Minute)
			case "future":
				claim = now.Add(time.Second)
			case "old":
				claim = now.Add(-31 * time.Second)
			}
			if _, err := deadlines(c, claim, created, now); err == nil {
				t.Fatal("accepted infeasible phases")
			}
		})
	}
}
func TestDefaultAndDriftAreNonexecuting(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.c.ReceiptDir, "config.json")
	raw, _ := json.Marshal(f.c)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := execute([]string{"--config", path}, &out); err != nil || !strings.Contains(out.String(), "NONEXECUTING") {
		t.Fatalf("%v %s", err, out.String())
	}
	for _, mode := range []string{"binary", "ca", "prompt"} {
		t.Run(mode, func(t *testing.T) {
			c := f.c
			switch mode {
			case "binary":
				c.BinarySHA256 = strings.Repeat("0", 64)
			case "ca":
				c.CASHA256 = strings.Repeat("0", 64)
			case "prompt":
				c.Policy.Prompts[1] = "changed"
			}
			if c.validate() == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}

// Exercise the actual adapter command sequence through a fake invocation,
// rather than substituting a plan-only dispatcher. No process/transport starts.
func TestExistingCLIAdapterDispatchesInSameStep(t *testing.T) {
	f := newFixture(t)
	f.c.ATaskStarted = time.Time{} // arm before model startup; derive start from log
	var commands [][]string
	fake := func(_ context.Context, binary string, args []string) ([]byte, error) {
		commands = append(commands, append([]string{binary}, args...))
		switch args[0] {
		case "show":
			return json.Marshal(f.o.Session)
		case "log":
			return json.Marshal(f.o.Log)
		default:
			f.calls++
			return json.Marshal(apiv1.Session{UID: bUID, Name: f.c.Policy.SessionName(1), IdempotencyKey: f.c.Policy.Key(1)})
		}
	}
	a := liveAdapters(f.c, fake)
	a.now = func() time.Time { return f.now }
	r, err := run(context.Background(), f.c, a)
	if err != nil || f.calls != 1 || len(commands) != 3 || commands[0][1] != "show" || commands[1][1] != "log" || !r.DispatchAt.Equal(r.GateAt) {
		t.Fatalf("%v %+v commands%v", err, r, commands)
	}
	for _, args := range commands {
		found := false
		for _, arg := range args {
			found = found || arg == "--once"
		}
		if !found {
			t.Fatalf("retrying command %v", args)
		}
	}
}

func TestActualStartupNoLogTransitionsToOneDispatch(t *testing.T) {
	f := newFixture(t)
	f.now = f.now.Add(-10 * time.Second)
	stages, logs, dispatches := 0, 0, 0
	fake := func(_ context.Context, _ string, args []string) ([]byte, error) {
		switch args[0] {
		case "show":
			stages++
			s := f.o.Session
			switch stages {
			case 1:
				s.AgentStatus = nil
				s.PrivateHistory = nil
			case 2:
				a := *s.AgentStatus
				a.Boot = "starting"
				a.State = "idle"
				a.ProjectBootstrap = &apiv1.ProjectBootstrap{State: "Pending"}
				s.AgentStatus = &a
			}
			return json.Marshal(s)
		case "log":
			logs++
			if stages < 3 {
				return nil, errors.New("captured: session has no log yet (404 not_found)")
			}
			return json.Marshal(f.o.Log)
		default:
			dispatches++
			return json.Marshal(apiv1.Session{UID: bUID, Name: f.c.Policy.SessionName(1), IdempotencyKey: f.c.Policy.Key(1)})
		}
	}
	a := liveAdapters(f.c, fake)
	a.now = func() time.Time { return f.now }
	a.sleep = func(_ context.Context, d time.Duration) error { f.now = f.now.Add(d); return nil }
	r, err := run(context.Background(), f.c, a)
	if err != nil || stages != 3 || logs != 1 || dispatches != 1 || r.ObservationRounds != 3 {
		t.Fatalf("err%v shows%d logs%d creates%d receipt%+v", err, stages, logs, dispatches, r)
	}
}

func TestCapturedRealClaimLogGate(t *testing.T) {
	f := newFixture(t)
	raw, err := os.ReadFile("testdata/captured-claim-log.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &f.o.Log); err != nil {
		t.Fatal(err)
	}
	// Exact actual protocol/start/receipt bytes, projected from the preserved
	// log-6 capture. Synthetic Session/prompt stand in for the supported show.
	f.c.Policy.Cohort = "c7-20261016-1750"
	f.c.AUID = "57b6fdf8-e0d6-4545-918b-700fd40d8d0c"
	f.c.APodUID = "86c81ed2-a661-40f1-b0b0-2919ad718baa"
	f.c.Head = "c72d2e48f70f0131845dc53f55a7b177e0ac32e1"
	f.c.CommonMarker = "dev-env-common-rules-20261011-4ca29d70"
	f.c.RepositoryMarker = "dev-env-repository-rules-20261011-6d807bf2"
	f.c.Policy.CatalogRevision = "5fe34bd347f85c4b41773fbe48f815f8655945af9d73503a1ef0e89ee89aad90"
	f.c.Policy.RulesRevision = "1cebe4524e65483d4854960c34a31d3f75f1cfc0acb991f0fd490b10bbc98e2b"
	s := &f.o.Session
	s.UID = f.c.AUID
	s.Name = f.c.Policy.SessionName(0)
	s.IdempotencyKey = f.c.Policy.Key(0)
	s.PrivateHistory.SessionUID = f.c.AUID
	s.PrivateHistory.LastExecutor.PodUID = f.c.APodUID
	s.AgentStatus.Head = f.c.Head
	s.AgentStatus.ProjectBootstrap.CatalogRevision = f.c.Policy.CatalogRevision
	d, err := validateGate(f.c, f.o, f.now)
	if err != nil || d == nil {
		t.Fatalf("actual captured format refused: %v", err)
	}
}
func TestExistingRunRefusesBeforeAnyTransport(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.c.ReceiptDir, "config.json")
	raw, _ := json.Marshal(f.c)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveExclusive(filepath.Join(f.c.ReceiptDir, f.c.Policy.Key(1)+".run.json"), map[string]string{"state": "already consumed"}); err != nil {
		t.Fatal(err)
	}
	// Binary is deliberately NOT executable. Re-entry must refuse before it
	// could run any show/log/create. No actual command/transport is invoked.
	var out bytes.Buffer
	if err := execute([]string{"--config", path, "--execute"}, &out); !errors.Is(err, os.ErrExist) || out.Len() != 0 {
		t.Fatalf("err%v out%s", err, out.String())
	}
}

// Actual failure offset: task 19:26:56 -> result 19:27:27.419783. The
// installed 60s sender can be healthy throughout that 31.419783s gap.
func TestInstalledHeartbeatCadenceAcceptsHealthyGap(t *testing.T) {
	for _, age := range []time.Duration{31419783 * time.Microsecond, 59 * time.Second, time.Minute, maxHeartbeatAge} {
		t.Run(age.String(), func(t *testing.T) {
			f := newFixture(t)
			beat := f.now.Add(-age)
			f.o.Session.AgentStatus.LastHeartbeat = &beat
			r, err := run(context.Background(), f.c, f.adapters(t))
			if err != nil || f.calls != 1 || f.attempts != 1 || r.Rejection != nil {
				t.Fatalf("healthy heartbeat age%s: receipt%+v err%v calls%d", age, r, err, f.calls)
			}
		})
	}
}

func TestRejectedGateRetainsExactPredicateAndMetadata(t *testing.T) {
	for _, tc := range []struct{ name, predicate string }{
		{"missing", "heartbeat_missing"}, {"future", "heartbeat_future"}, {"stale", "heartbeat_stale"}, {"wrongUID", "session_binding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			switch tc.name {
			case "missing":
				f.o.Session.AgentStatus.LastHeartbeat = nil
			case "future":
				beat := f.now.Add(time.Second)
				f.o.Session.AgentStatus.LastHeartbeat = &beat
			case "stale":
				beat := f.now.Add(-maxHeartbeatAge - time.Nanosecond)
				f.o.Session.AgentStatus.LastHeartbeat = &beat
			case "wrongUID":
				f.o.Session.UID = bUID
			}
			r, err := run(context.Background(), f.c, f.adapters(t))
			if !errors.Is(err, errRefused) || r.Rejection == nil || r.Rejection.Predicate != tc.predicate || f.calls != 0 || f.attempts != 0 {
				t.Fatalf("receipt%+v err%v calls%d attempts%d", r, err, f.calls, f.attempts)
			}
			evidence := r.Rejection
			if !evidence.ObservedAt.Equal(f.now) || evidence.SessionUID != f.o.Session.UID || evidence.PodUID != podUID || evidence.MaxHeartbeatAgeSeconds != 70 || r.ObservationRounds != 1 {
				t.Fatalf("wrong failed-round evidence%+v", evidence)
			}
			if !reflect.DeepEqual(evidence.LastHeartbeat, f.o.Session.AgentStatus.LastHeartbeat) {
				t.Fatal("lost exact failed heartbeat timestamp")
			}
			path := filepath.Join(f.c.ReceiptDir, "rejected-result.json")
			if err := saveExclusive(path, r); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved Receipt
			if err := json.Unmarshal(raw, &saved); err != nil || saved.Rejection == nil || saved.Rejection.Predicate != tc.predicate {
				t.Fatalf("receipt not retained: %s err%v", raw, err)
			}
			if bytes.Contains(raw, []byte(f.c.Policy.Prompts[0])) || bytes.Contains(raw, []byte(f.o.Log.Text)) {
				t.Fatal("raw prompt/log leaked into refusal evidence")
			}
			t.Logf("saved predicate=%s observed=%s lastHeartbeat=%v maxAge=70s", evidence.Predicate, evidence.ObservedAt.Format(time.RFC3339Nano), evidence.LastHeartbeat)
		})
	}
}

// Fake command adapters model the proven native capture boundary, not actual
// streaming. The 315.233877s delay is claim 19:28:42 -> script 19:33:57.233877;
// its first log visibility was NOT timestamped in the old run.
func TestClaimOutputVisibilityBoundary(t *testing.T) {
	for _, tc := range []struct {
		name         string
		visibleAfter time.Duration
		accept       bool
	}{
		{"short_completed_claim_call", 2 * time.Second, true},
		{"old_visible_claim", 31 * time.Second, false},
		{"captured_long_wait_buffer", 315233877 * time.Microsecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			claimAt := f.c.Policy.NotBefore.Add(4*time.Minute + 22*time.Second)
			f.now = claimAt
			fullLog := f.o.Log
			parts := strings.SplitN(fullLog.Text, "\n", 3)
			noReceipt := fullLog
			noReceipt.Text = parts[0] + "\n" + parts[1] + "\n"
			visibleAt := claimAt.Add(tc.visibleAfter)
			var commands []string
			fake := func(_ context.Context, _ string, args []string) ([]byte, error) {
				commands = append(commands, args[0])
				switch args[0] {
				case "show":
					session := f.o.Session
					status := *session.AgentStatus
					// Match installed 60s cadence without refreshing on each observation.
					beat := claimAt.Add(f.now.Sub(claimAt) / time.Minute * time.Minute)
					status.LastHeartbeat = &beat
					session.AgentStatus = &status
					return json.Marshal(session)
				case "log":
					if f.now.Before(visibleAt) {
						return json.Marshal(noReceipt)
					}
					return json.Marshal(fullLog)
				default:
					if !reflect.DeepEqual(args, f.c.dispatchArgs()) {
						t.Fatal("changed prebound dispatch")
					}
					f.calls++
					return json.Marshal(apiv1.Session{UID: bUID, Name: f.c.Policy.SessionName(1), IdempotencyKey: f.c.Policy.Key(1)})
				}
			}
			a := liveAdapters(f.c, fake)
			a.now = func() time.Time { return f.now }
			a.sleep = func(_ context.Context, d time.Duration) error { f.now = f.now.Add(d); return nil }
			r, err := run(context.Background(), f.c, a)
			if tc.accept {
				if err != nil || f.calls != 1 || !r.GateAt.Equal(claimAt.Add(5*time.Second)) || !r.DispatchAt.Equal(r.GateAt) || !reflect.DeepEqual(commands, []string{"show", "log", "show", "log", "--project"}) {
					t.Fatalf("err%v receipt%+v creates%d commands%v", err, r, f.calls, commands)
				}
			} else {
				if err == nil || f.calls != 0 {
					t.Fatalf("buffered/old claim dispatched: err%v creates%d receipt%+v", err, f.calls, r)
				}
				if tc.name == "old_visible_claim" && (r.Rejection == nil || r.Rejection.Predicate != "claim_stale") {
					t.Fatalf("missing exact stale claim predicate%+v", r)
				}
				if f.now.Sub(claimAt) > 5*time.Minute {
					t.Fatal("buffered proof exceeded existing finite wait")
				}
			}
			t.Logf("visibleAfter=%s elapsed=%s creates=%d rejection=%+v", tc.visibleAfter, f.now.Sub(claimAt), f.calls, r.Rejection)
		})
	}
}
