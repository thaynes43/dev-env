package broker

// The envtest case for the proposed admission policy dev-env-grant-secret-names
// (DESIGN-042 5.1, P5) and the role it guards, dev-env-grant-namespace-operator
// (P4). Neither is in haynes-ops yet: both are proposals, kept in
// testdata/proposed-haynes-ops/ beside the verbatim copies of testdata/haynes-ops/.
// The identity guard's `use` clause (P5's other delta) is not installed here, so
// this proves the named-Secret rule on its own.
//
// No broker runs in this case. The test makes each grant identity by hand, as the
// broker would (a ServiceAccount named after the grant and RoleBindings to the
// catalog role), sets the grant's phase, and sends every pod-bearing request as a
// server-side dry run: admission runs and nothing is stored.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const secretNamesPolicy = "dev-env-grant-secret-names"

var (
	proposedOnce sync.Once
	proposedErr  error
)

// proposed installs the proposed haynes-ops objects once, after the suite's.
func proposed(t *testing.T) {
	t.Helper()
	suite(t)
	proposedOnce.Do(func() {
		files, err := filepath.Glob(filepath.Join("testdata", "proposed-haynes-ops", "*.yaml"))
		if err != nil {
			proposedErr = err
			return
		}
		if len(files) != 2 {
			proposedErr = fmt.Errorf("want the 2 proposed haynes-ops files, found %v", files)
			return
		}
		for _, f := range files {
			if err := apply(context.Background(), f); err != nil {
				proposedErr = err
				return
			}
		}
	})
	if proposedErr != nil {
		t.Fatalf("proposed haynes-ops objects: %v", proposedErr)
	}
}

// grantIdentity makes a grant as the broker would leave it: the AccessGrant in
// phase, its ServiceAccount, and a RoleBinding to its role in each of bindIn
// (which a test may make wider than the grant's own namespaces). It returns a
// client that acts as that ServiceAccount.
func grantIdentity(t *testing.T, kube v1alpha1.KubeGrant, phase v1alpha1.GrantPhase, bindIn ...string) (string, client.Client) {
	t.Helper()
	ctx := context.Background()
	n := name("grant-s5")
	g := &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: n},
		Spec: v1alpha1.AccessGrantSpec{
			Requester: v1alpha1.GrantRequester{Session: "s5-session", Repo: "haynes-ops", Profile: "full", Agent: v1alpha1.AgentClaude},
			Type:      v1alpha1.GrantKube, Kube: &kube,
			TTL:    metav1.Duration{Duration: 30 * time.Minute},
			Reason: "DESIGN-042 S5 envtest",
		},
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: n}}
	objs := []client.Object{g, sa}
	for _, ns := range bindIn {
		objs = append(objs, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: n},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: kube.Role},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Namespace: sessionNS, Name: n}},
		})
	}
	for _, o := range objs {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatalf("create %T %s: %v", o, o.GetName(), err)
		}
	}
	t.Cleanup(func() {
		for _, o := range objs {
			if err := admin.Delete(context.Background(), o); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("delete %T %s: %v", o, o.GetName(), err)
			}
		}
	})
	setPhase(t, n, phase)
	cfg := rest.CopyConfig(adminCfg)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:" + sessionNS + ":" + n,
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:" + sessionNS, "system:authenticated"},
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return n, c
}

func setPhase(t *testing.T, n string, phase v1alpha1.GrantPhase) {
	t.Helper()
	g := grant(t, n)
	g.Status.Phase = phase
	if err := admin.Status().Update(context.Background(), g); err != nil {
		t.Fatalf("set %s to %s: %v", n, phase, err)
	}
}

