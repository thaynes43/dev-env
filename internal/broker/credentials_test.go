package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

type credentialFixture struct {
	t       *testing.T
	b       *Broker
	c       client.WithWatch
	g       *v1alpha1.AccessGrant
	s       *v1alpha1.AgentSession
	pod     *corev1.Pod
	clk     *clocktesting.FakePassiveClock
	creates int
}

func credentialCase(t *testing.T) *credentialFixture {
	t.Helper()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	f := &credentialFixture{t: t, clk: clocktesting.NewFakePassiveClock(now)}
	f.s = &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "s-credential", UID: "11111111-1111-1111-1111-111111111111"}}
	f.g = &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "grant-credential", UID: "22222222-2222-2222-2222-222222222222", CreationTimestamp: metav1.NewTime(now), Finalizers: []string{Finalizer}, Labels: map[string]string{v1alpha1.LabelSession: f.s.Name}},
		Spec: v1alpha1.AccessGrantSpec{
			Requester: v1alpha1.GrantRequester{Session: f.s.Name, SessionUID: f.s.UID, Profile: "full", Repo: "haynes-ops", Agent: v1alpha1.AgentClaude},
			Type:      v1alpha1.GrantCredential, Credential: &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialProxmox}, TTL: metav1.Duration{Duration: time.Hour}, Reason: "maintain a guest",
		},
	}
	f.pod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: f.s.Name, UID: "33333333-3333-3333-3333-333333333333", Labels: map[string]string{v1alpha1.LabelSession: f.s.Name}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(f.s, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	p := &v1alpha1.GrantPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: "pve-full"}, Spec: v1alpha1.GrantPolicySpec{Profiles: []string{"full"}, Type: v1alpha1.GrantCredential, Credential: &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{v1alpha1.CredentialProxmox}}, MaxTTL: metav1.Duration{Duration: time.Hour}}}
	sc := runtime.NewScheme()
	if err := errors.Join(v1alpha1.AddToScheme(sc), corev1.AddToScheme(sc)); err != nil {
		t.Fatal(err)
	}
	f.c = fake.NewClientBuilder().WithScheme(sc).WithStatusSubresource(&v1alpha1.AccessGrant{}, &v1alpha1.CredentialJob{}, &corev1.Pod{}).WithObjects(f.s, f.g, f.pod, p).Build()
	c := interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if _, ok := o.(*v1alpha1.CredentialJob); !ok {
			t.Fatalf("credential grant tried to create %T", o)
		}
		f.creates++
		o.SetUID("44444444-4444-4444-4444-444444444444")
		o.SetCreationTimestamp(metav1.NewTime(f.clk.Now()))
		return c.Create(ctx, o, opts...)
	}})
	f.b = &Broker{Client: c, APIReader: f.c, SessionNamespace: sessionNS, PolicyNamespace: systemNS, EnableProxmoxGrants: true, Clock: f.clk}
	return f
}

func (f *credentialFixture) step() (ctrl.Result, error) {
	f.t.Helper()
	return f.b.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.g)})
}

func (f *credentialFixture) getGrant() *v1alpha1.AccessGrant {
	f.t.Helper()
	var g v1alpha1.AccessGrant
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.g), &g); err != nil {
		f.t.Fatal(err)
	}
	return &g
}

func (f *credentialFixture) job() *v1alpha1.CredentialJob {
	f.t.Helper()
	var j v1alpha1.CredentialJob
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: v1alpha1.CredentialJobName(f.g.UID)}, &j); err != nil {
		f.t.Fatal(err)
	}
	return &j
}

