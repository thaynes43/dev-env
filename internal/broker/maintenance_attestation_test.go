package broker

// Attested maintenance approval tests (ADR-013): a maintenance grant approved
// through an attested GrantPolicy with an exact maintenance scope, on the
// requesting agent's attestation. The attestation is a claim, not verified human
// identity. Fake-client units in the style of maintenance_test.go and
// attestation_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

const (
	amGPU     = "GPU-d8a856f1-f955-f683-bc24-654561496774"
	amOtherGP = "GPU-00000000-1111-2222-3333-444444444444"
	amImage   = "ghcr.io/thaynes43/comfyui:0.39.0-2@sha256:991867f5dafc1eddc9ed70b307d8bf1d2d91c50f53a428a6315e8fd7dbe1a61b"
	amNode    = "talosw01"
	amNodeUID = "b7ef4b0a-524b-411b-9b47-79e3c37e7269"
	amPolicy  = "maintenance-card-c-attested"
	amHuman   = "human/dev-env-system/dev-env-human"
	amOther   = "99999999-9999-9999-9999-999999999999"
)

type amFixture struct {
	t    *testing.T
	b    *Broker
	c    client.Client
	clk  *clocktesting.FakeClock
	key  types.NamespacedName
	sess *v1alpha1.AgentSession
	pod  *corev1.Pod
}

// amCase builds a Card C shaped maintenance grant, an attested policy that
// matches it exactly, the coordinator and one worker (sessions and their
// Running pods), and the target node. edit changes the grant and the policy
// before they are stored. All three flags are on.
func amCase(t *testing.T, edit ...func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy)) *amFixture {
	t.Helper()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	coord := v1alpha1.MaintenanceIdentity{Name: "coord", UID: "coord-uid", PodUID: "coord-pod"}
	g := &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "grant-maintenance-c", UID: "grant-uid-c", CreationTimestamp: metav1.NewTime(now)},
		Spec: v1alpha1.AccessGrantSpec{
			Type:      v1alpha1.GrantMaintenance,
			Requester: v1alpha1.GrantRequester{Session: coord.Name, SessionUID: types.UID(coord.UID), Repo: "haynes-ops", Profile: "full", Agent: v1alpha1.AgentClaude},
			TTL:       metav1.Duration{Duration: 10 * time.Minute}, Reason: "set Card C's power limit",
			Maintenance: &v1alpha1.MaintenanceTask{
				RuntimeProfile: "nvidia-utility", Coordinator: coord,
				Node: amNode, NodeUID: amNodeUID, Resource: amGPU, Operation: "nvidia-smi power limit",
				Capabilities: []string{"SYS_ADMIN"}, Image: amImage, CPUMillis: 100, MemoryMiB: 128,
				Restore: "read back and restore the prior power limit",
			},
		},
	}
	p := &v1alpha1.GrantPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: amPolicy},
		Spec: v1alpha1.GrantPolicySpec{
			Description: "test", Type: v1alpha1.GrantMaintenance,
			Attested: &v1alpha1.AttestedRequesters{Profiles: []string{"full"}, Repos: []string{"haynes-ops"}},
			Maintenance: &v1alpha1.MaintenancePolicy{
				Node: amNode, NodeUID: amNodeUID, Resource: amGPU, RuntimeProfile: "nvidia-utility",
				Capabilities: []string{"SYS_ADMIN"}, Image: amImage, MaxCPUMillis: 100, MaxMemoryMiB: 128,
			},
			MaxTTL: metav1.Duration{Duration: 10 * time.Minute},
		},
	}
	for _, e := range edit {
		e(g, p)
	}
	objects := []client.Object{g, p, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: amNode, UID: amNodeUID}}}
	f := &amFixture{t: t, key: client.ObjectKeyFromObject(g)}
	for _, id := range []v1alpha1.MaintenanceIdentity{coord, {Name: "worker", UID: "worker-uid", PodUID: "worker-pod"}} {
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: id.Name, UID: types.UID(id.UID)}}
		if id.Name == "worker" {
			s.Spec.Parent = coord.Name
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: id.Name, UID: types.UID(id.PodUID), Labels: map[string]string{v1alpha1.LabelSession: id.Name},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
		if id.Name == coord.Name {
			f.sess, f.pod = s, pod
		}
		objects = append(objects, s, pod)
	}
	sc := runtime.NewScheme()
	if err := errors.Join(v1alpha1.AddToScheme(sc), corev1.AddToScheme(sc), networkingv1.AddToScheme(sc), coordinationv1.AddToScheme(sc)); err != nil {
		t.Fatal(err)
	}
	// The fake client assigns no UIDs; the executor's stop patch tests one.
	assignUID := interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if obj.GetUID() == "" {
			obj.SetUID(types.UID("uid-" + obj.GetName()))
		}
		return c.Create(ctx, obj, opts...)
	}}
	f.c = fake.NewClientBuilder().WithScheme(sc).WithStatusSubresource(&v1alpha1.AccessGrant{}, &corev1.Pod{}).WithObjects(objects...).WithInterceptorFuncs(assignUID).Build()
	f.clk = clocktesting.NewFakeClock(now)
	f.b = &Broker{Client: f.c, APIReader: f.c, SessionNamespace: sessionNS, PolicyNamespace: systemNS, Clock: f.clk,
		EnableMaintenanceGrants: true, EnableAttestedGrants: true, EnableAttestedMaintenance: true}
	return f
}

