package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexhostpool"
)

func capacityCreate(fresh bool) apiv1.CreateSessionRequest {
	return apiv1.CreateSessionRequest{Repo: "dev-env", Agent: "codex", Mode: "remote", Model: "gpt-6-astra", Profile: "dev", Size: "L", FreshHost: fresh}
}

func (f *fixture) memoryReport(name string, current int64) {
	f.t.Helper()
	sess := f.session(name)
	sess.Status.Agent.CodexHostMemory = &v1alpha1.CodexHostMemoryStatus{CurrentBytes: current, LimitBytes: 4 << 30, ObservedAt: metav1.NewTime(f.now)}
	if err := f.c.Status().Update(context.Background(), sess); err != nil {
		f.t.Fatal(err)
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		f.t.Fatal(err)
	}
	pod.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")}
	if err := f.c.Update(context.Background(), &pod); err != nil {
		f.t.Fatal(err)
	}
}

func TestCapacityAPISelectAndCreate(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "room", true: "full"}[full], func(t *testing.T) {
			f := newCodexFixture(t)
			f.addCodexHost("paired", codexHostOpt{})
			current := int64(2 << 30)
			if full {
				current = 3400 << 20
			}
			f.memoryReport("paired", current)
			req := apiv1.CodexHostSelectRequest{Repo: "dev-env", Profile: "dev", LaunchID: codexTestLaunch}
			w := f.do(http.MethodPost, apiv1.CodexHostsSelectPath+"?check=true", tokClient, req)
			var choice codexhostpool.Result
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &choice) != nil {
				t.Fatalf("select %d %s", w.Code, w.Body.String())
			}
			want := "use"
			if full {
				want = "create"
			}
			if choice.Action != want || full && choice.Reason != "host_full" {
				t.Fatalf("choice %+v", choice)
			}
			if f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
				t.Fatal("check wrote lease")
			}
			w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(false))
			if !full {
				if w.Code != http.StatusConflict {
					t.Fatalf("reuse bypassed %d %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusCreated {
				t.Fatalf("create %d %s", w.Code, w.Body.String())
			}
			var made apiv1.Session
			_ = json.Unmarshal(w.Body.Bytes(), &made)
			if f.session(made.Name).Annotations[codexCapacityReason] != "host_full" {
				t.Fatal("create reason lost")
			}
			// A pending second host counts before its first status update.
			w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true))
			if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte("at_capacity")) {
				t.Fatalf("third host %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestFreshHostAPI(t *testing.T) {
	for _, tok := range []string{tokHuman, tokClient} {
		t.Run(tok, func(t *testing.T) {
			f := newCodexFixture(t)
			f.addCodexHost("old", codexHostOpt{})
			req := capacityCreate(true)
			req.IdempotencyKey = "fresh-host-intent"
			w := f.do(http.MethodPost, apiv1.SessionsPath, tok, req)
			if w.Code != http.StatusCreated {
				t.Fatalf("fresh create %d %s", w.Code, w.Body.String())
			}
			var made apiv1.Session
			_ = json.Unmarshal(w.Body.Bytes(), &made)
			saved := f.session(made.Name)
			if saved.Annotations[codexCapacityReason] != "fresh_host" || saved.Annotations[codexCapacityRequester] == "" {
				t.Fatalf("intent not recorded %+v", saved.Annotations)
			}
			if w = f.do(http.MethodPost, apiv1.SessionsPath, tok, req); w.Code != http.StatusOK {
				t.Fatalf("idempotent retry %d %s", w.Code, w.Body.String())
			}
			req.FreshHost = false
			if w = f.do(http.MethodPost, apiv1.SessionsPath, tok, req); w.Code != http.StatusConflict {
				t.Fatalf("intent hash lost %d", w.Code)
			}
			if f.session("old").Annotations[codexHostLeaseAnnotation] != "" {
				t.Fatal("fresh intent touched old host lease")
			}
		})
	}
	for _, kind := range []string{"ordinary", "workbench", "coordinator"} {
		t.Run(kind, func(t *testing.T) {
			f := newCodexFixture(t)
			if kind == "coordinator" {
				f = coordinatorFixture(t)
				f.enableCodexHosts()
			}
			tok := tokCoordinator
			if kind == "ordinary" {
				tok = f.sessionPod("ordinary", "dev", 0)
			}
			if kind == "workbench" {
				tok = workbenchSessionToken(t, f, "workbench-parent")
			}
			for _, path := range []string{apiv1.SessionsPath, apiv1.CodexHostsSelectPath + "?check=true"} {
				var body any = capacityCreate(true)
				if path != apiv1.SessionsPath {
					body = apiv1.CodexHostSelectRequest{Repo: "dev-env", LaunchID: codexTestLaunch, FreshHost: true}
				}
				w := f.do(http.MethodPost, path, tok, body)
				if w.Code != http.StatusForbidden {
					t.Fatalf("fresh authority %s %d %s", path, w.Code, w.Body.String())
				}
			}
		})
	}
	f := newFixture(t)
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true)); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled fresh %d %s", w.Code, w.Body.String())
	}
}

