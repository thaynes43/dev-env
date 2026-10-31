package apiserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// The launch rules of DESIGN-042 6.4 (S4) for a profile that names a named
// coordinator identity. Every name below is synthetic.

// withCoordinatorProfile adds the profile "coordinator", running as leadSA, to
// the fixture's templates and turns the policy on.
func withCoordinatorProfile(f *fixture) {
	f.tmpl.Profiles["coordinator"] = templates.Profile{ServiceAccount: leadSA}
	f.srv.Policy.NamedCoordinatorIdentities = true
}

func coordinatorProfileTask() apiv1.CreateSessionRequest {
	r := task()
	r.Profile = "coordinator"
	return r
}

// setPhase records a session's phase, as the reconciler would.
func setPhase(t *testing.T, f *fixture, name string, phase v1alpha1.SessionPhase) {
	t.Helper()
	s := f.session(name)
	s.Status.Phase = phase
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// suspendHolder records a session as fully suspended, as a suspend and the
// reconciler would leave it: operating mode and phase Suspended. A session the
// API created in the fake client has no pod.
func suspendHolder(t *testing.T, f *fixture, name string) {
	t.Helper()
	s := f.session(name)
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	setPhase(t, f, name, v1alpha1.PhaseSuspended)
}

// workbenchSessionToken adds a workbench session, its pod running as the
// workbench account, and returns a token bound to that pod.
func workbenchSessionToken(t *testing.T, f *fixture, name string) string {
	t.Helper()
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	f.tmpl.Profiles["workbench"] = templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
	tok := f.sessionPod(name, "workbench", 0)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
	id.PodName, id.PodUID = name, string(pod.UID)
	f.auth[tok] = id
	return tok
}

// Tom, a client and the workbench launch a session on such a profile, and the
// API records the admission the controller needs. Ordinary sessions, the named
// coordinator session itself and coordinator hosts cannot.
func TestNamedCoordinatorLaunchRules(t *testing.T) {
	f := newFixture(t)
	withCoordinatorProfile(f)
	wb := workbenchSessionToken(t, f, "haynes-ops-1031-150000")
	plain := f.sessionPod("haynes-ops-1031-150001", "full", 0)
	lead := namedCoordinatorPod(t, f, "haynes-ops-1031-150002", leadSA, 0)
	// The lead session above holds leadSA, so the launches below use a second
	// identity; each admitted launch is suspended before the next.
	const two = "dev-env-coordinator-two"
	f.tmpl.Profiles["coordinator-two"] = templates.Profile{ServiceAccount: two}
	launch := task()
	launch.Profile = "coordinator-two"
	for who, tok := range map[string]string{"human": tokHuman, "client": tokClient, "workbench session": wb} {
		w := f.do(http.MethodPost, apiv1.SessionsPath, tok, launch)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", who, w.Code, w.Body.String())
		}
		name := decode[apiv1.Session](t, w).Name
		s := f.session(name)
		if s.Spec.Profile != "coordinator-two" || s.Labels[templates.CoordinatorIdentityLabel] != two {
			t.Fatalf("%s: profile %q, admission %q", who, s.Spec.Profile, s.Labels[templates.CoordinatorIdentityLabel])
		}
		// The recorded launch is one the shared rule admits.
		want := map[string]NamedCoordinatorLauncher{"human": LauncherHuman, "client": LauncherClient, "workbench session": LauncherWorkbenchSession}[who]
		if got := NamedCoordinatorLauncher(s.Annotations[NamedCoordinatorLauncherAnnotation]); got != want || !f.srv.Policy.NamedCoordinatorSessionEligible(s) {
			t.Fatalf("%s: launcher %q (want %q), eligible %v", who, got, want, f.srv.Policy.NamedCoordinatorSessionEligible(s))
		}
		suspendHolder(t, f, name)
	}
	// A workbench session that is itself a child (depth 1) may not launch one:
	// its launch would sit at depth 2.
	deep := workbenchSessionToken(t, f, "haynes-ops-1031-150003")
	deepSess := f.session("haynes-ops-1031-150003")
	deepSess.Labels[v1alpha1.LabelDepth] = "1"
	if err := f.c.Update(context.Background(), deepSess); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, deep, launch), http.StatusForbidden, apiv1.CodeForbidden)

	// An ordinary session may not name the profile (its children run on its own).
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, plain, launch), http.StatusForbidden, apiv1.CodeForbidden)
	// The named coordinator session launches no second delegate: naming any
	// profile is refused.
	for _, profile := range []string{"coordinator", "coordinator-two", "full", "dev"} {
		r := task()
		r.Profile = profile
		wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, lead, r), http.StatusForbidden, apiv1.CodeForbidden)
	}
	// Its child that names no profile runs on the default profile, unlabelled.
	w := f.do(http.MethodPost, apiv1.SessionsPath, lead, task())
	if w.Code != http.StatusCreated {
		t.Fatalf("lead child: %d %s", w.Code, w.Body.String())
	}
	child := f.session(decode[apiv1.Session](t, w).Name)
	if child.Spec.Profile != "" || child.Labels[templates.CoordinatorIdentityLabel] != "" || child.Spec.Parent != "haynes-ops-1031-150002" {
		t.Fatalf("lead child: profile %q, admission %q, parent %q", child.Spec.Profile, child.Labels[templates.CoordinatorIdentityLabel], child.Spec.Parent)
	}
	// A workbench session's own children keep inheriting the workbench
	// profile, unlabelled, as before.
	w = f.do(http.MethodPost, apiv1.SessionsPath, wb, task())
	if w.Code != http.StatusCreated {
		t.Fatalf("workbench child: %d %s", w.Code, w.Body.String())
	}
	if s := f.session(decode[apiv1.Session](t, w).Name); s.Spec.Profile != "workbench" || s.Labels[templates.CoordinatorIdentityLabel] != "" {
		t.Fatalf("workbench child: profile %q, admission %q", s.Spec.Profile, s.Labels[templates.CoordinatorIdentityLabel])
	}
	// The other callers never may.
	for _, k := range []callerKind{kindCoordinator, kindPortalLaunch, kindPortalReader, kindSession} {
		if f.srv.mayLaunchNamedCoordinator(&caller{kind: k}) {
			t.Fatalf("%s may launch a named coordinator identity", k)
		}
	}
}