func (f *amFixture) step(n int) {
	f.t.Helper()
	for range n {
		if _, err := f.b.Reconcile(context.Background(), ctrl.Request{NamespacedName: f.key}); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *amFixture) grant() *v1alpha1.AccessGrant {
	f.t.Helper()
	var g v1alpha1.AccessGrant
	if err := f.c.Get(context.Background(), f.key, &g); err != nil {
		f.t.Fatal(err)
	}
	return &g
}

func (f *amFixture) update(obj client.Object) {
	f.t.Helper()
	if err := f.c.Update(context.Background(), obj); err != nil {
		f.t.Fatal(err)
	}
}

func (f *amFixture) policy() *v1alpha1.GrantPolicy {
	f.t.Helper()
	var p v1alpha1.GrantPolicy
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: amPolicy}, &p); err != nil {
		f.t.Fatal(err)
	}
	return &p
}

// statement is what the operator's attestation route would write for the
// stored grant, from the coordinator's current pod.
func (f *amFixture) statement(edit ...func(*grantattest.Statement)) grantattest.Statement {
	g := f.grant()
	a, _ := grantattest.ParseApprover("coordinator:meatball")
	st := grantattest.Statement{V: grantattest.Version, Attestor: grantattest.Attestor{Session: f.sess.Name, SessionUID: string(f.sess.UID), PodUID: string(f.pod.UID)},
		Approver: a, Ref: "msg-card-c", GrantUID: string(g.UID), ScopeDigest: grantattest.ScopeDigest(g), TTL: g.Spec.TTL.Duration.String(), AttestedAt: f.clk.Now().Truncate(time.Second)}
	for _, e := range edit {
		e(&st)
	}
	return st
}

func (f *amFixture) annotate(key, value string) {
	f.t.Helper()
	g := f.grant()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[key] = value
	f.update(g)
}

func (f *amFixture) attest(st grantattest.Statement) {
	f.t.Helper()
	raw, err := st.Encode()
	if err != nil {
		f.t.Fatal(err)
	}
	f.annotate(grantattest.Annotation, raw)
}

// human records the authenticated human route's bound decision, as the
// operator's maintenance-decision route writes it.
func (f *amFixture) human(approve bool, edit ...func(*MaintenanceDecision)) {
	f.t.Helper()
	g := f.grant()
	d := MaintenanceDecision{GrantUID: string(g.UID), Digest: MaintenanceDigest(g), By: amHuman, Approve: approve, At: f.clk.Now()}
	for _, e := range edit {
		e(&d)
	}
	raw, _ := json.Marshal(d)
	f.annotate(MaintenanceDecisionAnnotation, string(raw))
}

