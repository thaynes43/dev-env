package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

// Only this ephemeral API server is changed. The live snapshot stays byte
// unchanged. Kyverno, live PSA configuration and Cilium are not run by envtest.
func installAdminProposal(t *testing.T) {
	t.Helper()
	suite(t)
	ctx := context.Background()
	dir := filepath.Join("testdata", "proposed-haynes-ops", "admin-lease")
	var old admissionv1.ValidatingAdmissionPolicy
	if err := admin.Get(ctx, client.ObjectKey{Name: "dev-env-broker-guard"}, &old); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "broker-guard.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var proposed unstructured.Unstructured
	for proposed.GetKind() == "" {
		if err := dec.Decode(&proposed.Object); err != nil {
			t.Fatal(err)
		}
	}
	proposed.SetResourceVersion(old.ResourceVersion)
	if err := admin.Update(ctx, &proposed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var current admissionv1.ValidatingAdmissionPolicy
		if err := admin.Get(ctx, client.ObjectKeyFromObject(&old), &current); err != nil {
			t.Error(err)
			return
		}
		current.Spec = old.Spec
		if err := admin.Update(ctx, &current); err != nil {
			t.Error(err)
		}
		for _, o := range []client.Object{
			&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "dev-env-operator-legacy-actor-read"}},
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "dev-env-operator-legacy-actor-read"}},
			&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: "dev-env-broker-admin-issuer"}},
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: "dev-env-broker-admin-issuer"}},
		} {
			if err := admin.Delete(ctx, o); err != nil && !apierrors.IsNotFound(err) {
				t.Error(err)
			}
		}
		for _, o := range []client.Object{&admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-identity-guard"}}, &admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-identity-guard"}}, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.RoleBreakglass}}} {
			if err := admin.Delete(ctx, o); err != nil && !apierrors.IsNotFound(err) {
				t.Error(err)
			}
		}
	})
	for _, file := range []string{filepath.Join(dir, "issuer.yaml"), filepath.Join(dir, "legacy-actor-read.yaml"), filepath.Join(dir, "identity-guard.yaml"), filepath.Join("testdata", "grant-breakglass-RESERVED-PROPOSAL.yaml")} {
		if err := apply(ctx, file); err != nil {
			t.Fatal(err)
		}
	}
}

func adminEnvCase(t *testing.T) (*Broker, *v1alpha1.AccessGrant, *recorder) {
	t.Helper()
	ctx := context.Background()
	actorName := name("rd2-actor")
	actorRef := sessionNS + "/" + actorName
	if err := admin.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: actorName}}); err != nil {
		t.Fatal(err)
	}
	host := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name("rd2-host"), Annotations: map[string]string{v1alpha1.LabelPrefix + "coordinator-host": "rd2-test-host"}}, Spec: corev1.PodSpec{ServiceAccountName: actorName, Containers: []corev1.Container{{Name: "test", Image: "synthetic"}}}}
	if err := admin.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	host.Status.Phase = corev1.PodRunning
	if err := admin.Status().Update(ctx, host); err != nil {
		t.Fatal(err)
	}
	s, _ := newSession(t, func(s *v1alpha1.AgentSession) {
		s.Spec.Parent = actorRef
		s.Labels = map[string]string{v1alpha1.LabelDepth: "0"}
	})
	s.Status.Phase = v1alpha1.PhaseRunning
	if err := admin.Status().Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	grantName := newGrant(t, s, breakglass)
	g := grant(t, grantName)
	bc, err := client.New(brokerCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	inst := &recorder{}
	b := &Broker{Client: bc, APIReader: bc, Clock: clock.RealClock{}, SessionNamespace: sessionNS, PolicyNamespace: systemNS, EnableAdminLeases: true, AdminIssuerNamespace: AdminIssuanceNamespace, GrantDelegates: []string{actorRef}, ReservedDecisionClasses: []string{grantattest.ClassRD2}, Installer: adminNamespaceInstaller{Installer: inst, t: t}}
	step := func() {
		t.Helper()
		if _, err := b.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}); err != nil {
			t.Fatal(err)
		}
	}
	step()
	g = grant(t, grantName)
	// The decision is a test stand-in for the API's authenticated set-once CAS;
	// all actor/grant/requester/reach objects and their UIDs are real envtest objects.
	d := MaintenanceDecision{GrantUID: string(g.UID), Digest: grantattest.ScopeDigest(g), By: DelegateApprover + actorRef, Approve: true, TTL: 30 * time.Minute, At: time.Now().UTC().Truncate(time.Second), Ref: "delegate-decision-current", Reason: "persistent delegated authority", Delegate: &DelegateIdentity{Form: 1, PodName: host.Name, PodUID: string(host.UID), HostID: "rd2-test-host"}}
	d.Reserved, err = NewReservedBinding(ctx, admin, sessionNS, g, d, b.ReservedDecisionClasses, nil, "persistent-authority-reference")
	if err != nil {
		t.Fatal(err)
	}
	d.Reach, err = NewReservedDelegateReach(ctx, admin, sessionNS, g, d.Delegate, d.By, nil, ReservedAuthority(d.Reserved))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	g.Annotations = map[string]string{MaintenanceDecisionAnnotation: string(raw)}
	if err := admin.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	step()
	step()
	g = grant(t, grantName)
	if g.Status.AdminCredential == nil || g.Status.AdminCredential.ServiceAccountUID == "" || g.Status.AdminCredential.BindingUID == "" || g.Status.InstalledPodUID == "" {
		t.Fatalf("not installed: %+v", g.Status)
	}
	t.Cleanup(func() {
		current := grantStatusForAdmin(t, g.Name)
		current.Finalizers = nil
		if err := admin.Update(ctx, current); err != nil {
			t.Error(err)
		}
		if err := admin.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
			t.Error(err)
		}
	})
	return b, g, inst
}

