package keeper

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func credentialManifest(t *testing.T, scheme *runtime.Scheme, path string) []client.Object {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	codec := serializer.NewCodecFactory(scheme).UniversalDeserializer()
	var objects []client.Object
	for {
		var doc runtime.RawExtension
		if err := decoder.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(doc.Raw) == 0 {
			continue
		}
		obj, _, err := codec.Decode(doc.Raw, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, obj.(client.Object))
	}
	return objects
}

func TestCredentialExactRolesAndActorGuard(t *testing.T) {
	ctx := context.Background()
	adminCfg := envConfig(t)
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	admin, err := client.New(adminCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{keeperNS, "dev-agents"} {
		err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"testdata/credential-rbac.yaml", "testdata/credential-job-guard.yaml"} {
		for _, obj := range credentialManifest(t, scheme, path) {
			if err := admin.Create(ctx, obj); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Deliberately give the broker status and broad metadata permission here:
	// admission must still reject violations if RBAC accidentally grows later.
	brokerSA := "dev-env-broker"
	for _, obj := range []client.Object{
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: keeperNS, Name: "credential-broker-test"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"credentialjobs", "credentialjobs/status"}, Verbs: []string{"get", "create", "update", "patch", "delete"}}}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: keeperNS, Name: "credential-broker-test"}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "credential-broker-test"}, Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Namespace: keeperNS, Name: brokerSA}}},
	} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	actor := func(sa string) client.Client {
		cfg := rest.CopyConfig(adminCfg)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + keeperNS + ":" + sa, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + keeperNS, "system:authenticated"}}
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	keeper, broker := actor(keeperSA), actor(brokerSA)
	key := types.NamespacedName{Namespace: keeperNS, Name: DefaultCredentialJournalSecret}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
	if err := admin.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	journal := &credentialJournal{Client: keeper, Secret: key}
	e := credentialEntry{JobUID: testJobUID, Spec: newCredentialFixture(t).job.Spec, Stage: journalIntent, Uncertain: true}
	onServer(t, "credential roles to propagate", func() bool { return journal.update(ctx, testJobUID, &e) == nil })
	if _, _, err := journal.load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := keeper.List(ctx, &corev1.SecretList{}, client.InNamespace(keeperNS)); !apierrors.IsForbidden(err) {
		t.Fatalf("secret list should be forbidden: %v", err)
	}
	if err := keeper.Get(ctx, types.NamespacedName{Namespace: keeperNS, Name: "unrelated-private-secret"}, &corev1.Secret{}); !apierrors.IsForbidden(err) {
		t.Fatalf("unrelated secret get should be forbidden: %v", err)
	}
	job := &v1alpha1.CredentialJob{ObjectMeta: metav1.ObjectMeta{Namespace: keeperNS, Name: v1alpha1.CredentialJobName(testGrantUID)}, Spec: e.Spec}
	onServer(t, "broker role to propagate", func() bool { return broker.Create(ctx, job) == nil })
	getJob := func(c client.Client) *v1alpha1.CredentialJob {
		j := &v1alpha1.CredentialJob{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(job), j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	onServer(t, "actor policy to reject broker status", func() bool {
		j := getJob(broker)
		j.Status.Phase = v1alpha1.CredentialPending
		err := broker.Status().Update(ctx, j)
		return apierrors.IsForbidden(err)
	})
	policy := &admissionv1.ValidatingAdmissionPolicy{}
	if err := admin.Get(ctx, types.NamespacedName{Name: "dev-env-credential-job-actors"}, policy); err != nil {
		t.Fatal(err)
	}
	if policy.Status.TypeChecking != nil && len(policy.Status.TypeChecking.ExpressionWarnings) > 0 {
		t.Fatalf("CEL warnings: %+v", policy.Status.TypeChecking.ExpressionWarnings)
	}
	j := getJob(keeper)
	j.Finalizers = []string{v1alpha1.CredentialCleanupFinalizer, "example.test/other"}
	// Keeper cannot add somebody else's finalizer.
	if err := keeper.Update(ctx, j); !apierrors.IsForbidden(err) {
		t.Fatalf("keeper unrelated finalizer: %v", err)
	}
	j = getJob(keeper)
	j.Finalizers = []string{v1alpha1.CredentialCleanupFinalizer}
	if err := keeper.Update(ctx, j); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*v1alpha1.CredentialJob){
		func(j *v1alpha1.CredentialJob) { j.Spec.Release = true },
		func(j *v1alpha1.CredentialJob) { j.Labels = map[string]string{"unrelated": "changed"} },
	} {
		j = getJob(keeper)
		change(j)
		if err := keeper.Update(ctx, j); !apierrors.IsForbidden(err) {
			t.Fatalf("keeper request mutation: %v", err)
		}
	}
	j = getJob(keeper)
	j.Status.Phase = v1alpha1.CredentialInstalled
	if err := keeper.Status().Update(ctx, j); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*v1alpha1.CredentialJob){
		func(j *v1alpha1.CredentialJob) { j.Finalizers = nil },
		func(j *v1alpha1.CredentialJob) { j.Annotations = map[string]string{"unrelated": "changed"} },
		func(j *v1alpha1.CredentialJob) { j.Labels = map[string]string{"unrelated": "changed"} },
		func(j *v1alpha1.CredentialJob) {
			j.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "unrelated", UID: testPodUID}}
		},
	} {
		j = getJob(broker)
		change(j)
		if err := broker.Update(ctx, j); !apierrors.IsForbidden(err) {
			t.Fatalf("broker metadata mutation: %v", err)
		}
	}
	j = getJob(broker)
	if err := broker.Delete(ctx, j); !apierrors.IsForbidden(err) {
		t.Fatalf("broker delete before revocation: %v", err)
	}
	j = getJob(broker)
	j.Spec.Release = true
	if err := broker.Update(ctx, j); err != nil {
		t.Fatal(err)
	}
	// The guard scopes its writer split solely to component identities; the
	// existing administrator/v1 maintenance identity remains able to intervene.
	j = getJob(admin)
	j.Annotations = map[string]string{"maintenance": "allowed"}
	if err := admin.Update(ctx, j); err != nil {
		t.Fatal(err)
	}
	j = getJob(keeper)
	j.Status.Phase = v1alpha1.CredentialRevoked
	j.Status.RevokedAt = &metav1.Time{Time: time.Now()}
	if err := keeper.Status().Update(ctx, j); err != nil {
		t.Fatal(err)
	}
	j = getJob(keeper)
	j.Finalizers = nil
	if err := keeper.Update(ctx, j); err != nil {
		t.Fatal(err)
	}
	j = getJob(broker)
	if err := broker.Delete(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := keeper.Create(ctx, &v1alpha1.CredentialJob{ObjectMeta: metav1.ObjectMeta{Namespace: keeperNS, Name: "credential-" + string(testSessionUID)}, Spec: e.Spec}); !apierrors.IsForbidden(err) {
		t.Fatalf("keeper create: %v", err)
	}
	if err := keeper.Status().Patch(ctx, &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "grant-example"}}, client.RawPatch(types.MergePatchType, []byte(`{"status":{"phase":"Active"}}`))); !apierrors.IsForbidden(err) {
		t.Fatalf("keeper AccessGrant status: %v", err)
	}
}