func (f *amFixture) executorExists() bool {
	f.t.Helper()
	var p corev1.Pod
	err := f.c.Get(context.Background(), f.key, &p)
	if err != nil && !apierrors.IsNotFound(err) {
		f.t.Fatal(err)
	}
	return err == nil
}

// pendingThenAttest takes the grant to Pending, records a valid attestation
// and runs the broker twice.
func (f *amFixture) pendingThenAttest(edit ...func(*grantattest.Statement)) *v1alpha1.AccessGrant {
	f.t.Helper()
	f.step(1)
	if g := f.grant(); g.Status.Phase != v1alpha1.GrantPending {
		f.t.Fatalf("not pending: %+v", g.Status)
	}
	f.attest(f.statement(edit...))
	f.step(1)
	return f.grant()
}

func wantPolicyApproved(t *testing.T, g *v1alpha1.AccessGrant) {
	t.Helper()
	if g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != PolicyApprover+amPolicy || g.Status.ApprovedTTL == nil || g.Status.ApprovedTTL.Duration != g.Spec.TTL.Duration {
		t.Fatalf("not approved by the attested policy: %+v", g.Status)
	}
}

func wantWaiting(t *testing.T, f *amFixture) {
	t.Helper()
	f.step(1)
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || g.Status.ApprovedAt != nil || f.executorExists() {
		t.Fatalf("approved or ended: %+v executor %v", g.Status, f.executorExists())
	}
}

// A valid attestation through an exact attested policy approves, for the
// grant's TTL from now, and the next pass makes the executor.
func TestAttestedMaintenanceApproves(t *testing.T) {
	f := amCase(t)
	g := f.pendingThenAttest()
	wantPolicyApproved(t, g)
	if !g.Status.ApprovedAt.Time.Equal(f.clk.Now()) || !g.Status.ExpiresAt.Time.Equal(f.clk.Now().Add(10*time.Minute)) {
		t.Fatalf("lease not from approval: %+v", g.Status)
	}
	f.step(1)
	if !f.executorExists() {
		t.Fatal("no executor after an attested approval")
	}
	var np networkingv1.NetworkPolicy
	if err := f.c.Get(context.Background(), f.key, &np); err != nil {
		t.Fatalf("no deny-all network policy: %v", err)
	}
	msg := grantattest.Message(amPolicy, f.statement())
	for _, want := range []string{"agent attestation, not verified human identity", "policy/" + amPolicy, "coordinator:meatball"} {
		if !strings.Contains(msg, want) {
			t.Errorf("audit message %q lacks %q", msg, want)
		}
	}
}

// Workers, devices and read-only mounts approve only when the policy names
// each one exactly.
func TestAttestedMaintenanceNamedListsApprove(t *testing.T) {
	worker := v1alpha1.MaintenanceIdentity{Name: "worker", UID: "worker-uid", PodUID: "worker-pod"}
	for name, edit := range map[string]func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy){
		"worker": func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.Workers = []v1alpha1.MaintenanceIdentity{worker}
			p.Spec.Maintenance.Workers = []v1alpha1.MaintenanceIdentity{worker}
		},
		"device on the generic path": func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.RuntimeProfile, g.Spec.Maintenance.Devices = "", []string{"/dev/nvidia0"}
			p.Spec.Maintenance.RuntimeProfile, p.Spec.Maintenance.Devices = "", []string{"/dev/nvidiactl", "/dev/nvidia0"}
		},
		"read-only mount": func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.ReadOnlyMounts = []string{"/var/lib/maintenance-inputs/limit"}
			p.Spec.Maintenance.ReadOnlyMounts = []string{"/var/lib/maintenance-inputs/limit"}
		},
		"narrower than the caps": func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.CPUMillis, g.Spec.Maintenance.MemoryMiB, g.Spec.Maintenance.Capabilities = 50, 64, nil
			p.Spec.Maintenance.Capabilities = []string{"SYS_ADMIN", "SYS_RAWIO"}
			p.Spec.MaxTTL.Duration = time.Hour
		},
	} {
		t.Run(name, func(t *testing.T) {
			wantPolicyApproved(t, amCase(t, edit).pendingThenAttest())
		})
	}
}