// A coordinator host launches only on profile dev. If dev ever named a
// coordinator identity, the host is refused rather than given a delegate.
func TestNamedCoordinatorRefusesCoordinatorHosts(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	f.srv.Policy.NamedCoordinatorIdentities = true
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, coordinatorTask()); w.Code != http.StatusCreated {
		t.Fatalf("baseline host create: %d %s", w.Code, w.Body.String())
	}
	p := f.tmpl.Profiles["dev"]
	p.ServiceAccount = leadSA
	f.tmpl.Profiles["dev"] = p
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokCoordinator, coordinatorTask()), http.StatusForbidden, apiv1.CodeForbidden)
}

// Without the policy, or when the templates cannot be read, the API records no
// admission, so the controller runs the session as dev-env-agent.
func TestNamedCoordinatorNoAdmissionWithoutPolicyOrTemplates(t *testing.T) {
	f := newFixture(t)
	withCoordinatorProfile(f)
	f.srv.Policy.NamedCoordinatorIdentities = false
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, coordinatorProfileTask())
	if w.Code != http.StatusCreated {
		t.Fatalf("policy off: %d %s", w.Code, w.Body.String())
	}
	if s := f.session(decode[apiv1.Session](t, w).Name); s.Labels[templates.CoordinatorIdentityLabel] != "" {
		t.Fatal("admission recorded with the policy off")
	}
	f.srv.Policy.NamedCoordinatorIdentities = true
	f.tmplOK = false
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, coordinatorProfileTask())
	if w.Code != http.StatusCreated {
		t.Fatalf("templates unreadable: %d %s", w.Code, w.Body.String())
	}
	if s := f.session(decode[apiv1.Session](t, w).Name); s.Labels[templates.CoordinatorIdentityLabel] != "" {
		t.Fatal("admission recorded without the templates")
	}
}