func (f *credentialFixture) start() {
	f.t.Helper()
	if _, err := f.step(); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.step(); err != nil {
		f.t.Fatal(err)
	}
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != "policy/pve-full" {
		f.t.Fatalf("policy approval: %+v", g.Status)
	}
	if _, err := f.step(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *credentialFixture) receipt(phase v1alpha1.CredentialJobPhase) {
	f.t.Helper()
	j := f.job()
	now := metav1.NewTime(f.clk.Now())
	j.Status = v1alpha1.CredentialJobStatus{Phase: phase, ProviderID: "dev-env@pve!grant-" + string(f.g.UID), InstalledPodUID: string(f.pod.UID), InstalledAt: &now}
	if phase == v1alpha1.CredentialRevoked {
		j.Status.RevokedAt = &now
	}
	if err := f.c.Status().Update(context.Background(), j); err != nil {
		f.t.Fatal(err)
	}
}

func TestCredentialBrokerApprovalGate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*credentialFixture)
		phase v1alpha1.GrantPhase
	}{
		{"feature disabled", func(f *credentialFixture) { f.b.EnableProxmoxGrants = false }, v1alpha1.GrantDenied},
		{"general SSH disabled", func(f *credentialFixture) {
			f.g.Spec.Credential.Name = v1alpha1.CredentialHWSSH
			if err := f.c.Update(context.Background(), f.g); err != nil {
				t.Fatal(err)
			}
		}, v1alpha1.GrantDenied},
		{"no matching policy", func(f *credentialFixture) {
			var p v1alpha1.GrantPolicy
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: "pve-full"}, &p); err != nil {
				t.Fatal(err)
			}
			p.Spec.Profiles = []string{"dev"}
			if err := f.c.Update(context.Background(), &p); err != nil {
				t.Fatal(err)
			}
		}, v1alpha1.GrantPending},
		{"request lacks session fence", func(f *credentialFixture) {
			f.g.Spec.Requester.SessionUID = ""
			if err := f.c.Update(context.Background(), f.g); err != nil {
				t.Fatal(err)
			}
		}, v1alpha1.GrantDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := credentialCase(t)
			tc.edit(f)
			if _, err := f.step(); err != nil {
				t.Fatal(err)
			}
			if _, err := f.step(); err != nil {
				t.Fatal(err)
			}
			if g := f.getGrant(); g.Status.Phase != tc.phase || f.creates != 0 {
				t.Fatalf("gate: %+v, creates=%d", g.Status, f.creates)
			}
			if tc.phase == v1alpha1.GrantPending {
				if err := f.b.Decide(context.Background(), f.g.Name, Decision{Approve: true, By: "authentik/owner"}); !errors.Is(err, ErrRefused) {
					t.Fatalf("human credential approval accepted: %v", err)
				}
			}
		})
	}
}