// secretJob is a one-shot Job in ns whose container takes each named Secret
// one way: an env key, envFrom, a volume or a projected volume, in turn.
func secretJob(ns string, secrets ...string) *batchv1.Job {
	c := corev1.Container{Name: "op", Image: "synthetic"}
	var vols []corev1.Volume
	for i, s := range secrets {
		switch i % 4 {
		case 0:
			c.Env = append(c.Env, corev1.EnvVar{Name: fmt.Sprintf("V%d", i), ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: s}, Key: "k"},
			}})
		case 1:
			c.EnvFrom = append(c.EnvFrom, corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: s}}})
		case 2:
			vols = append(vols, corev1.Volume{Name: fmt.Sprintf("v%d", i), VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: s}}})
		case 3:
			vols = append(vols, corev1.Volume{Name: fmt.Sprintf("v%d", i), VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: s}}}},
			}}})
		}
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name("s5-job")},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{c}, Volumes: vols,
		}}},
	}
}

// describeObject names a test object in a failure message.
func describeObject(o client.Object) string { return fmt.Sprintf("%T in %s", o, o.GetNamespace()) }

// refusedByPolicy reports whether err is dev-env-grant-secret-names' refusal.
func refusedByPolicy(err error) bool {
	return apierrors.IsForbidden(err) && strings.Contains(err.Error(), secretNamesPolicy+":")
}

// wantAdmitted waits until a dry-run create of o by c is admitted: the policy's
// param cache sees grants a moment after they change.
func wantAdmitted(t *testing.T, c client.Client, o func() client.Object) {
	t.Helper()
	eventually(t, "admission of "+describeObject(o()), func() (bool, string) {
		err := c.Create(context.Background(), o(), client.DryRunAll)
		return err == nil, fmt.Sprint(err)
	})
}

// wantRefused waits until a dry-run create of o by c is refused by the policy,
// naming each of the Secrets in refused.
func wantRefused(t *testing.T, c client.Client, o func() client.Object, refused ...string) {
	t.Helper()
	var last error
	eventually(t, "the policy's refusal of "+describeObject(o()), func() (bool, string) {
		last = c.Create(context.Background(), o(), client.DryRunAll)
		return refusedByPolicy(last), fmt.Sprint(last)
	})
	for _, s := range refused {
		if !strings.Contains(last.Error(), s) {
			t.Errorf("the refusal does not name %s: %v", s, last)
		}
	}
}