func TestCapacityReservationAcrossReplicas(t *testing.T) {
	var updates atomic.Int32
	bothRead := make(chan struct{})
	f := newCodexFixture(t, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == codexCapacityName && cm.Data["pending"] != "" {
			n := updates.Add(1)
			if n == 2 {
				close(bothRead)
			}
			if n <= 2 {
				<-bothRead
			}
		}
		return c.Update(ctx, obj, opts...)
	}})
	ctx := context.Background()
	if err := f.c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: codexCapacityNamespace, Name: codexCapacityName}}); err != nil {
		t.Fatal(err)
	}
	// Two API server objects share storage, never a process mutex.
	other := &Server{Live: f.srv.Live, Client: f.srv.Client, Policy: f.srv.Policy}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, srv := range []*Server{f.srv, other} {
		go func(i int, srv *Server) {
			<-start
			results <- srv.reserveCodexCapacity(ctx, codexCapacityPending{Name: []string{"one", "two"}[i], Profile: "dev", Attempt: []string{"attempt-one", "attempt-two"}[i]})
		}(i, srv)
	}
	close(start)
	success := 0
	for range 2 {
		if err := <-results; err == nil {
			success++
		}
	}
	if success != 2 {
		t.Fatalf("concurrent reservations admitted %d", success)
	}
	_, pending, err := f.srv.codexCapacity(ctx)
	if err != nil || len(pending) != success {
		t.Fatalf("pending %+v %v", pending, err)
	}
	if err = f.srv.releaseCodexCapacity(ctx, "another-attempt"); err != nil {
		t.Fatal(err)
	}
	if err = other.reserveCodexCapacity(ctx, codexCapacityPending{Name: "third", Profile: "dev", Attempt: "attempt-three"}); err == nil {
		t.Fatal("unknown create reservation was lost")
	}
	if err = f.srv.releaseCodexCapacity(ctx, pending[0].Attempt); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityUnknownCreateRetainsReservation(t *testing.T) {
	f := newCodexFixture(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			return errors.New("transport interrupted")
		}
		return c.Create(ctx, obj, opts...)
	}})
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown create %d %s", w.Code, w.Body.String())
	}
	_, pending, err := f.srv.codexCapacity(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("reservation lost %+v %v", pending, err)
	}
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("second unknown %d %s", w.Code, w.Body.String())
	}
	_, pending, err = f.srv.codexCapacity(context.Background())
	if err != nil || len(pending) != 2 {
		t.Fatalf("unknown slots lost %+v %v", pending, err)
	}
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true))
	if w.Code != http.StatusConflict {
		t.Fatalf("third attempted %d %s", w.Code, w.Body.String())
	}
}