func TestCredentialBrokerSessionReplacementBeforeObservation(t *testing.T) {
	f := credentialCase(t)
	if err := f.c.Delete(context.Background(), f.s); err != nil {
		t.Fatal(err)
	}
	f.s.ResourceVersion = ""
	f.s.UID = "55555555-5555-5555-5555-555555555555"
	if err := f.c.Create(context.Background(), f.s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.step(); err != nil {
		t.Fatal(err)
	}
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantDenied || f.creates != 0 {
		t.Fatalf("replacement inherited request: %+v, creates=%d", g.Status, f.creates)
	}
}

func TestCredentialBrokerReceiptNeverApprovesPendingGrant(t *testing.T) {
	f := credentialCase(t)
	var p v1alpha1.GrantPolicy
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: "pve-full"}, &p); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	j := &v1alpha1.CredentialJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: v1alpha1.CredentialJobName(f.g.UID), UID: "44444444-4444-4444-4444-444444444444", Labels: grantLabels(f.g)},
		Spec: v1alpha1.CredentialJobSpec{
			Grant:      v1alpha1.CredentialObjectReference{Namespace: sessionNS, Name: f.g.Name, UID: f.g.UID},
			Session:    v1alpha1.CredentialObjectReference{Namespace: sessionNS, Name: f.s.Name, UID: f.s.UID},
			Credential: v1alpha1.CredentialProxmox, ExpiresAt: metav1.NewTime(f.clk.Now().Add(time.Hour)),
		},
		Status: v1alpha1.CredentialJobStatus{Phase: v1alpha1.CredentialInstalled, ProviderID: "dev-env@pve!grant-" + string(f.g.UID), InstalledPodUID: string(f.pod.UID)},
	}
	if err := f.c.Create(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if _, err := f.step(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.step(); err != nil {
		t.Fatal(err)
	}
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || g.Status.InstalledPodUID != "" {
		t.Fatalf("receipt acted as approval: %+v", g.Status)
	}
}

func TestCredentialBrokerReceiptAndPodReplacement(t *testing.T) {
	f := credentialCase(t)
	f.start()
	f.receipt(v1alpha1.CredentialInstalled)
	if _, err := f.step(); err != nil {
		t.Fatal(err)
	}
	if g := f.getGrant(); g.Status.InstalledPodUID != string(f.pod.UID) {
		t.Fatalf("valid receipt ignored: %+v", g.Status)
	}
	if err := f.c.Delete(context.Background(), f.pod); err != nil {
		t.Fatal(err)
	}
	f.pod.ResourceVersion = ""
	f.pod.UID = "66666666-6666-6666-6666-666666666666"
	if err := f.c.Create(context.Background(), f.pod); err != nil {
		t.Fatal(err)
	}
	if _, err := f.step(); err != nil {
		t.Fatal(err)
	}
	if g := f.getGrant(); g.Status.InstalledPodUID != "" {
		t.Fatal("stale receipt survived pod replacement")
	}
	f.receipt(v1alpha1.CredentialInstalled)
	if _, err := f.step(); err != nil {
		t.Fatal(err)
	}
	if g := f.getGrant(); g.Status.InstalledPodUID != string(f.pod.UID) || f.creates != 1 {
		t.Fatalf("replacement installation: %+v, creates=%d", g.Status, f.creates)
	}
}

func TestCredentialBrokerRejectsForgedReceipts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.CredentialJob)
		main bool
	}{
		{"wrong phase", func(j *v1alpha1.CredentialJob) { j.Status.Phase = v1alpha1.CredentialPending }, false},
		{"wrong pod", func(j *v1alpha1.CredentialJob) { j.Status.InstalledPodUID = "foreign-pod" }, false},
		{"wrong provider", func(j *v1alpha1.CredentialJob) { j.Status.ProviderID = "dev-env@pve!unrelated" }, false},
		{"future receipt", func(j *v1alpha1.CredentialJob) {
			j.Status.InstalledAt = &metav1.Time{Time: j.Status.InstalledAt.Add(time.Minute)}
		}, false},
		{"old receipt", func(j *v1alpha1.CredentialJob) {
			j.Status.InstalledAt = &metav1.Time{Time: j.Status.InstalledAt.Add(-time.Minute)}
		}, false},
		{"wrong grant UID", func(j *v1alpha1.CredentialJob) { j.Spec.Grant.UID = "77777777-7777-7777-7777-777777777777" }, true},
		{"wrong session UID", func(j *v1alpha1.CredentialJob) { j.Spec.Session.UID = "77777777-7777-7777-7777-777777777777" }, true},
		{"changed expiry", func(j *v1alpha1.CredentialJob) { j.Spec.ExpiresAt = metav1.NewTime(j.Spec.ExpiresAt.Add(time.Minute)) }, true},
		{"wrong ownership", func(j *v1alpha1.CredentialJob) { j.Labels[v1alpha1.LabelManagedBy] = "foreign" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := credentialCase(t)
			f.start()
			f.receipt(v1alpha1.CredentialInstalled)
			j := f.job()
			tc.edit(j)
			var err error
			if tc.main {
				err = f.c.Update(context.Background(), j)
			} else {
				err = f.c.Status().Update(context.Background(), j)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.step()
			if g := f.getGrant(); g.Status.InstalledPodUID != "" || g.Status.Phase != v1alpha1.GrantActive {
				t.Fatalf("forged receipt affected approval or installation: %+v", g.Status)
			}
		})
	}
}

func TestCredentialBrokerReplacementJobCannotRevoke(t *testing.T) {
	f := credentialCase(t)
	f.start()
	j := f.job()
	if err := f.c.Delete(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	j.ResourceVersion = ""
	j.UID = "77777777-7777-7777-7777-777777777777"
	if err := f.c.Create(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	f.receipt(v1alpha1.CredentialRevoked)
	g := f.getGrant()
	g.Spec.Release = true
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if _, err := f.step(); err == nil {
		t.Fatal("replacement receipt was accepted")
	}
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.EndedAt != nil {
		t.Fatalf("replacement claimed cleanup: %+v", g.Status)
	}
	if f.job().Spec.Release {
		t.Fatal("broker mutated replacement job")
	}
}

func TestCredentialBrokerRequiresLiveOwnedRunningPod(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*corev1.Pod)
		status bool
	}{
		{"foreign controller", func(p *corev1.Pod) { p.OwnerReferences[0].UID = "55555555-5555-5555-5555-555555555555" }, false},
		{"wrong session label", func(p *corev1.Pod) { p.Labels[v1alpha1.LabelSession] = "other-session" }, false},
		{"hold pod", func(p *corev1.Pod) { p.Labels[v1alpha1.LabelHold] = "true" }, false},
		{"not running", func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := credentialCase(t)
			f.start()
			f.receipt(v1alpha1.CredentialInstalled)
			tc.edit(f.pod)
			var err error
			if tc.status {
				err = f.c.Status().Update(context.Background(), f.pod)
			} else {
				err = f.c.Update(context.Background(), f.pod)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.step(); err != nil {
				t.Fatal(err)
			}
			if g := f.getGrant(); g.Status.InstalledPodUID != "" {
				t.Fatalf("receipt installed in unsuitable pod: %+v", g.Status)
			}
		})
	}
}

