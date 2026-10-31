package broker

// The envtest case for the proposed admission policy dev-env-grant-secret-names
// (DESIGN-042 5.1, P5) and the role it guards, dev-env-grant-namespace-operator
// (P4). Neither is in haynes-ops yet: both are proposals, kept in
// testdata/proposed-haynes-ops/ beside the verbatim copies of testdata/haynes-ops/.
// The identity guard is not installed here, so this proves the named-Secret
// rule on its own.
//
// No broker runs. Each grant identity is made by the broker's own ensure, as
// admin (the haynes-ops broker RBAC for secret-use Roles is P4), and ended by
// its own revoke. Every pod-bearing request is a server-side dry run: admission
// runs and nothing is stored.
//
// The proposed policy and ClusterRole stay installed for the rest of the
// package's tests. They act only on pod-bearing requests from grant
// identities, which no other test in the package makes.

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
	clocktesting "k8s.io/utils/clock/testing"
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

// adminBroker makes and revokes grant identities as the broker would, with
// admin's rights.
func adminBroker() *Broker {
	return &Broker{Client: admin, APIReader: admin, SessionNamespace: sessionNS, PolicyNamespace: systemNS, Clock: clocktesting.NewFakeClock(time.Now())}
}

// grantIdentity makes a grant's identity with the broker's ensure: its
// ServiceAccount, the catalog RoleBinding in each granted namespace and, for
// the namespace operator, the secret-use Role and RoleBinding. It revokes it
// when the test ends, and returns the grant and a client acting as it.
func grantIdentity(t *testing.T, kube v1alpha1.KubeGrant) (*v1alpha1.AccessGrant, client.Client) {
	t.Helper()
	g := &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name("grant-s5")},
		Spec: v1alpha1.AccessGrantSpec{
			Requester: v1alpha1.GrantRequester{Session: "s5-session", Repo: "haynes-ops", Profile: "full", Agent: v1alpha1.AgentClaude},
			Type:      v1alpha1.GrantKube, Kube: &kube,
			TTL:    metav1.Duration{Duration: 30 * time.Minute},
			Reason: "DESIGN-042 S5 envtest",
		},
	}
	if err := check(g); err != nil {
		t.Fatalf("the test grant fails the broker's checks: %v", err)
	}
	b := adminBroker()
	if _, err := b.ensure(context.Background(), g); err != nil {
		t.Fatalf("ensure %s: %v", g.Name, err)
	}
	t.Cleanup(func() {
		if err := b.revoke(context.Background(), g, "test end"); err != nil {
			t.Errorf("revoke %s: %v", g.Name, err)
		}
	})
	cfg := rest.CopyConfig(adminCfg)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:" + sessionNS + ":" + g.Name,
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:" + sessionNS, "system:authenticated"},
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return g, c
}

// strayBinding binds the grant's catalog role in a namespace the grant does
// not name, as a broken broker might: RBAC then allows the request, and only
// the policy stands in the way.
func strayBinding(t *testing.T, g *v1alpha1.AccessGrant, ns string) {
	t.Helper()
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: g.Name},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: g.Spec.Kube.Role},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Namespace: sessionNS, Name: g.Name}},
	}
	if err := admin.Create(context.Background(), rb); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(context.Background(), rb) })
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

// wantAdmitted waits until a dry-run create of o by c is admitted: RBAC and
// the policy see new objects a moment after they are made.
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

func job(ns string, secrets ...string) func() client.Object {
	return func() client.Object { return secretJob(ns, secrets...) }
}