func TestMemoryHeartbeatAndLegacyViews(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{})
	tok := f.sessionPod("reporting", "dev", 0)
	sess := f.session("reporting")
	sess.Spec.Agent = v1alpha1.AgentCodex
	sess.Spec.Mode = v1alpha1.ModeRemote
	sess.Spec.Prompt = ""
	if err := f.c.Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	st := status("reporting")
	st.CodexHostMemory = &protocol.CodexHostMemory{CurrentBytes: 3400 << 20, LimitBytes: 4 << 30, ObservedAt: f.now}
	path := protocol.HeartbeatPath("reporting")
	if w := f.do(http.MethodPost, path, tok, st); w.Code != http.StatusOK {
		t.Fatalf("beat %d %s", w.Code, w.Body.String())
	}
	if f.session("reporting").Status.Agent.CodexHostMemory == nil {
		t.Fatal("sample not persisted")
	}
	for _, route := range []string{apiv1.SessionPath("reporting"), apiv1.SessionsPath, apiv1.FleetPath} {
		w := f.do(http.MethodGet, route, tokHuman, nil)
		if w.Code != http.StatusOK || bytes.Contains(w.Body.Bytes(), []byte("codexHostMemory")) || bytes.Contains(w.Body.Bytes(), []byte("freshHost")) {
			t.Fatalf("legacy view changed %s %d %s", route, w.Code, w.Body.String())
		}
	}
	for _, sample := range []*protocol.CodexHostMemory{nil, {CurrentBytes: -1, LimitBytes: 4 << 30, ObservedAt: f.now}, {CurrentBytes: 4 << 30, LimitBytes: 4 << 30, ObservedAt: f.now.Add(time.Second)}, {CurrentBytes: 4 << 30, LimitBytes: 4 << 30, ObservedAt: f.now.Add(-4 * time.Minute)}} {
		st.CodexHostMemory = sample
		if w := f.do(http.MethodPost, path, tok, st); w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		if f.session("reporting").Status.Agent.CodexHostMemory != nil {
			t.Fatal("unknown sample did not clear")
		}
	}
}

func TestCapacityBudgetBindingAndLeaseRecheck(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{})
	f.memoryReport("paired", 3400<<20)
	sess := f.session("paired")
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &pod); err != nil {
		t.Fatal(err)
	}
	// A report using the wrong container budget is unknown, so default reuse
	// remains available rather than manufacturing full capacity.
	sess.Status.Agent.CodexHostMemory.LimitBytes = 8 << 30
	if err := f.c.Status().Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	h := f.srv.codexHostFrom(sess, &pod, f.tmpl, false, f.now)
	if h.MemoryLimit != 0 || codexhostpool.MemoryFull(h, f.now) {
		t.Fatal("foreign budget accepted")
	}
	sess.Status.Agent.CodexHostMemory.LimitBytes = 4 << 30
	if err := f.c.Status().Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	who := codexhostpool.Caller{Kind: codexhostpool.KindClient, Name: "client", LaunchID: codexTestLaunch}
	result, _, err := f.srv.leaseCodexHost(context.Background(), codexLeaseTarget{name: "paired", uid: string(sess.UID), profile: "dev", repo: "dev-env", caller: who, tmpl: f.tmpl})
	if err != nil || result != codexLeaseBusy || f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
		t.Fatalf("lease admitted full host %v %v", result, err)
	}
}

func TestCapacityBoundWithSelectionOff(t *testing.T) {
	f := newCodexFixture(t)
	for _, name := range []string{"first", "second"} {
		f.addCodexHost(name, codexHostOpt{phase: v1alpha1.PhaseSuspended, mode: v1alpha1.OperatingModeSuspended})
	}
	f.srv.CodexHosts = nil
	f.h = f.srv.Handler()
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(false))
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte("at_capacity")) {
		t.Fatalf("direct API bypass %d %s", w.Code, w.Body.String())
	}
}

func TestPendingCreateCountsAcrossProfiles(t *testing.T) {
	f := newCodexFixture(t)
	for _, profile := range []string{"dev", "full"} {
		req := capacityCreate(false)
		req.Profile = profile
		if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, req); w.Code != http.StatusCreated {
			t.Fatalf("pending %s %d %s", profile, w.Code, w.Body.String())
		}
	}
	// Both still have empty phase: a third profile must hit the fleet cap.
	req := capacityCreate(true)
	req.Profile = "dev"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, req)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte("at_capacity")) {
		t.Fatalf("pending fleet bypass %d %s", w.Code, w.Body.String())
	}
}