// Each mismatch between the grant's envelope and the policy's scope leaves the
// grant waiting. The unmutated case approves, so a broker that ignored the
// scope would fail here.
func TestAttestedMaintenanceScopeMismatchRefused(t *testing.T) {
	wantPolicyApproved(t, amCase(t).pendingThenAttest())
	for name, edit := range map[string]func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy){
		"node":           func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) { p.Spec.Maintenance.Node = "talosw02" },
		"node UID":       func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) { p.Spec.Maintenance.NodeUID = amOther },
		"resource":       func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) { p.Spec.Maintenance.Resource = amOtherGP },
		"runtimeProfile": func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) { p.Spec.Maintenance.RuntimeProfile = "" },
		"image digest": func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.Maintenance.Image = amImage[:len(amImage)-5] + "00000" // still a well-formed digest
		},
		"image repository": func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.Maintenance.Image = strings.Replace(amImage, "comfyui", "comfyui-dev", 1)
		},
		"image tag only": func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.Maintenance.Image = "ghcr.io/thaynes43/comfyui:0.39.0-2"
		},
		"capability not in the policy": func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.Capabilities = []string{"SYS_ADMIN", "SYS_RAWIO"}
		},
		"capability with an empty policy list": func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) { p.Spec.Maintenance.Capabilities = nil },
		"cpu over the cap":                     func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) { g.Spec.Maintenance.CPUMillis = 101 },
		"memory over the cap":                  func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) { g.Spec.Maintenance.MemoryMiB = 129 },
		"ttl over maxTTL":                      func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) { g.Spec.TTL.Duration = 11 * time.Minute },
		"worker not named": func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.Workers = []v1alpha1.MaintenanceIdentity{{Name: "worker", UID: "worker-uid", PodUID: "worker-pod"}}
		},
		"worker named with another pod": func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.Workers = []v1alpha1.MaintenanceIdentity{{Name: "worker", UID: "worker-uid", PodUID: "worker-pod"}}
			p.Spec.Maintenance.Workers = []v1alpha1.MaintenanceIdentity{{Name: "worker", UID: "worker-uid", PodUID: "older-pod"}}
		},
		"device not named": func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.RuntimeProfile, g.Spec.Maintenance.Devices = "", []string{"/dev/nvidia0"}
			p.Spec.Maintenance.RuntimeProfile = ""
		},
		"read-only mount not named": func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.ReadOnlyMounts = []string{"/var/lib/maintenance-inputs/limit"}
		},
		"profile not named": func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.Attested.Profiles = []string{"workbench"}
		},
		"repo not named": func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) { p.Spec.Attested.Repos = []string{"dev-env"} },
		"no maintenance scope": func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.Maintenance = nil
		},
		"policy of another type": func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.Type = v1alpha1.GrantCredential
			p.Spec.Credential = &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{v1alpha1.CredentialProxmox}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := amCase(t, edit)
			f.pendingThenAttest()
			wantWaiting(t, f)
		})
	}
}

