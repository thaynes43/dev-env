package broker

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

func podExecAskFixture(t *testing.T) (*grantAskFixture, *corev1.Pod) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "fixture",
			Name:      strings.Repeat(strings.Repeat("p", 63)+".", 3) + strings.Repeat("p", 61),
			UID:       "11111111-1111-1111-1111-111111111111",
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}}},
	}
	f := newGrantAskFixture(t, true, pod)
	if err := f.base.Delete(f.ctx, f.g); err != nil {
		t.Fatal(err)
	}
	g := f.g.DeepCopy()
	g.Name, g.ResourceVersion = "grant-pod-exec-fixture", ""
	g.Spec.Type, g.Spec.Kube = v1alpha1.GrantPodExec, nil
	g.Spec.PodExec = &v1alpha1.PodExecGrant{Namespace: pod.Namespace, Pod: pod.Name, Container: "app", PodUID: string(pod.UID)}
	g.Spec.TTL.Duration = 20 * time.Minute
	if err := f.base.Create(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.g = g
	return f, pod
}

func TestPodExecAskApprovalAndCleanup(t *testing.T) {
	for _, end := range []string{"expiry", "release", "session ended", "pod replaced"} {
		t.Run(end, func(t *testing.T) {
			f, pod := podExecAskFixture(t)
			f.openAsk()
			ask := f.onlyAsk().Spec.Grant
			if ask.Type != "pod-exec" || ask.Target != pod.Name || ask.TTL != "20m0s" ||
				ask.Detail != "Open a shell in container app in namespace fixture" || ask.ScopeDigest != grantattest.ScopeDigest(f.g) {
				t.Fatalf("wrong scope on the Ask: %+v", ask)
			}
			f.answer(ptr.To(true), "approver", "")
			f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.ServiceAccount == g.Name })
			g := f.grant()
			if g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != "authentik/approver" {
				t.Fatalf("grant status %+v", g.Status)
			}
			role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: g.Name}}
			binding := rb(pod.Namespace, g.Name)
			acct := sa(g.Name)
			for _, o := range []client.Object{role, binding, acct} {
				if err := f.base.Get(f.ctx, client.ObjectKeyFromObject(o), o); err != nil {
					t.Fatal(err)
				}
			}
			wantRules := []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}, ResourceNames: []string{pod.Name}},
				{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}, ResourceNames: []string{pod.Name}},
			}
			if !reflect.DeepEqual(role.Rules, wantRules) ||
				binding.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: g.Name}) ||
				!reflect.DeepEqual(binding.Subjects, []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: sessionNS, Name: g.Name}}) {
				t.Fatalf("unexpected role or binding: %+v %+v", role.Rules, binding)
			}
			wantPhase := v1alpha1.GrantReleased
			switch end {
			case "expiry":
				f.clk.Step(20 * time.Minute)
				wantPhase = v1alpha1.GrantExpired
			case "release":
				g.Spec.Release = true
				if err := f.base.Update(f.ctx, g); err != nil {
					t.Fatal(err)
				}
			case "session ended":
				if err := f.base.Delete(f.ctx, f.session); err != nil {
					t.Fatal(err)
				}
			case "pod replaced":
				replacePodExecFixture(t, f, pod)
				wantPhase = v1alpha1.GrantFailed
			}
			f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == wantPhase })
			for _, o := range []client.Object{role, binding, acct} {
				if err := f.base.Get(f.ctx, client.ObjectKeyFromObject(o), o); !apierrors.IsNotFound(err) {
					t.Fatalf("expected cleanup of %T, got %v", o, err)
				}
			}
		})
	}
}

func replacePodExecFixture(t *testing.T, f *grantAskFixture, pod *corev1.Pod) {
	t.Helper()
	if err := f.base.Delete(f.ctx, pod); err != nil {
		t.Fatal(err)
	}
	replacement := pod.DeepCopy()
	replacement.ResourceVersion = ""
	replacement.UID = "33333333-3333-3333-3333-333333333333"
	if err := f.base.Create(f.ctx, replacement); err != nil {
		t.Fatal(err)
	}
}

func TestPodExecApprovalRefusals(t *testing.T) {
	f, pod := podExecAskFixture(t)
	if err := f.b.Decide(f.ctx, f.g.Name, Decision{Approve: true, By: "approver"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("unrouted approval: %v", err)
	}
	p := &v1alpha1.GrantPolicy{Spec: v1alpha1.GrantPolicySpec{
		Type: v1alpha1.GrantPodExec, Profiles: []string{f.g.Spec.Requester.Profile}, MaxTTL: f.g.Spec.TTL,
	}}
	if Matches(p, f.g) {
		t.Fatal("pod-exec matched a standing policy")
	}
	p.Spec.Attested, p.Spec.Profiles = &v1alpha1.AttestedRequesters{Profiles: p.Spec.Profiles}, nil
	if MatchesAttested(p, f.g) {
		t.Fatal("pod-exec matched an attested policy")
	}
	f.openAsk()
	replacePodExecFixture(t, f, pod)
	f.answer(ptr.To(true), "approver", "")
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
	if err := f.base.Get(f.ctx, client.ObjectKeyFromObject(sa(f.g.Name)), &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
		t.Fatalf("replaced pod provisioned a ServiceAccount: %v", err)
	}
}