func TestDefiniteCreateRefusalReleasesCapacity(t *testing.T) {
	t.Run("own admission refusal", func(t *testing.T) {
		f := newCodexFixture(t)
		ctx := withCodexHostAdmission(context.Background())
		c := &caller{kind: kindClient, parent: clientSA, identity: saIdentity(clientSA)}
		sess, err := f.srv.newSession(ctx, capacityCreate(true), c)
		if err != nil {
			t.Fatal(err)
		}
		sess.Name = "reserved-during-admission"
		f.srv.CodexWorkbenchHosts.Name = sess.Name
		err = f.srv.createOrdinaryCodexHost(ctx, sess, c, capacityCreate(true))
		var refused *apiError
		if !errors.As(err, &refused) || refused.status != http.StatusForbidden {
			t.Fatalf("expected definite admission refusal: %v", err)
		}
		_, pending, err := f.srv.codexCapacity(ctx)
		if err != nil || len(pending) != 0 {
			t.Fatalf("refusal kept reservation %+v %v", pending, err)
		}
	})
	t.Run("old title schema", func(t *testing.T) {
		f := newCodexFixture(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.AgentSession); ok {
				return apierrors.NewBadRequest(`unknown field "spec.title"`)
			}
			return c.Create(ctx, obj, opts...)
		}})
		req := capacityCreate(true)
		req.Title = "Coordinator"
		w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("old schema %d %s", w.Code, w.Body.String())
		}
		_, pending, err := f.srv.codexCapacity(context.Background())
		if err != nil || len(pending) != 0 {
			t.Fatalf("refusal kept reservation %+v %v", pending, err)
		}
	})
}

func TestUnknownCreateOccupiesOneSlot(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	f := newCodexFixture(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok && fail.Swap(false) {
			return errors.New("unknown create outcome")
		}
		return c.Create(ctx, obj, opts...)
	}})
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true)); w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code, w.Body.String())
	}
	req := capacityCreate(false)
	req.Profile = "full"
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, req); w.Code != http.StatusCreated {
		t.Fatalf("one unknown blocked fleet %d %s", w.Code, w.Body.String())
	}
	_, pending, err := f.srv.codexCapacity(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("unknown slot lost %+v %v", pending, err)
	}
}

func TestCapacitySnapshotCountsCommitDuringList(t *testing.T) {
	armed := false
	f := newCodexFixture(t, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
		if err := c.List(ctx, obj, opts...); err != nil {
			return err
		}
		if _, ok := obj.(*v1alpha1.AgentSessionList); ok && armed {
			armed = false
			made := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "late-commit", Annotations: map[string]string{codexCapacityAttempt: "late-attempt"}}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Profile: "dev", Agent: v1alpha1.AgentCodex, Mode: v1alpha1.ModeRemote, Model: "gpt-6-astra"}}
			if err := c.Create(ctx, made); err != nil {
				return err
			}
			var cm corev1.ConfigMap
			if err := c.Get(ctx, types.NamespacedName{Namespace: codexCapacityNamespace, Name: codexCapacityName}, &cm); err != nil {
				return err
			}
			var pending []codexCapacityPending
			if err := json.Unmarshal([]byte(cm.Data["pending"]), &pending); err != nil {
				return err
			}
			kept := pending[:0]
			for _, p := range pending {
				if p.Attempt != "late-attempt" {
					kept = append(kept, p)
				}
			}
			raw, _ := json.Marshal(kept)
			cm.Data["pending"] = string(raw)
			if err := c.Update(ctx, &cm); err != nil {
				return err
			}
		}
		return nil
	}})
	f.addCodexHost("existing", codexHostOpt{})
	if err := f.srv.reserveCodexCapacity(context.Background(), codexCapacityPending{Name: "late-commit", Profile: "dev", Attempt: "late-attempt"}); err != nil {
		t.Fatal(err)
	}
	ctx := withCodexHostAdmission(context.Background())
	c := &caller{kind: kindClient, parent: clientSA, identity: saIdentity(clientSA)}
	req := capacityCreate(true)
	sess, err := f.srv.newSession(ctx, req, c)
	if err != nil {
		t.Fatal(err)
	}
	sess.Name = "would-be-third"
	armed = true
	err = f.srv.createOrdinaryCodexHost(ctx, sess, c, req)
	var refusal *apiError
	if !errors.As(err, &refusal) || refusal.body.Reason != "at_capacity" {
		t.Fatalf("missed late commit: %v", err)
	}
}