// A missing, forged, misdirected or stale attestation never approves, nor does
// one whose session pod changed before approval. The valid case approves.
func TestAttestedMaintenanceAttestationFailures(t *testing.T) {
	wantPolicyApproved(t, amCase(t).pendingThenAttest())
	for _, tc := range []struct {
		name  string
		setup func(*amFixture)
	}{
		{"no attestation", func(f *amFixture) {}},
		{"forged session", func(f *amFixture) {
			f.attest(f.statement(func(s *grantattest.Statement) { s.Attestor.Session = "worker" }))
		}},
		{"other session UID", func(f *amFixture) {
			f.attest(f.statement(func(s *grantattest.Statement) { s.Attestor.SessionUID = amOther }))
		}},
		{"another session's statement", func(f *amFixture) {
			f.attest(f.statement(func(s *grantattest.Statement) {
				s.Attestor = grantattest.Attestor{Session: "worker", SessionUID: "worker-uid", PodUID: "worker-pod"}
			}))
		}},
		{"other grant", func(f *amFixture) {
			f.attest(f.statement(func(s *grantattest.Statement) { s.GrantUID = amOther }))
		}},
		{"forged digest", func(f *amFixture) {
			f.attest(f.statement(func(s *grantattest.Statement) { s.ScopeDigest = strings.Repeat("A", 43) }))
		}},
		{"stale digest after an envelope change", func(f *amFixture) {
			st := f.statement()
			g := f.grant()
			g.Spec.Maintenance.Operation = "something else" // the store does not enforce immutability here
			f.update(g)
			f.attest(st)
		}},
		{"stale digest after a restore text change", func(f *amFixture) {
			st := f.statement()
			g := f.grant()
			g.Spec.Maintenance.Restore = "no restore"
			f.update(g)
			f.attest(st)
		}},
		{"statement from another pod", func(f *amFixture) {
			f.attest(f.statement(func(s *grantattest.Statement) { s.Attestor.PodUID = amOther }))
		}},
		{"pod changed before approval", func(f *amFixture) {
			st := f.statement()
			ctx := context.Background()
			if err := f.c.Delete(ctx, f.pod); err != nil {
				t.Fatal(err)
			}
			rolled := f.pod.DeepCopy()
			rolled.ResourceVersion, rolled.UID = "", "coord-pod-2"
			if err := f.c.Create(ctx, rolled); err != nil {
				t.Fatal(err)
			}
			f.attest(st)
		}},
		{"predates the grant", func(f *amFixture) {
			f.attest(f.statement(func(s *grantattest.Statement) { s.AttestedAt = f.clk.Now().Add(-time.Hour) }))
		}},
		{"malformed", func(f *amFixture) { f.annotate(grantattest.Annotation, `{"v":1`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := amCase(t)
			f.step(1)
			tc.setup(f)
			wantWaiting(t, f)
			wantWaiting(t, f)
		})
	}
}

// The human route's identity checks also gate the attested route.
func TestAttestedMaintenanceIdentityChecks(t *testing.T) {
	ctx := context.Background()
	worker := v1alpha1.MaintenanceIdentity{Name: "worker", UID: "worker-uid", PodUID: "worker-pod"}
	withWorker := func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
		g.Spec.Maintenance.Workers = []v1alpha1.MaintenanceIdentity{worker}
		p.Spec.Maintenance.Workers = []v1alpha1.MaintenanceIdentity{worker}
	}
	wantPolicyApproved(t, amCase(t, withWorker).pendingThenAttest())
	for name, setup := range map[string]func(*amFixture){
		"worker not a direct child": func(f *amFixture) {
			var s v1alpha1.AgentSession
			if err := f.c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: "worker"}, &s); err != nil {
				t.Fatal(err)
			}
			s.Spec.Parent = "someone-else"
			f.update(&s)
		},
		"node replaced": func(f *amFixture) {
			if err := f.c.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: amNode}}); err != nil {
				t.Fatal(err)
			}
			if err := f.c.Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: amNode, UID: amOther}}); err != nil {
				t.Fatal(err)
			}
		},
		"control-plane node": func(f *amFixture) {
			var n corev1.Node
			if err := f.c.Get(ctx, types.NamespacedName{Name: amNode}, &n); err != nil {
				t.Fatal(err)
			}
			n.Labels = map[string]string{"node-role.kubernetes.io/control-plane": ""}
			f.update(&n)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := amCase(t, withWorker)
			setup(f)
			f.pendingThenAttest()
			wantWaiting(t, f)
		})
	}
}