// One session at a time holds each account. A second launch or a resume waits
// until the holder is Archived or Failed, or Suspended in both phase and
// operating mode with no pod of its own still running. An idempotent repeat of
// the holder's own create still gets the holder back.
func TestNamedCoordinatorOneHolderPerAccount(t *testing.T) {
	f := newFixture(t)
	withCoordinatorProfile(f)
	first := coordinatorProfileTask()
	first.IdempotencyKey = "lead-1"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, first)
	if w.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	a := decode[apiv1.Session](t, w).Name

	// Pending (no phase yet), Running, Idle and Draining all hold the account.
	for _, phase := range []v1alpha1.SessionPhase{"", v1alpha1.PhasePending, v1alpha1.PhaseRunning, v1alpha1.PhaseIdle, v1alpha1.PhaseDraining} {
		setPhase(t, f, a, phase)
		wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokClient, coordinatorProfileTask()), http.StatusConflict, apiv1.CodeConflict)
	}
	// The holder's own retry is answered from the idempotency key, not refused.
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, first); w.Code != http.StatusOK || decode[apiv1.Session](t, w).Name != a {
		t.Fatalf("idempotent repeat: %d %s", w.Code, w.Body.String())
	}
	// Another profile is unaffected.
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, task()); w.Code != http.StatusCreated {
		t.Fatalf("full profile: %d %s", w.Code, w.Body.String())
	}

	// A Suspended phase alone does not free it while the operating mode still
	// says Running: a resume the reconciler has not acted on yet.
	setPhase(t, f, a, v1alpha1.PhaseSuspended)
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokClient, coordinatorProfileTask()), http.StatusConflict, apiv1.CodeConflict)

	// Suspended in both, but a pod it controls still runs (a private-history
	// session reports Suspended while its stop evidence is pending): held.
	suspendHolder(t, f, a)
	sa := f.session(a)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: a, Namespace: sessionNS, UID: uuid.NewUUID(),
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sa, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Spec:   corev1.PodSpec{ServiceAccountName: leadSA, Containers: []corev1.Container{{Name: "agent", Image: "x"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokClient, coordinatorProfileTask()), http.StatusConflict, apiv1.CodeConflict)
	// Once that pod has ended, or is gone, it is free.
	pod.Status.Phase = corev1.PodSucceeded
	if err := f.c.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.namedCoordinatorFree(context.Background(), leadSA, ""); err != nil {
		t.Fatalf("ended pod: %v", err)
	}
	if err := f.c.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}

	// Resume A, then create B before the reconciler acts: refused, because A's
	// operating mode now says Running although its phase is still Suspended.
	if w := f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	if f.session(a).Status.Phase != v1alpha1.PhaseSuspended {
		t.Fatal("the fixture moved the phase")
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokClient, coordinatorProfileTask()), http.StatusConflict, apiv1.CodeConflict)

	// Fully suspended again, it is free; the second launch then holds it.
	if w := f.do(http.MethodPost, apiv1.SessionSuspendPath(a), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatalf("suspend: %d %s", w.Code, w.Body.String())
	}
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, coordinatorProfileTask())
	if w.Code != http.StatusCreated {
		t.Fatalf("second after suspend: %d %s", w.Code, w.Body.String())
	}
	b := decode[apiv1.Session](t, w).Name

	// Resuming the first while the second holds the account is refused.
	wantError(t, f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil), http.StatusConflict, apiv1.CodeConflict)
	if f.session(a).Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("a refused resume changed the operating mode")
	}
	// Archived and Failed free it too.
	for _, phase := range []v1alpha1.SessionPhase{v1alpha1.PhaseArchived, v1alpha1.PhaseFailed} {
		setPhase(t, f, b, phase)
		if err := f.srv.admitNamedCoordinator(context.Background(), f.session(a)); err != nil {
			t.Fatalf("holder %s: %v", phase, err)
		}
	}
	if w := f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatalf("resume after the holder failed: %d %s", w.Code, w.Body.String())
	}
}