func TestCapacityConfiguredNamespace(t *testing.T) {
	f := newCodexFixture(t)
	f.srv.PolicyNamespace = "operator-control"
	if err := f.srv.reserveCodexCapacity(context.Background(), codexCapacityPending{Name: "pending", Profile: "dev", Attempt: "attempt"}); err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: "operator-control", Name: codexCapacityName}, &cm); err != nil {
		t.Fatal(err)
	}
}

func TestCodexRestoreSkipsReuseWithinBound(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{})
	const old = "dev-env-1001-090000"
	f.srv.Shelf = &fakeShelf{list: protocol.RescueList{Rescues: []protocol.RescueEntry{rescueEntry(old, "20261001-1200", true, map[string][]string{"dev-env": {"refs/heads/agent/" + old}})}}}
	req := capacityCreate(false)
	req.Restore = old + "/20261001-1200"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("restore substituted reusable host %d %s", w.Code, w.Body.String())
	}
	var saved apiv1.Session
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	if saved.Restore != req.Restore {
		t.Fatal("restored state lost")
	}
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte("at_capacity")) {
		t.Fatalf("restore bypassed bound %d %s", w.Code, w.Body.String())
	}
}

func TestConcurrentFreshCreatesKeepHostBound(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{})
	other := &Server{Live: f.srv.Live, Client: f.srv.Client, Policy: f.srv.Policy, Templates: f.srv.Templates, Now: f.srv.Now, CodexHosts: f.srv.CodexHosts, Log: f.srv.Log}
	results := make(chan error, 2)
	start := make(chan struct{})
	for i, srv := range []*Server{f.srv, other} {
		go func(i int, srv *Server) {
			ctx := withCodexHostAdmission(context.Background())
			c := &caller{kind: kindClient, parent: clientSA, identity: saIdentity(clientSA)}
			req := capacityCreate(true)
			sess, err := srv.newSession(ctx, req, c)
			if err != nil {
				results <- err
				return
			}
			sess.Name = []string{"fresh-one", "fresh-two"}[i]
			<-start
			results <- srv.createOrdinaryCodexHost(ctx, sess, c, req)
		}(i, srv)
	}
	close(start)
	created := 0
	for range 2 {
		if err := <-results; err == nil {
			created++
		} else {
			var refusal *apiError
			if !errors.As(err, &refusal) {
				t.Fatalf("unexpected create error %v", err)
			}
		}
	}
	if created > 1 {
		t.Fatalf("created %d hosts beside existing host", created)
	}
	var sessions v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &sessions, client.InNamespace(sessionNS)); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Items) > 2 {
		t.Fatalf("profile has %d hosts", len(sessions.Items))
	}
}

func TestCapacityFencesSameIdempotencyKey(t *testing.T) {
	f := newCodexFixture(t)
	ctx := context.Background()
	first := codexCapacityPending{Name: "first", Profile: "dev", Attempt: "first-attempt", RequestKey: "parent-key-hash"}
	if err := f.srv.reserveCodexCapacity(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Name = "second"
	second.Attempt = "second-attempt"
	if err := f.srv.reserveCodexCapacity(ctx, second); err == nil {
		t.Fatal("same-key request was admitted while unconfirmed")
	}
}