func TestCredentialBrokerWaitsForCleanup(t *testing.T) {
	for _, cause := range []string{"release", "expiry", "session end", "backend failure", "feature disabled", "delete"} {
		t.Run(cause, func(t *testing.T) {
			f := credentialCase(t)
			f.start()
			f.receipt(v1alpha1.CredentialInstalled)
			want := v1alpha1.GrantReleased
			switch cause {
			case "release":
				g := f.getGrant()
				g.Spec.Release = true
				if err := f.c.Update(context.Background(), g); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				f.clk.SetTime(f.clk.Now().Add(time.Hour))
				want = v1alpha1.GrantExpired
			case "session end":
				if err := f.c.Delete(context.Background(), f.s); err != nil {
					t.Fatal(err)
				}
			case "backend failure":
				j := f.job()
				j.Status.Phase = v1alpha1.CredentialCleanupPending
				j.Status.FailureCode = v1alpha1.CredentialAmbiguousMint
				if err := f.c.Status().Update(context.Background(), j); err != nil {
					t.Fatal(err)
				}
				want = v1alpha1.GrantFailed
			case "feature disabled":
				f.b.EnableProxmoxGrants = false
				g := f.getGrant()
				g.Spec.Release = true
				if err := f.c.Update(context.Background(), g); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := f.c.Delete(context.Background(), f.getGrant()); err != nil {
					t.Fatal(err)
				}
			}
			if res, err := f.step(); err != nil || res.RequeueAfter == 0 {
				t.Fatalf("cleanup wait: %+v %v", res, err)
			}
			if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.EndedAt != nil || len(g.Finalizers) == 0 {
				t.Fatalf("cleanup claimed early: %+v", g)
			}
			if !f.job().Spec.Release {
				t.Fatal("cleanup did not request one-way release")
			}
			f.receipt(v1alpha1.CredentialRevoked)
			if _, err := f.step(); err != nil {
				t.Fatal(err)
			}
			if cause == "delete" {
				var g v1alpha1.AccessGrant
				if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.g), &g); err == nil {
					t.Fatal("confirmed cleanup did not lift grant finalizer")
				}
			} else if g := f.getGrant(); g.Status.Phase != want {
				t.Fatalf("cleanup result %+v, want %s", g.Status, want)
			}
		})
	}
}

func TestCredentialBrokerRestartAndLostCreateResponse(t *testing.T) {
	f := credentialCase(t)
	original := f.b.Client
	f.b.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, _ client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if err := original.Create(ctx, o, opts...); err != nil {
			return err
		}
		return errors.New("create response lost")
	}})
	if _, err := f.step(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.step(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.step(); err == nil {
		t.Fatal("lost response was not retryable")
	}
	f.b = &Broker{Client: original, APIReader: f.c, SessionNamespace: sessionNS, PolicyNamespace: systemNS, Clock: f.clk, EnableProxmoxGrants: true}
	if _, err := f.step(); err != nil || f.creates != 1 {
		t.Fatalf("restart recreated credential job: %v, creates=%d", err, f.creates)
	}
	f.receipt(v1alpha1.CredentialInstalled)
	if _, err := f.step(); err != nil {
		t.Fatal(err)
	}
	if g := f.getGrant(); g.Status.InstalledPodUID != string(f.pod.UID) {
		t.Fatal("restart did not accept validated receipt")
	}
}

func TestCredentialBrokerBackpressureAndOutage(t *testing.T) {
	f := credentialCase(t)
	f.start()
	j := f.job()
	j.Status.Phase = v1alpha1.CredentialPending
	j.Status.FailureCode = v1alpha1.CredentialBackendUnavailable
	if err := f.c.Status().Update(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if res, err := f.step(); err != nil || res.RequeueAfter == 0 {
		t.Fatalf("bounded retry: %+v %v", res, err)
	}
	if f.creates != 1 || f.job().Spec.Release || f.getGrant().Status.Phase != v1alpha1.GrantActive {
		t.Fatal("backpressure changed approval or created a second job")
	}
	f.b.APIReader = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if _, ok := o.(*v1alpha1.CredentialJob); ok {
			return errors.New("keeper job API unavailable")
		}
		return c.Get(ctx, key, o, opts...)
	}})
	g := f.getGrant()
	g.Spec.Release = true
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if _, err := f.step(); err == nil {
		t.Fatal("job API outage was ignored")
	}
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.EndedAt != nil {
		t.Fatal("outage claimed early revocation")
	}
}