func TestEnvtestReusableAdminIssuer(t *testing.T) {
	installAdminProposal(t)
	ctx := context.Background()
	bc, err := client.New(brokerCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	// Ensure the once-only broker policy is loaded; an old policy would allow
	// this root subject in dev-agents. Never accept a generic RBAC failure here.
	eventually(t, "dedicated root subject guard", func() (bool, string) {
		o := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name("grant-wrong-root")}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: v1alpha1.RoleBreakglass}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: sessionNS, Name: "wrong"}}}
		o.Subjects[0].Name = o.Name
		err := bc.Create(ctx, o, client.DryRunAll)
		return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "dev-env-broker-guard:"), fmt.Sprint(err)
	})
	if !allowed(t, operatorUser, authorizationv1.ResourceAttributes{Namespace: sessionNS, Resource: "serviceaccounts", Name: "dev-env-agent", Verb: "get"}) {
		t.Fatal("operator cannot perform exact legacy actor SA UID recheck")
	}
	for _, a := range []authorizationv1.ResourceAttributes{{Namespace: sessionNS, Resource: "serviceaccounts", Name: "other", Verb: "get"}, {Namespace: sessionNS, Resource: "serviceaccounts", Name: "dev-env-agent", Verb: "delete"}, {Namespace: AdminIssuanceNamespace, Resource: "serviceaccounts", Subresource: "token", Name: "grant-unapproved", Verb: "create"}} {
		if allowed(t, operatorUser, a) {
			t.Fatal("legacy metadata read broadened operator authority", a)
		}
	}
	// Broker RBAC remains unable to mutate the guard and create issuer Pods.
	for _, a := range []authorizationv1.ResourceAttributes{{Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies", Verb: "update"}, {Namespace: AdminIssuanceNamespace, Resource: "pods", Verb: "create"}} {
		if allowed(t, brokerUser, a) {
			t.Fatal("broker acquired broad mutation authority", a)
		}
	}
	for _, account := range []string{"not-a-grant", "dev-env-agent"} {
		err := bc.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: account}}, client.DryRunAll)
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "dev-env-broker-guard:") {
			t.Fatal("broker minted non-grant account", err)
		}
	}
	badRole := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name("grant-wrong-role")}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: v1alpha1.RoleNodes}}
	badRole.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: AdminIssuanceNamespace, Name: badRole.Name}}
	if err := bc.Create(ctx, badRole, client.DryRunAll); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "dev-env-broker-guard:") {
		t.Fatal("non-root catalog subject entered issuer", err)
	}
	b, g, inst := adminEnvCase(t)
	installs, _ := inst.of(g.Name)
	if len(installs) != 1 {
		t.Fatal("expected one real token installation")
	}
	token := installs[0].token
	who := review(t, token)
	if !who.Authenticated || who.User.Username != "system:serviceaccount:"+AdminIssuanceNamespace+":"+g.Name || who.User.UID != g.Status.AdminCredential.ServiceAccountUID {
		t.Fatal("wrong authenticated original issuer identity")
	}
	root := tokenClient(t, token)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: name("rd2-secret")}, Data: map[string][]byte{"synthetic": []byte("synthetic-only")}}
	if err := admin.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if err := root.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{}); err != nil {
		t.Fatal("root resource access failed", err)
	}
	// Both rule types are exercised against the API authorizer. Root wildcard
	// also authorizes a made-up marker: it cannot be an independent fence.
	for _, sar := range []*authorizationv1.SubjectAccessReview{
		{Spec: authorizationv1.SubjectAccessReviewSpec{User: who.User.Username, ResourceAttributes: &authorizationv1.ResourceAttributes{Group: "synthetic.dev-env.test", Resource: "admin-markers", Verb: "use"}}},
		{Spec: authorizationv1.SubjectAccessReviewSpec{User: who.User.Username, NonResourceAttributes: &authorizationv1.NonResourceAttributes{Path: "/metrics", Verb: "get"}}},
	} {
		if err := admin.Create(ctx, sar); err != nil || !sar.Status.Allowed {
			t.Fatal("root wildcard missing", err, sar.Status)
		}
	}
	cfg := rest.AnonymousClientConfig(adminCfg)
	cfg.BearerToken = token
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.Host, "/")+"/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("nonresource root GET", resp.StatusCode)
	}
	privileged := func() client.Object {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: name("rd2-job")}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "test", Image: "synthetic", SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true)}, EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name}}}}}}}}}}
	}
	wantAdmitted(t, root, privileged)
	secretPod := func() client.Object {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: name("rd2-secret-pod")}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "synthetic", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name}}}}}}}}
	}
	wantAdmitted(t, root, secretPod)
	wantAdmitted(t, root, func() client.Object {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: name("rd2-control-write")}}
	})

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name("rd2-exec-node")}}
	if err := admin.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}
	node.Status.DaemonEndpoints.KubeletEndpoint.Port = 1
	if err := admin.Status().Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	target := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: name("rd2-controller")}, Spec: corev1.PodSpec{NodeName: node.Name, Containers: []corev1.Container{{Name: "c", Image: "synthetic"}}}}
	if err := admin.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	execAttempt := func(config *rest.Config) error {
		k, err := kubernetes.NewForConfig(config)
		if err != nil {
			return err
		}
		return k.CoreV1().RESTClient().Post().Namespace(target.Namespace).Resource("pods").Name(target.Name).SubResource("exec").Param("container", "c").Param("command", "true").Param("stdout", "true").Do(ctx).Error()
	}
	if err := execAttempt(cfg); err == nil || !strings.Contains(err.Error(), "Upgrade request required") {
		t.Fatal("root CONNECT did not reach downstream upgrade handler", err)
	}
	// A derived identity and token are intentionally not children of the lease.
	derived := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: name("rd2-derived")}}
	if err := root.Create(ctx, derived); err != nil {
		t.Fatal(err)
	}
	derivedBinding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name("rd2-derived-binding")}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: v1alpha1.RoleBreakglass}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: derived.Namespace, Name: derived.Name}}}
	if err := root.Create(ctx, derivedBinding); err != nil {
		t.Fatal(err)
	}
	derivedRequest := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](600)}}
	if err := root.SubResource("token").Create(ctx, derived, derivedRequest); err != nil {
		t.Fatal(err)
	}
	derivedClient := tokenClient(t, derivedRequest.Status.Token)
	t.Cleanup(func() {
		_ = admin.Delete(ctx, derivedBinding)
		_ = admin.Delete(ctx, derived)
		_ = admin.Delete(ctx, target, client.GracePeriodSeconds(0))
		_ = admin.Delete(ctx, node)
	})

	wantAdmitted(t, root, func() client.Object {
		return &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: name("rd2-role")}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}}
	})
	// Ordinary identities are deliberately given broad RBAC to prove the native
	// mutation guard itself protects issuer objects, independent of RBAC denial.
	for _, account := range []string{"dev-env-agent", name("grant-ordinary")} {
		actor := ordinaryRootFixture(t, account)
		eventually(t, "issuer target protection", func() (bool, string) {
			err := actor.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: name("grant-illicit")}}, client.DryRunAll)
			return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "dev-env-identity-guard:"), fmt.Sprint(err)
		})
		for _, o := range []client.Object{&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: g.Name}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: AdminIssuanceNamespace}}} {
			if err := actor.Delete(ctx, o, client.DryRunAll); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "dev-env-identity-guard:") {
				t.Fatal("ordinary deletion escaped guard", err)
			}
		}
		tr := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](600)}}
		if err := actor.SubResource("token").Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: g.Name}}, tr); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "dev-env-identity-guard:") {
			t.Fatal("ordinary identity minted admin token", err)
		}
		if err := actor.Create(ctx, privileged(), client.DryRunAll); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "dev-env-identity-guard:") {
			t.Fatal("ordinary privileged Job escaped native guard", err)
		}
		if err := actor.Create(ctx, secretPod(), client.DryRunAll); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "dev-env-identity-guard:") {
			t.Fatal("ordinary Secret reference escaped native guard", err)
		}
		if err := execAttempt(impersonate("system:serviceaccount:" + sessionNS + ":" + account)); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "dev-env-identity-guard:") {
			t.Fatal("ordinary controller exec escaped native guard", err)
		}
	}
	// Unapproved issuer identity has neither admin root binding nor mint rights.
	unapproved := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: name("grant-unapproved")}}
	if err := admin.Create(ctx, unapproved); err != nil {
		t.Fatal(err)
	}
	unapprovedCfg := impersonate("system:serviceaccount:" + AdminIssuanceNamespace + ":" + unapproved.Name)
	unapprovedClient, err := client.New(unapprovedCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := unapprovedClient.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{}); !apierrors.IsForbidden(err) {
		t.Fatal("unapproved issuer has root reads", err)
	}
	if err := unapprovedClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: name("grant-unapproved-created")}}); !apierrors.IsForbidden(err) {
		t.Fatal("unapproved issuer can create accounts", err)
	}
	unauthorizedToken := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](600)}}
	if err := unapprovedClient.SubResource("token").Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: g.Name}}, unauthorizedToken); !apierrors.IsForbidden(err) {
		t.Fatal("unapproved issuer can mint admin token", err)
	}
	expired, err := env.ExpiredServiceAccountToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if review(t, expired).Authenticated {
		t.Fatal("expired original token authenticated")
	}
	// Demonstrate the name-based residual, rather than asserting impossible UID
	// containment: a trusted admin can recreate an SA behind a surviving CRB.
	original := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: g.Name}}
	if err := root.Delete(ctx, original); err != nil {
		t.Fatal(err)
	}
	eventually(t, "original token invalidation", func() (bool, string) { return !review(t, token).Authenticated, "original token still authenticates" })
	replacement := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: g.Name}}
	if err := admin.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	tr := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](600)}}
	if err := admin.SubResource("token").Create(ctx, replacement, tr); err != nil {
		t.Fatal(err)
	}
	replacementClient := tokenClient(t, tr.Status.Token)
	if string(replacement.UID) == g.Status.AdminCredential.ServiceAccountUID {
		t.Fatal("replacement did not change UID")
	}
	if err := replacementClient.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{}); err != nil {
		t.Fatal("name-based read residual was not demonstrated", err)
	}
	if err := replacementClient.List(ctx, &corev1.SecretList{}, client.InNamespace("frontend")); err != nil {
		t.Fatal("name-based list residual", err)
	}
	replacementCfg := rest.AnonymousClientConfig(adminCfg)
	replacementCfg.BearerToken = tr.Status.Token
	watchClient, err := client.NewWithWatch(replacementCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := watchClient.Watch(ctx, &corev1.SecretList{}, client.InNamespace("frontend"), &client.ListOptions{Raw: &metav1.ListOptions{TimeoutSeconds: ptr.To[int64](1)}})
	if err != nil {
		t.Fatal("name-based watch residual", err)
	}
	stream.Stop()

	g.Spec.Release = true
	if err := admin.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}); err == nil {
		t.Fatal("replacement cleanup incorrectly claimed success")
	}
	g = grant(t, g.Name)
	if g.Status.AdminCredential.ServiceAccountUID == string(replacement.UID) || !g.Status.AdminCredential.CleanupStarted || g.Status.AdminCredential.CleanupError == "" {
		t.Fatal("replacement UID was adopted or cleanup failure hidden")
	}
	if err := admin.Get(ctx, client.ObjectKey{Name: g.Name}, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatal("original CRB not removed first", err)
	}
	if err := derivedClient.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{}); err != nil {
		t.Fatal("derived authority did not survive original cleanup", err)
	}
	eventually(t, "root binding revocation", func() (bool, string) {
		err := replacementClient.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{})
		return apierrors.IsForbidden(err), fmt.Sprint(err)
	})
	for _, mode := range []string{"release", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			leased, grant, rec := adminEnvCase(t)
			entries, _ := rec.of(grant.Name)
			if len(entries) != 1 {
				t.Fatal("lifecycle token missing")
			}
			if mode == "release" {
				grant.Spec.Release = true
				if err := admin.Update(ctx, grant); err != nil {
					t.Fatal(err)
				}
			} else {
				leased.Clock = clocktesting.NewFakeClock(grant.Status.ExpiresAt.Add(time.Second))
			}
			if _, err := leased.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(grant)}); err != nil {
				t.Fatal(err)
			}
			ended := grantStatusForAdmin(t, grant.Name)
			if !ended.Status.Phase.Ended() || ended.Status.AdminCredential.CleanupError != "" {
				t.Fatal("real lifecycle did not complete cleanup", ended.Status)
			}
			if _, err := leased.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(grant)}); err != nil {
				t.Fatal(err)
			}
			entries, _ = rec.of(grant.Name)
			if len(entries) != 1 {
				t.Fatal("ended real lease reminted")
			}
			eventually(t, "real lifecycle token invalidation", func() (bool, string) {
				return !review(t, entries[0].token).Authenticated, "original token still authenticates"
			})
			for _, o := range []client.Object{&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: grant.Name}}, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: grant.Name}}} {
				if err := admin.Get(ctx, client.ObjectKeyFromObject(o), o); !apierrors.IsNotFound(err) {
					t.Fatal("real lifecycle retained authority", err)
				}
			}
		})
	}
}