// Default off: with any of the three flags off, a valid attestation and an
// exact policy approve nothing. With either attested flag off the grant ends up
// exactly as a broker without this route leaves it; with maintenance off it is
// denied as before. All on, the same request approves.
func TestAttestedMaintenanceDefaultOff(t *testing.T) {
	run := func(t *testing.T, flags func(*Broker)) v1alpha1.AccessGrantStatus {
		f := amCase(t)
		flags(f.b)
		f.step(1)
		f.attest(f.statement())
		f.step(2)
		st := f.grant().Status
		if st.Phase != v1alpha1.GrantActive && f.executorExists() {
			t.Fatal("executor made without an approval")
		}
		return st
	}
	// Today's behaviour: the attested maintenance route does not exist.
	today := run(t, func(b *Broker) { b.EnableAttestedMaintenance = false })
	if today.Phase != v1alpha1.GrantPending || today.ApprovedBy != "" || today.NotifiedAt != nil {
		t.Fatalf("baseline: %+v", today)
	}
	for name, flags := range map[string]func(*Broker){
		"attested grants off": func(b *Broker) { b.EnableAttestedGrants = false },
		"both attested off":   func(b *Broker) { b.EnableAttestedGrants, b.EnableAttestedMaintenance = false, false },
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(t, flags); !reflect.DeepEqual(got, today) {
				t.Fatalf("status %+v differs from the route-off baseline %+v", got, today)
			}
		})
	}
	t.Run("maintenance off", func(t *testing.T) {
		got := run(t, func(b *Broker) { b.EnableMaintenanceGrants = false })
		if got.Phase != v1alpha1.GrantDenied || got.DeniedBy != DeniedByBroker || got.ApprovedBy != "" {
			t.Fatalf("status %+v", got)
		}
	})
	t.Run("all on approves", func(t *testing.T) {
		got := run(t, func(*Broker) {})
		if got.Phase != v1alpha1.GrantActive || got.ApprovedBy != PolicyApprover+amPolicy {
			t.Fatalf("status %+v", got)
		}
	})
	b := &Broker{}
	if b.EnableAttestedMaintenance || b.EnableAttestedGrants || b.EnableMaintenanceGrants {
		t.Fatal("a zero broker has a maintenance route on")
	}
}

// No standing policy approves maintenance, whatever its scope and whatever the
// schema did. The same scope under spec.attested does.
func TestStandingPolicyNeverApprovesMaintenance(t *testing.T) {
	standing := func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
		p.Spec.Profiles, p.Spec.Repos, p.Spec.Attested = p.Spec.Attested.Profiles, p.Spec.Attested.Repos, nil
	}
	f := amCase(t, standing)
	f.pendingThenAttest()
	wantWaiting(t, f)
	g, p := f.grant(), f.policy()
	if Matches(p, g) || MatchesAttested(p, g) || Match(g, []v1alpha1.GrantPolicy{*p}) != nil {
		t.Fatal("a standing maintenance policy matched")
	}
	mixed := p.DeepCopy()
	mixed.Spec.Attested = &v1alpha1.AttestedRequesters{Profiles: []string{"full"}}
	if Matches(mixed, g) || MatchesAttested(mixed, g) {
		t.Fatal("a mixed maintenance policy matched")
	}
	// A standing policy of another type never reaches a maintenance grant.
	kube := kubePolicy("kube", []string{v1alpha1.RoleNodes})
	if Matches(&kube, g) {
		t.Fatal("a kube policy matched a maintenance grant")
	}
	attested := amCase(t)
	wantPolicyApproved(t, attested.pendingThenAttest())
	if Matches(attested.policy(), attested.grant()) {
		t.Fatal("an attested maintenance policy matched as a standing one")
	}
}