// A resume of an admitted session runs its holder check and its patch under the
// create lock, as a create does, so the two cannot interleave on one replica. A
// resume of any other session does not wait for the lock.
func TestNamedCoordinatorResumeTakesTheCreateLock(t *testing.T) {
	f := newFixture(t)
	withCoordinatorProfile(f)
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, coordinatorProfileTask())
	if w.Code != http.StatusCreated {
		t.Fatalf("lead: %d %s", w.Code, w.Body.String())
	}
	lead := decode[apiv1.Session](t, w).Name
	suspendHolder(t, f, lead)
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task())
	if w.Code != http.StatusCreated {
		t.Fatalf("plain: %d %s", w.Code, w.Body.String())
	}
	plain := decode[apiv1.Session](t, w).Name
	suspendHolder(t, f, plain)

	resume := func(name string) <-chan int {
		done := make(chan int, 1)
		go func() {
			req := httptest.NewRequest(http.MethodPost, apiv1.SessionResumePath(name), nil)
			req.Header.Set("Authorization", "Bearer "+tokHuman)
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			done <- rec.Code
		}()
		return done
	}

	f.srv.createMu.Lock()
	// The ordinary session's resume does not take the lock.
	select {
	case code := <-resume(plain):
		if code != http.StatusAccepted {
			f.srv.createMu.Unlock()
			t.Fatalf("plain resume: %d", code)
		}
	case <-time.After(10 * time.Second):
		f.srv.createMu.Unlock()
		t.Fatal("an ordinary resume waited for the create lock")
	}
	// The admitted session's resume waits for it.
	leadDone := resume(lead)
	select {
	case code := <-leadDone:
		f.srv.createMu.Unlock()
		t.Fatalf("the admitted resume finished (%d) while the create lock was held", code)
	case <-time.After(200 * time.Millisecond):
	}
	if f.session(lead).Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		f.srv.createMu.Unlock()
		t.Fatal("the admitted resume patched before it had the lock")
	}
	f.srv.createMu.Unlock()
	select {
	case code := <-leadDone:
		if code != http.StatusAccepted {
			t.Fatalf("admitted resume: %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the admitted resume never finished once the lock was free")
	}
	if f.session(lead).Spec.OperatingMode != v1alpha1.OperatingModeRunning {
		t.Fatal("the admitted resume did not resume")
	}
}

// The standby never runs as a named coordinator identity: a standby request on
// such a profile is refused and creates nothing.
func TestNamedCoordinatorStandbyNeverHoldsTheIdentity(t *testing.T) {
	f := newFixture(t)
	withCoordinatorProfile(f)
	req := apiv1.CreateSessionRequest{Repo: "haynes-ops", Agent: "claude", Mode: "remote", Model: "claude-opus-5-5", Effort: "xhigh", Profile: "coordinator"}
	if name, err := f.srv.CreateStandby(context.Background(), req); err == nil {
		t.Fatalf("the standby took the identity as %s", name)
	}
	var list v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("a refused standby created %d sessions", len(list.Items))
	}
	// Even were the operator's own parent configured as a client, which the
	// shared launch rule would then admit, the standby refuses the identity.
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, StandbyParent)
	if name, err := f.srv.CreateStandby(context.Background(), req); err == nil {
		t.Fatalf("the standby took the identity as %s with its parent a client", name)
	}
	if err := f.c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("a refused standby created %d sessions", len(list.Items))
	}
	// On another profile the standby is created as before.
	req.Profile = "dev"
	if _, err := f.srv.CreateStandby(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

// Only a Claude session runs as a named coordinator identity (DESIGN-042 6.4
// serves the Claude lead): Tom's own Codex session on such a profile is refused.
func TestNamedCoordinatorIsClaudeOnly(t *testing.T) {
	f := newFixture(t)
	withCoordinatorProfile(f)
	codex := apiv1.CreateSessionRequest{Repo: "haynes-ops", Agent: "codex", Mode: "remote", Model: "gpt-6-luna", Profile: "coordinator"}
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, codex), http.StatusForbidden, apiv1.CodeForbidden)
	// The same Codex session on an ordinary profile is created as before.
	codex.Profile = "dev"
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, codex); w.Code != http.StatusCreated {
		t.Fatalf("codex on dev: %d %s", w.Code, w.Body.String())
	}
}