func grantStatusForAdmin(t *testing.T, n string) *v1alpha1.AccessGrant { return grant(t, n) }

func ordinaryRootFixture(t *testing.T, account string) client.Client {
	t.Helper()
	ctx := context.Background()
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name("rd2-ordinary-test")}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: v1alpha1.RoleBreakglass}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: sessionNS, Name: account}}}
	if err := admin.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Delete(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
			t.Error(err)
		}
	})
	c, err := client.New(impersonate("system:serviceaccount:"+sessionNS+":"+account), client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type adminNamespaceInstaller struct {
	Installer
	t *testing.T
}

func (i adminNamespaceInstaller) Install(ctx context.Context, pod *corev1.Pod, g *v1alpha1.AccessGrant, token string, expires time.Time) error {
	if pod.Namespace != g.Namespace || pod.Namespace == AdminIssuanceNamespace {
		i.t.Fatal("admin token delivered into issuer or changed namespace")
	}
	return i.Installer.Install(ctx, pod, g, token, expires)
}
func (i adminNamespaceInstaller) Remove(ctx context.Context, pod *corev1.Pod, g *v1alpha1.AccessGrant) error {
	if pod.Namespace != g.Namespace || pod.Namespace == AdminIssuanceNamespace {
		i.t.Fatal("admin token removed from issuer or changed namespace")
	}
	return i.Installer.Remove(ctx, pod, g)
}
