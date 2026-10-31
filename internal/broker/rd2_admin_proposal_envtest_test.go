package broker

// This tests static option (c), not a request-time grant GET or Kyverno.
// All RBAC is deliberately broad for the negative users, so denials must come
// from the proposed identity guard. No tokens or objects reach a live cluster.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestRD2ProposedIdentityGuard(t *testing.T) {
	suite(t)
	ctx := context.Background()
	fixture := filepath.Join("testdata", "proposed-haynes-ops", "rd2")
	for _, f := range []string{"admin-role.yaml", "identity-guard.yaml"} {
		if err := apply(ctx, filepath.Join(fixture, f)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, o := range []client.Object{
			&admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-identity-guard"}},
			&admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-identity-guard"}},
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.RoleBreakglass}},
		} {
			if err := admin.Delete(ctx, o); err != nil && !apierrors.IsNotFound(err) {
				t.Error(err)
			}
		}
	})
	policy := &admissionv1.ValidatingAdmissionPolicy{}
	if err := admin.Get(ctx, client.ObjectKey{Name: "dev-env-identity-guard"}, policy); err != nil {
		t.Fatal(err)
	}
	// Use real envtest Grant, session, Pod and SA UIDs. Setting Active status
	// is a synthetic broker stand-in, not proof of a delegate decision.
	requester, requesterPod := newSession(t, repo("haynes-ops"))
	g := grant(t, newGrant(t, requester, breakglass))
	t.Cleanup(func() {
		_ = admin.Delete(ctx, g)
		_ = admin.Delete(ctx, requesterPod)
		_ = admin.Delete(ctx, requester)
	})
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: g.Name, Labels: map[string]string{v1alpha1.LabelGrant: g.Name}}}
	if err := admin.Create(ctx, sa); err != nil {
		t.Fatal(err)
	}
	approvedUser := "system:serviceaccount:dev-agents:" + sa.Name
	broad := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name("rd2-test")}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: v1alpha1.RoleBreakglass}, Subjects: []rbacv1.Subject{
		{Kind: "ServiceAccount", Namespace: sessionNS, Name: sa.Name},
		{Kind: "ServiceAccount", Namespace: sessionNS, Name: "grant-rd2-proposal"},
		{Kind: "ServiceAccount", Namespace: sessionNS, Name: "grant-unapproved"},
		{Kind: "ServiceAccount", Namespace: sessionNS, Name: "grant-existing-other"},
		{Kind: "ServiceAccount", Namespace: sessionNS, Name: "dev-env-agent"},
	}}
	if err := admin.Create(ctx, broad); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(ctx, broad); _ = admin.Delete(ctx, sa) })
	actor := func(user string, uid types.UID) (client.Client, *rest.Config) {
		cfg := rest.CopyConfig(adminCfg)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: user, UID: string(uid), Groups: []string{"system:serviceaccounts", "system:serviceaccounts:dev-agents", "system:authenticated"}}
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return c, cfg
	}
	// Mint a synthetic envtest token: the API server supplies request.userInfo.uid
	// from the real SA object, not a header supplied by the proposal compiler.
	token := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](1800)}}
	if err := admin.SubResource("token").Create(ctx, sa, token); err != nil {
		t.Fatal(err)
	}
	cfg := rest.AnonymousClientConfig(adminCfg)
	cfg.BearerToken = token.Status.Token
	approved, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	// The positive compiler read uses the real API, including an Active grant.
	now := time.Now()
	g.Status = v1alpha1.AccessGrantStatus{
		Phase: v1alpha1.GrantActive, ServiceAccount: sa.Name, InstalledPodUID: string(requesterPod.UID),
		ApprovedBy: "delegate/dev-agents/synthetic-reviewer", ApprovedAt: rd2Time(now),
		ApprovedTTL: &metav1.Duration{Duration: 30 * time.Minute}, ExpiresAt: rd2Time(now.Add(30 * time.Minute)),
	}
	if err := admin.Status().Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	bound := rd2ProposalBinding{GrantName: g.Name, GrantUID: g.UID, ServiceAccountUID: sa.UID,
		Requester: requester.Name, RequesterUID: requester.UID, PodUID: requesterPod.UID}
	exemption, err := rd2ProposalExemption(ctx, true, admin, bound, now)
	if err != nil {
		t.Fatal(err)
	}
	// Negative snapshot variants do not mutate the actual lease or claim that
	// static admission itself watches AccessGrants.
	g = g.DeepCopy()
	objects := []client.Object{g, sa.DeepCopy(), requester.DeepCopy(), requesterPod.DeepCopy()}
	setExemption := func(expr string) {
		t.Helper()
		if err := admin.Get(ctx, client.ObjectKeyFromObject(policy), policy); err != nil {
			t.Fatal(err)
		}
		policy.Spec.MatchConditions[0].Expression = "request.userInfo.username.startsWith('system:serviceaccount:dev-agents:') && request.userInfo.username != 'system:serviceaccount:dev-agents:dev-env-workbench' && !(" + expr + ")"
		if err := admin.Update(ctx, policy); err != nil {
			t.Fatal(err)
		}
	}
	job := func() client.Object {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: name("rd2-job")}, Spec: batchv1.JobSpec{ActiveDeadlineSeconds: ptr.To[int64](600), TTLSecondsAfterFinished: ptr.To[int32](600), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "c", Image: "synthetic.invalid/image", SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true)}}}}}}}
	}
	secretPod := func() client.Object {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: name("rd2-secret")}, Spec: corev1.PodSpec{ActiveDeadlineSeconds: ptr.To[int64](600), Containers: []corev1.Container{{Name: "c", Image: "synthetic.invalid/image", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "synthetic-secret"}}}}}}}}
	}
	role := func() client.Object {
		return &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: name("rd2-role")}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}}}}
	}
	controlWrite := func() client.Object {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: name("rd2-control")}}
	}
	operations := []struct {
		name string
		make func() client.Object
	}{{"privileged Job", job}, {"Secret Pod", secretPod}, {"RBAC", role}, {"control namespace write", controlWrite}}
	// A downstream exec error after CONNECT admission is observable without a
	// kubelet. Never claim an actual remote command or open stream was tested.
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name("rd2-node")}}
	if err := admin.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}
	node.Status.DaemonEndpoints.KubeletEndpoint.Port = 1
	if err := admin.Status().Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	target := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: name("rd2-controller")}, Spec: corev1.PodSpec{NodeName: node.Name, Containers: []corev1.Container{{Name: "c", Image: "synthetic.invalid/image"}}}}
	if err := admin.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(ctx, target); _ = admin.Delete(ctx, node) })
	execErr := func(config *rest.Config) error {
		k, err := kubernetes.NewForConfig(config)
		if err != nil {
			return err
		}
		return k.CoreV1().RESTClient().Post().Namespace(target.Namespace).Resource("pods").Name(target.Name).SubResource("exec").Param("container", "c").Param("command", "true").Param("stdout", "true").Do(ctx).Error()
	}
	denied := func(t *testing.T, c client.Client, config *rest.Config) {
		t.Helper()
		for _, op := range operations {
			err := c.Create(ctx, op.make(), client.DryRunAll)
			if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "dev-env-identity-guard") {
				t.Fatalf("%s: want identity guard Deny, got %v", op.name, err)
			}
		}
		if err := execErr(config); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "dev-env-identity-guard") {
			t.Fatalf("exec: want identity guard Deny, got %v", err)
		}
	}
	// Wait for initial Deny, so a positive response cannot merely mean the
	// asynchronously installed policy has not started enforcing yet.
	eventually(t, "initial proposed guard enforced", func() (bool, string) {
		err := approved.Create(ctx, role(), client.DryRunAll)
		return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "dev-env-identity-guard"), fmt.Sprint(err)
	})
	fixtureActor, _ := actor("system:serviceaccount:dev-agents:grant-rd2-proposal", "rd2-proposal-sa-uid")
	eventually(t, "literal fixture exemption observed", func() (bool, string) {
		err := fixtureActor.Create(ctx, role(), client.DryRunAll)
		return err == nil, fmt.Sprint(err)
	})
	setExemption(exemption)
	eventually(t, "RD2 exemption observed", func() (bool, string) {
		err := approved.Create(ctx, role(), client.DryRunAll)
		return err == nil, fmt.Sprint(err)
	})
	for _, op := range operations {
		if err := approved.Create(ctx, op.make(), client.DryRunAll); err != nil {
			t.Fatalf("approved %s: %v", op.name, err)
		}
	}
	if err := execErr(cfg); err == nil || !strings.Contains(err.Error(), "Upgrade request required") {
		t.Fatalf("approved exec must reach downstream upgrade handler, got %v", err)
	}
	// Discovery and diagnostics are genuinely authorized by nonResourceURLs.
	for _, url := range []string{"/api", "/apis", "/version", "/healthz", "/livez", "/readyz", "/openapi/v2", "/metrics"} {
		k, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := k.CoreV1().RESTClient().Get().AbsPath(url).Do(ctx).Error(); err != nil {
			t.Fatalf("admin nonResourceURL %s: %v", url, err)
		}
	}
	for _, tc := range []struct {
		name, user string
		uid        types.UID
	}{
		{"unapproved", "system:serviceaccount:dev-agents:grant-unapproved", "other-uid"},
		{"baseline", "system:serviceaccount:dev-agents:dev-env-agent", "baseline-uid"},
		{"same name new UID", approvedUser, "replacement-uid"},
		{"existing other grant", "system:serviceaccount:dev-agents:grant-existing-other", "another-uid"},
	} {
		t.Run(tc.name, func(t *testing.T) { c, config := actor(tc.user, tc.uid); denied(t, c, config) })
	}
	for _, state := range []string{"revoked", "expired", "lookup failure", "grant absent"} {
		t.Run(state, func(t *testing.T) {
			switch state {
			case "revoked":
				g.Status.Phase = v1alpha1.GrantReleased
			case "expired":
				g.Status.Phase = v1alpha1.GrantActive
				g.Status.ExpiresAt = rd2Time(now)
			case "lookup failure":
				g.Status.ExpiresAt = rd2Time(now.Add(30 * time.Minute))
			}
			var reader client.Reader = rd2Fake(objects...)
			if state == "lookup failure" {
				reader = rd2FailedReader{reader}
			}
			if state == "grant absent" {
				reader = rd2Fake(objects[1:]...)
			}
			expr, err := rd2ProposalExemption(ctx, true, reader, bound, now)
			if err == nil || expr != "false" {
				t.Fatalf("must render no exemption: %q %v", expr, err)
			}
			setExemption(expr)
			eventually(t, "exemption removed", func() (bool, string) {
				err := approved.Create(ctx, role(), client.DryRunAll)
				return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "dev-env-identity-guard"), fmt.Sprint(err)
			})
			denied(t, approved, cfg)
		})
	}
	// A stale static exception cannot approve a recreated SA of the same name.
	// Leave the broad name-based CRB in place to isolate the UID admission fence.
	setExemption(exemption)
	eventually(t, "stale original exemption restored", func() (bool, string) {
		err := approved.Create(ctx, role(), client.DryRunAll)
		return err == nil, fmt.Sprint(err)
	})
	if err := admin.Delete(ctx, sa); err != nil {
		t.Fatal(err)
	}
	eventually(t, "original envtest token revoked", func() (bool, string) {
		err := approved.Create(ctx, role(), client.DryRunAll)
		return apierrors.IsUnauthorized(err), fmt.Sprint(err)
	})
	replacementSA := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: sa.Name}}
	if err := admin.Create(ctx, replacementSA); err != nil {
		t.Fatal(err)
	}
	if replacementSA.UID == sa.UID {
		t.Fatal("recreated SA must have a new UID")
	}
	replacementToken := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](1800)}}
	if err := admin.SubResource("token").Create(ctx, replacementSA, replacementToken); err != nil {
		t.Fatal(err)
	}
	replacementCfg := rest.AnonymousClientConfig(adminCfg)
	replacementCfg.BearerToken = replacementToken.Status.Token
	replacementClient, err := client.New(replacementCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "replacement token authenticated but UID refused", func() (bool, string) {
		err := replacementClient.Create(ctx, role(), client.DryRunAll)
		return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "dev-env-identity-guard"), fmt.Sprint(err)
	})
	denied(t, replacementClient, replacementCfg)

}