func TestEnvtestGrantSecretNames(t *testing.T) {
	proposed(t)
	ctx := context.Background()
	const (
		in, also, stray  = "media", "home-automation", "frontend"
		named, alsoNamed = "app-secret", "app-db-secret"
	)
	op := func(secrets ...string) v1alpha1.KubeGrant {
		return v1alpha1.KubeGrant{Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{in}, Secrets: secrets}
	}

	// (iv) No grant anywhere may use a Secret: a namespace-operator identity
	// whose secret-use objects are gone is refused, with nothing else around.
	// It runs first, so it is also the barrier that the policy is enforced.
	t.Run("(iv) no grant authorizes any Secret", func(t *testing.T) {
		var roles rbacv1.RoleList
		if err := admin.List(ctx, &roles, client.MatchingLabels{v1alpha1.LabelManagedBy: ManagedBy}); err != nil {
			t.Fatal(err)
		}
		for _, r := range roles.Items {
			if strings.HasSuffix(r.Name, secretUseSuffix) {
				t.Fatalf("a secret-use Role is left over: %s/%s", r.Namespace, r.Name)
			}
		}
		g, c := grantIdentity(t, op(named))
		for _, o := range secretUseObjects(g, in) {
			if err := admin.Delete(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
		wantRefused(t, c, job(in, named), named)
		wantAdmitted(t, c, job(in))
	})

	// The main grant: the namespace operator in two namespaces with two
	// Secrets, and a stray catalog binding in a third.
	g, opClient := grantIdentity(t, v1alpha1.KubeGrant{
		Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{in, also}, Secrets: []string{named, alsoNamed},
	})
	strayBinding(t, g, stray)

	t.Run("(ii) own grant, a Secret it does not name", func(t *testing.T) {
		for i := range 4 { // each way of referencing one
			secrets := []string{named, alsoNamed, named, alsoNamed}
			secrets[i] = "other-secret"
			wantRefused(t, opClient, job(in, secrets...), "other-secret")
		}
	})
	t.Run("(i) own grant, its named Secrets, each way a pod references one", func(t *testing.T) {
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
	t.Run("more than 16 references in one request", func(t *testing.T) {
		many := make([]string, 17)
		for i := range many {
			many[i] = named
		}
		wantRefused(t, opClient, job(in, many...), "at most 16")
	})

	t.Run("a pod-spec update: restart passes, an unnamed or newly visible Secret does not", func(t *testing.T) {
		labels := map[string]string{"app": "s5"}
		spec := secretJob(in, "app-old-secret").Spec.Template.Spec // a Secret the grant does not name
		spec.RestartPolicy = corev1.RestartPolicyAlways
		spec.Volumes = append(spec.Volumes, corev1.Volume{Name: "csi", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{
			Driver: "synthetic.csi", NodePublishSecretRef: &corev1.LocalObjectReference{Name: "csi-secret"},
		}}})
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: in, Name: name("s5-app")},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: spec},
			},
		}
		if err := admin.Create(ctx, dep); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = admin.Delete(context.Background(), dep) })
		patch := func(body string) error {
			return opClient.Patch(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: in, Name: dep.Name}},
				client.RawPatch(types.StrategicMergePatchType, []byte(body)), client.DryRunAll)
		}
		addEnv := func(secret string) string {
			return fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"name":"op","env":[{"name":"NEW","valueFrom":{"secretKeyRef":{"name":%q,"key":"k"}}}]}]}}}}`, secret)
		}
		for _, c := range []struct {
			what, body string
			refused    string // "" is admitted
		}{
			{"a restart", `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-10-31T15:00:00Z"}}}}}`, ""},
			{"an image change", `{"spec":{"template":{"spec":{"containers":[{"name":"op","image":"synthetic:2"}]}}}}`, ""},
			{"a named Secret added", addEnv(named), ""},
			{"an unnamed Secret added", addEnv("other-secret"), "other-secret"},
			{"a kubelet-only Secret made visible to the container", addEnv("csi-secret"), "csi-secret"},
		} {
			err := patch(c.body)
			switch {
			case c.refused == "" && err != nil:
				t.Errorf("%s refused: %v", c.what, err)
			case c.refused != "" && (!refusedByPolicy(err) || !strings.Contains(err.Error(), c.refused)):
				t.Errorf("%s: %v, want the policy's refusal naming %s", c.what, err, c.refused)
			}
		}
	})

	t.Run("(iii) no own authorization, unrelated grants that name the Secret", func(t *testing.T) {
		for range 3 {
			grantIdentity(t, op(named))
		}
		_, other := grantIdentity(t, op("other-secret"))
		wantRefused(t, other, job(in, named), named)
		wantAdmitted(t, other, job(in, "other-secret"))
		_, wl := grantIdentity(t, v1alpha1.KubeGrant{Role: v1alpha1.RoleWorkloads, Namespaces: []string{in}})
		wantRefused(t, wl, job(in, named), named)
		wantAdmitted(t, wl, job(in))
	})

	t.Run("sixteen named Secrets in one Job fit the cost budget", func(t *testing.T) {
		sixteen := make([]string, 16)
		for i := range sixteen {
			sixteen[i] = fmt.Sprintf("app-%d-secret", i)
		}
		_, c := grantIdentity(t, op(sixteen...))
		wantAdmitted(t, c, job(in, sixteen...))
	})

	// Last, as they end the main grant.
	t.Run("(v) an ended grant authorizes nothing", func(t *testing.T) {
		// Revoke deletes the secret-use binding first. A Role left without it
		// allows nothing, though the catalog binding still stands.
		rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: in, Name: secretUseName(g)}}
		if err := admin.Delete(ctx, rb); err != nil {
			t.Fatal(err)
		}
		wantRefused(t, opClient, job(in, named), named)
		wantAdmitted(t, opClient, job(also, alsoNamed)) // the other namespace's binding still stands
		// The full revoke: nothing of the grant is left.
		if err := adminBroker().revoke(ctx, g, "ended"); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the revoked grant's refusal", func() (bool, string) {
			err := opClient.Create(ctx, secretJob(also, alsoNamed), client.DryRunAll)
			return apierrors.IsForbidden(err), fmt.Sprint(err)
		})
	})
}