func TestEnvtestGrantSecretNames(t *testing.T) {
	proposed(t)
	ctx := context.Background()
	const (
		in, also, stray  = "media", "home-automation", "frontend"
		named, alsoNamed = "app-secret", "app-db-secret"
	)
	job := func(ns string, secrets ...string) func() client.Object {
		return func() client.Object { return secretJob(ns, secrets...) }
	}

	// The namespace operator in media and home-automation, naming two Secrets,
	// with a stray binding in frontend that its grant does not name.
	op, opClient := grantIdentity(t, v1alpha1.KubeGrant{
		Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{in, also}, Secrets: []string{named, alsoNamed},
	}, v1alpha1.GrantActive, in, also, stray)

	// First a refusal: it shows the policy is enforced and its param cache
	// holds this grant, so the admissions after it are not vacuous.
	t.Run("a Secret the grant does not name", func(t *testing.T) {
		for i := range 4 { // each way of referencing one
			secrets := []string{named, alsoNamed, named, alsoNamed}
			secrets[i] = "other-secret"
			wantRefused(t, opClient, job(in, secrets...), "other-secret")
		}
	})
	t.Run("its named Secrets, each way a pod references one", func(t *testing.T) {
		wantAdmitted(t, opClient, job(in, named))
		wantAdmitted(t, opClient, job(in, named, alsoNamed, named, alsoNamed))
		wantAdmitted(t, opClient, job(also, alsoNamed))
	})
	t.Run("no Secret, or one on the identity guard's short list", func(t *testing.T) {
		wantAdmitted(t, opClient, job(in))
		wantAdmitted(t, opClient, job(in, "media-volsync-aws-secret"))
	})
	t.Run("a named Secret in a namespace the grant does not name", func(t *testing.T) {
		wantRefused(t, opClient, job(stray, named), named)
	})

	t.Run("a pod-spec update: restart passes, a new unnamed Secret does not", func(t *testing.T) {
		labels := map[string]string{"app": "s5"}
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: in, Name: name("s5-app")},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					// It already mounts a Secret the grant does not name.
					Spec: secretJob(in, "app-old-secret").Spec.Template.Spec,
				},
			},
		}
		dep.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyAlways
		if err := admin.Create(ctx, dep); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = admin.Delete(context.Background(), dep) })
		patch := func(spec string) error {
			return opClient.Patch(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: in, Name: dep.Name}},
				client.RawPatch(types.StrategicMergePatchType, []byte(spec)), client.DryRunAll)
		}
		restart := `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-10-31T15:00:00Z"}}}}}`
		if err := patch(restart); err != nil {
			t.Fatalf("restart refused: %v", err)
		}
		addEnv := func(secret string) string {
			return fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"name":"op","env":[{"name":"NEW","valueFrom":{"secretKeyRef":{"name":%q,"key":"k"}}}]}]}}}}`, secret)
		}
		if err := patch(addEnv(named)); err != nil {
			t.Fatalf("adding a named Secret refused: %v", err)
		}
		if err := patch(addEnv("other-secret")); !refusedByPolicy(err) {
			t.Fatalf("adding an unnamed Secret: %v, want the policy's refusal", err)
		}
	})

	t.Run("an app-operator (C1) identity, which names no Secret", func(t *testing.T) {
		_, wl := grantIdentity(t, v1alpha1.KubeGrant{Role: v1alpha1.RoleWorkloads, Namespaces: []string{in}}, v1alpha1.GrantActive, in)
		wantRefused(t, wl, job(in, named), named)
		wantAdmitted(t, wl, job(in))
	})
	t.Run("a namespace operator that names none, beside one that does", func(t *testing.T) {
		_, none := grantIdentity(t, v1alpha1.KubeGrant{Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{in}}, v1alpha1.GrantActive, in)
		wantRefused(t, none, job(in, named), named)
		wantAdmitted(t, none, job(in))
	})
	t.Run("a grant that is not Active", func(t *testing.T) {
		_, pending := grantIdentity(t, v1alpha1.KubeGrant{
			Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{in}, Secrets: []string{named},
		}, v1alpha1.GrantPending, in)
		wantRefused(t, pending, job(in, named), named)
	})

	// Last, as it ends the main grant: once it is Expired, its named Secrets
	// are refused too (the broker deletes the identity as well).
	t.Run("the main grant, once Expired", func(t *testing.T) {
		setPhase(t, op, v1alpha1.GrantExpired)
		wantRefused(t, opClient, job(in, named), named)
	})
}

// The policy's other params pass trivially, so other grants, however many and
// whatever they name, neither allow nor refuse the caller's request: twenty
// more namespace-operator grants naming other-secret change nothing.
func TestEnvtestGrantSecretNamesIgnoresOtherGrants(t *testing.T) {
	proposed(t)
	const in = "media"
	for range 20 {
		grantIdentity(t, v1alpha1.KubeGrant{Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{in}, Secrets: []string{"other-secret"}}, v1alpha1.GrantActive)
	}
	_, c := grantIdentity(t, v1alpha1.KubeGrant{Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{in}, Secrets: []string{"app-secret"}}, v1alpha1.GrantActive, in)
	job := func(secrets ...string) func() client.Object {
		return func() client.Object { return secretJob(in, secrets...) }
	}
	wantRefused(t, c, job("other-secret"), "other-secret")
	wantAdmitted(t, c, job("app-secret"))
	if err := c.Create(context.Background(), secretJob(in, "app-secret"), client.DryRunAll); err != nil {
		t.Fatalf("the named Secret, once admitted: %v", err)
	}
	var list v1alpha1.AccessGrantList
	if err := admin.List(context.Background(), &list, client.InNamespace(sessionNS)); err != nil {
		t.Fatal(err)
	}
	t.Logf("evaluated against %d retained grants", len(list.Items))
	if len(list.Items) < 21 {
		t.Fatalf("%d grants in %s, want at least 21", len(list.Items), sessionNS)
	}
}