// The human route is checked first and is unchanged: a bound human decision,
// approve or deny, wins over a valid attestation, and a malformed one is
// refused without consulting any policy.
func TestAttestedMaintenanceHumanRouteFirst(t *testing.T) {
	t.Run("human deny wins", func(t *testing.T) {
		f := amCase(t)
		f.step(1)
		f.attest(f.statement())
		f.human(false)
		f.step(1)
		if g := f.grant(); g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != amHuman || g.Status.ApprovedBy != "" {
			t.Fatalf("status %+v", g.Status)
		}
	})
	t.Run("human approve wins", func(t *testing.T) {
		f := amCase(t)
		f.step(1)
		f.attest(f.statement())
		f.human(true)
		f.step(1)
		if g := f.grant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != amHuman {
			t.Fatalf("status %+v", g.Status)
		}
		// Execution after a human approval does not depend on the attested route.
		f.b.EnableAttestedMaintenance = false
		f.step(1)
		if !f.executorExists() {
			t.Fatal("human-approved executor not made")
		}
	})
	t.Run("malformed human decision blocks the attested route", func(t *testing.T) {
		f := amCase(t)
		f.step(1)
		f.attest(f.statement())
		f.human(true, func(d *MaintenanceDecision) { d.Digest = "stale" })
		if _, err := f.b.Reconcile(context.Background(), ctrl.Request{NamespacedName: f.key}); !errors.Is(err, ErrRefused) {
			t.Fatalf("want the human route's refusal, got %v", err)
		}
		if g := f.grant(); g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" {
			t.Fatalf("status %+v", g.Status)
		}
	})
	t.Run("without a human decision the attestation approves", func(t *testing.T) {
		wantPolicyApproved(t, amCase(t).pendingThenAttest())
	})
}

// A policy approval is execution authority only while it holds: the next pass
// ends the lease, before any executor exists, when the policy is gone or no
// longer fits, the statement no longer verifies, or a flag is off.
func TestAttestedMaintenanceExecutionRecheck(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		change func(*amFixture)
		ends   bool
	}{
		"unchanged": {func(*amFixture) {}, false},
		"policy deleted": {func(f *amFixture) {
			if err := f.c.Delete(ctx, f.policy()); err != nil {
				t.Fatal(err)
			}
		}, true},
		"policy narrowed": {func(f *amFixture) {
			p := f.policy()
			p.Spec.Maintenance.MaxMemoryMiB = 64
			f.update(p)
		}, true},
		"policy made standing": {func(f *amFixture) {
			p := f.policy()
			p.Spec.Profiles, p.Spec.Attested = []string{"full"}, nil
			f.update(p)
		}, true},
		"attestation removed": {func(f *amFixture) {
			g := f.grant()
			delete(g.Annotations, grantattest.Annotation)
			f.update(g)
		}, true},
		"attested maintenance off": {func(f *amFixture) { f.b.EnableAttestedMaintenance = false }, true},
		"attested grants off":      {func(f *amFixture) { f.b.EnableAttestedGrants = false }, true},
	} {
		t.Run(name, func(t *testing.T) {
			f := amCase(t)
			wantPolicyApproved(t, f.pendingThenAttest())
			tc.change(f)
			f.step(1)
			g := f.grant()
			if tc.ends {
				if g.Status.Phase != v1alpha1.GrantReleased || !strings.Contains(g.Status.Message, "maintenance approval no longer holds") || f.executorExists() {
					t.Fatalf("lease not ended before execution: %+v executor %v", g.Status, f.executorExists())
				}
				var np networkingv1.NetworkPolicy
				if err := f.c.Get(ctx, f.key, &np); !apierrors.IsNotFound(err) {
					t.Fatalf("network policy made or left: %v", err)
				}
				return
			}
			if g.Status.Phase != v1alpha1.GrantActive || !f.executorExists() {
				t.Fatalf("lease did not proceed: %+v", g.Status)
			}
		})
	}
	// While the executor runs, a later failure starts the existing stop path.
	f := amCase(t)
	wantPolicyApproved(t, f.pendingThenAttest())
	f.step(1)
	if !f.executorExists() {
		t.Fatal("no executor")
	}
	if err := f.c.Delete(ctx, f.policy()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.Reconcile(ctx, ctrl.Request{NamespacedName: f.key}); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantActive || g.Status.Maintenance == nil || g.Status.Maintenance.Executor != "StopPending" {
		t.Fatalf("running executor not asked to stop: %+v", g.Status)
	}
}
