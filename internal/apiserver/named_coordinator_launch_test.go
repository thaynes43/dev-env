package apiserver

import (
	"context"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

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
	// The lead session above is not labelled, so it holds nothing; each
	// admitted launch below is suspended before the next.
	for who, tok := range map[string]string{"human": tokHuman, "client": tokClient, "workbench session": wb} {
		w := f.do(http.MethodPost, apiv1.SessionsPath, tok, coordinatorProfileTask())
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", who, w.Code, w.Body.String())
		}
		name := decode[apiv1.Session](t, w).Name
		s := f.session(name)
		if s.Spec.Profile != "coordinator" || s.Labels[templates.CoordinatorIdentityLabel] != leadSA {
			t.Fatalf("%s: profile %q, admission %q", who, s.Spec.Profile, s.Labels[templates.CoordinatorIdentityLabel])
		}
		setPhase(t, f, name, v1alpha1.PhaseSuspended)
	}

	// An ordinary session may not name the profile (its children run on its own).
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, plain, coordinatorProfileTask()), http.StatusForbidden, apiv1.CodeForbidden)
	// The named coordinator session launches no second delegate: naming any
	// profile is refused.
	for _, profile := range []string{"coordinator", "full", "dev"} {
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
// until the holder is Suspended, Archived or Failed, and an idempotent repeat
// of the holder's own create still gets the holder back.
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

	// Suspended frees it; the second launch then holds it.
	setPhase(t, f, a, v1alpha1.PhaseSuspended)
	sa := f.session(a)
	sa.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := f.c.Update(context.Background(), sa); err != nil {
		t.Fatal(err)
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