// NamedCoordinatorLaunchAllowed admits exactly three launch shapes: the human
// ServiceAccount at depth 0, a configured client at depth 0, and a depth-0
// workbench session, whose launch sits at depth 1. Every other shape is refused.
func TestNamedCoordinatorLaunchAllowedShapes(t *testing.T) {
	wbRef := sessionNS + "/" + templates.WorkbenchServiceAccount
	p := Policy{Human: humanSA, Clients: []string{clientSA, wbRef}, SessionNamespace: sessionNS,
		WorkbenchServiceAccount: templates.WorkbenchServiceAccount, NamedCoordinatorIdentities: true}
	cases := []struct {
		name     string
		launcher NamedCoordinatorLauncher
		parent   string
		depth    int
		want     bool
	}{
		{"human at depth 0", LauncherHuman, humanSA, 0, true},
		{"human at depth 1", LauncherHuman, humanSA, 1, false},
		{"human claiming a client parent", LauncherHuman, clientSA, 0, false},
		{"client at depth 0", LauncherClient, clientSA, 0, true},
		{"workbench client token at depth 0", LauncherClient, wbRef, 0, true},
		{"client at depth 1", LauncherClient, clientSA, 1, false},
		{"client not configured", LauncherClient, "upgrade-agent/alert-responder", 0, false},
		{"client claiming the human parent", LauncherClient, humanSA, 0, false},
		{"standby parent", LauncherClient, StandbyParent, 0, false},
		{"workbench session at depth 1", LauncherWorkbenchSession, "haynes-ops-1031-160000", 1, true},
		{"workbench session at depth 0", LauncherWorkbenchSession, "haynes-ops-1031-160000", 0, false},
		{"workbench session at depth 2", LauncherWorkbenchSession, "haynes-ops-1031-160000", 2, false},
		{"workbench session with an account parent", LauncherWorkbenchSession, clientSA, 1, false},
		{"workbench session with a malformed parent", LauncherWorkbenchSession, "Not_A_Name", 1, false},
		{"no launcher", "", humanSA, 0, false},
		{"an ordinary session", "session", "haynes-ops-1031-160000", 1, false},
		{"an unknown launcher with a client parent", "session", clientSA, 0, false},
		{"an unknown launcher with the human parent", "portal", humanSA, 0, false},
		{"a coordinator host", "coordinator", coordinatorSA, 0, false},
		{"empty parent", LauncherHuman, "", 0, false},
	}
	for _, c := range cases {
		if got := p.NamedCoordinatorLaunchAllowed(c.launcher, c.parent, c.depth); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// With the policy off, or without a workbench account, nothing (or no
	// workbench session) is admitted.
	off := p
	off.NamedCoordinatorIdentities = false
	if off.NamedCoordinatorLaunchAllowed(LauncherHuman, humanSA, 0) {
		t.Error("admitted with the policy off")
	}
	noWB := p
	noWB.WorkbenchServiceAccount = ""
	if noWB.NamedCoordinatorLaunchAllowed(LauncherWorkbenchSession, "haynes-ops-1031-160000", 1) {
		t.Error("admitted a workbench session with no workbench account")
	}
}

// NamedCoordinatorSessionEligible reads an existing session's record: the
// admission label, the recorded launcher, spec.parent and the depth label, and
// applies the same rule. A missing or malformed piece refuses.
func TestNamedCoordinatorSessionEligible(t *testing.T) {
	p := Policy{Human: humanSA, Clients: []string{clientSA}, SessionNamespace: sessionNS,
		WorkbenchServiceAccount: templates.WorkbenchServiceAccount, NamedCoordinatorIdentities: true}
	record := func(launcher NamedCoordinatorLauncher, parent, depth string) *v1alpha1.AgentSession {
		s := &v1alpha1.AgentSession{
			ObjectMeta: metav1.ObjectMeta{Name: "lead", Namespace: sessionNS,
				Labels:      map[string]string{templates.CoordinatorIdentityLabel: leadSA, v1alpha1.LabelDepth: depth},
				Annotations: map[string]string{NamedCoordinatorLauncherAnnotation: string(launcher)}},
			Spec: v1alpha1.AgentSessionSpec{Parent: parent},
		}
		return s
	}
	for name, c := range map[string]struct {
		s    *v1alpha1.AgentSession
		want bool
	}{
		"human":                  {record(LauncherHuman, humanSA, "0"), true},
		"client":                 {record(LauncherClient, clientSA, "0"), true},
		"workbench session":      {record(LauncherWorkbenchSession, "haynes-ops-1031-160000", "1"), true},
		"deep workbench":         {record(LauncherWorkbenchSession, "haynes-ops-1031-160000", "2"), false},
		"human at depth 1":       {record(LauncherHuman, humanSA, "1"), false},
		"launcher mismatch":      {record(LauncherClient, humanSA, "0"), false},
		"no launcher":            {record("", humanSA, "0"), false},
		"malformed depth":        {record(LauncherHuman, humanSA, "x"), false},
		"padded depth":           {record(LauncherHuman, humanSA, "00"), false},
		"negative depth":         {record(LauncherHuman, humanSA, "-0"), false},
		"no session":             {nil, false},
		"an unconfigured parent": {record(LauncherClient, "upgrade-agent/alert-responder", "0"), false},
	} {
		if got := p.NamedCoordinatorSessionEligible(c.s); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
	noLabel := record(LauncherHuman, humanSA, "0")
	delete(noLabel.Labels, templates.CoordinatorIdentityLabel)
	noDepth := record(LauncherHuman, humanSA, "0")
	delete(noDepth.Labels, v1alpha1.LabelDepth)
	badLabel := record(LauncherHuman, humanSA, "0")
	badLabel.Labels[templates.CoordinatorIdentityLabel] = "dev-env-agent"
	for name, s := range map[string]*v1alpha1.AgentSession{"no admission label": noLabel, "no depth label": noDepth, "not an identity": badLabel} {
		if p.NamedCoordinatorSessionEligible(s) {
			t.Errorf("%s: eligible", name)
		}
	}
}

// A resume rechecks the shared launch rule: an admitted session whose record no
// longer fits it (here a client removed from the policy) is not resumed.
func TestNamedCoordinatorResumeRechecksEligibility(t *testing.T) {
	f := newFixture(t)
	withCoordinatorProfile(f)
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, coordinatorProfileTask())
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	name := decode[apiv1.Session](t, w).Name
	suspendHolder(t, f, name)
	f.srv.Policy.Clients = nil
	wantError(t, f.do(http.MethodPost, apiv1.SessionResumePath(name), tokHuman, nil), http.StatusForbidden, apiv1.CodeForbidden)
	if f.session(name).Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("a refused resume changed the operating mode")
	}
	f.srv.Policy.Clients = []string{clientSA}
	if w := f.do(http.MethodPost, apiv1.SessionResumePath(name), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatalf("eligible resume: %d %s", w.Code, w.Body.String())
	}
}

// --human-service-account and --client-service-accounts may not name a named
// coordinator identity in the session namespace.
func TestNamedCoordinatorPolicyRefusesHumanAndClientOverlap(t *testing.T) {
	base := Policy{Human: humanSA, Clients: []string{clientSA, sessionNS + "/" + templates.WorkbenchServiceAccount},
		SessionNamespace: sessionNS, NamedCoordinatorIdentities: true}
	if err := ValidateNamedCoordinatorPolicy(base); err != nil {
		t.Fatal(err)
	}
	human := base
	human.Human = sessionNS + "/" + leadSA
	clients := base
	clients.Clients = append([]string{}, base.Clients...)
	clients.Clients = append(clients.Clients, sessionNS+"/"+leadSA)
	for name, p := range map[string]Policy{"human": human, "client": clients} {
		if err := ValidateNamedCoordinatorPolicy(p); err == nil {
			t.Fatalf("%s overlap accepted", name)
		}
		p.NamedCoordinatorIdentities = false
		if err := ValidateNamedCoordinatorPolicy(p); err != nil {
			t.Fatalf("%s with the policy off: %v", name, err)
		}
	}
	// The shape in another namespace is not the identity.
	other := base
	other.Clients = append([]string{}, base.Clients...)
	other.Clients = append(other.Clients, "dev/"+leadSA)
	if err := ValidateNamedCoordinatorPolicy(other); err != nil {
		t.Fatal(err)
	}
}
