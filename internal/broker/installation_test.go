package broker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd"
)

type compatibilityInstaller struct{ attempts int }

// storeInstaller exercises the real agentd files while standing in for exec.
type storeInstaller struct {
	store  agentd.Grants
	podUID string
}

func (i *storeInstaller) Install(_ context.Context, pod *corev1.Pod, g *v1alpha1.AccessGrant, token string, expires time.Time) error {
	if string(pod.UID) != i.podUID {
		return errors.New("the exec target pod changed")
	}
	return i.store.Install(agentd.GrantSpec{Name: g.Name, Role: g.Spec.Kube.Role, Namespaces: g.Spec.Kube.Namespaces, Expires: expires}, []byte(token))
}

func (i *storeInstaller) Remove(_ context.Context, pod *corev1.Pod, g *v1alpha1.AccessGrant) error {
	if string(pod.UID) != i.podUID {
		return errors.New("the exec target pod changed")
	}
	return i.store.Remove(g.Name)
}

func (i *compatibilityInstaller) Install(context.Context, *corev1.Pod, *v1alpha1.AccessGrant, string, time.Time) error {
	i.attempts++
	return ErrIncompatiblePod
}
func (*compatibilityInstaller) Remove(context.Context, *corev1.Pod, *v1alpha1.AccessGrant) error {
	return nil
}

func installationFixture(t *testing.T, inst Installer, capToken time.Duration) (*Broker, *v1alpha1.AccessGrant, *corev1.Pod, *clocktesting.FakePassiveClock, *int) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := errors.Join(v1alpha1.AddToScheme(scheme), corev1.AddToScheme(scheme), rbacv1.AddToScheme(scheme)); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	clk := clocktesting.NewFakePassiveClock(now)
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "session-one", Namespace: "dev-agents", UID: "session-uid"}, Spec: v1alpha1.AgentSessionSpec{Repo: "repo", Profile: "full"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "pod-one", Labels: map[string]string{v1alpha1.LabelSession: s.Name}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	g := &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant-one", Namespace: s.Namespace, UID: "grant-uid", Finalizers: []string{Finalizer}, CreationTimestamp: metav1.NewTime(now)}, Spec: v1alpha1.AccessGrantSpec{
		Requester: v1alpha1.GrantRequester{Session: s.Name}, Type: v1alpha1.GrantKube, TTL: metav1.Duration{Duration: time.Hour}, Kube: &v1alpha1.KubeGrant{Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}},
	}, Status: v1alpha1.AccessGrantStatus{Phase: v1alpha1.GrantActive, ExpiresAt: ptr.To(metav1.NewTime(now.Add(time.Hour)))}}
	mints := new(int)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(g).WithObjects(s, pod, g).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceCreate: func(_ context.Context, _ client.Client, subresource string, obj, output client.Object, _ ...client.SubResourceCreateOption) error {
			if subresource != "token" || obj.GetName() != g.Name {
				t.Fatal("mint used an identity outside its grant")
			}
			*mints++
			tr := output.(*authenticationv1.TokenRequest)
			life := time.Duration(*tr.Spec.ExpirationSeconds) * time.Second
			if capToken != 0 {
				life = min(life, capToken)
			}
			tr.Status.Token = "fixture-private-token"
			tr.Status.ExpirationTimestamp = metav1.NewTime(clk.Now().Add(life))
			return nil
		}}).Build()
	return &Broker{Client: c, APIReader: c, Clock: clk, SessionNamespace: "dev-agents", PolicyNamespace: "dev-env-system", Installer: inst}, g, pod, clk, mints
}

func reconcileInstallation(t *testing.T, b *Broker, g *v1alpha1.AccessGrant) ctrl.Result {
	t.Helper()
	res, err := b.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.APIReader.Get(context.Background(), client.ObjectKeyFromObject(g), g); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestInstallationRestartAndReplacement(t *testing.T) {
	inst := &recorder{}
	b, g, pod, _, mints := installationFixture(t, inst, 0)
	reconcileInstallation(t, b, g)
	if *mints != 1 || g.Status.InstalledPodUID != string(pod.UID) || installedState(g).TokenExpires == nil {
		t.Fatal("installation was not persisted")
	}
	rv := g.ResourceVersion
	// A new broker retains no in-memory grant state and makes no new token.
	b = &Broker{Client: b.Client, APIReader: b.APIReader, Clock: b.Clock, SessionNamespace: b.SessionNamespace, PolicyNamespace: b.PolicyNamespace, Installer: inst}
	reconcileInstallation(t, b, g)
	if *mints != 1 || g.ResourceVersion != rv {
		t.Fatal("broker restart reminted or rewrote an unchanged grant")
	}
	ctx := context.Background()
	if err := b.Client.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.UID = "pod-two"
	pod.ResourceVersion = ""
	if err := b.Client.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	reconcileInstallation(t, b, g)
	if *mints != 2 || g.Status.InstalledPodUID != "pod-two" {
		t.Fatal("a replacement pod did not get a fresh token")
	}
	g.Spec.Release = true
	if err := b.Client.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	reconcileInstallation(t, b, g)
	if g.Status.Phase != v1alpha1.GrantReleased {
		t.Fatal("release did not end the grant")
	}
	if err := b.Client.Get(ctx, client.ObjectKeyFromObject(g), &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
		t.Fatal("release retained the grant identity")
	}
	_, removes := inst.of(g.Name)
	if len(removes) != 1 || removes[0] != pod.UID {
		t.Fatal("release did not remove the token from the replacement pod")
	}
}

func TestReleaseCleansDeliveredTokenWithoutInstallationStatus(t *testing.T) {
	inst := &storeInstaller{store: agentd.Grants{Dir: t.TempDir(), Server: "https://127.0.0.1:443", Namespace: "dev-agents"}, podUID: "pod-one"}
	b, g, _, clk, _ := installationFixture(t, inst, 0)
	inst.store.Now = clk.Now
	underlying := b.Client
	// Delivery succeeds, but its metadata write conflicts before the broker
	// can persist InstalledPodUID. A release then wins the next reconcile.
	b.Client = interceptor.NewClient(underlying.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if obj.GetAnnotations()[InstallationAnnotation] != "" {
				return apierrors.NewConflict(v1alpha1.GroupVersion.WithResource("accessgrants").GroupResource(), obj.GetName(), errors.New("fixture installation write conflict"))
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	reconcileInstallation(t, b, g)
	if g.Status.InstalledPodUID != "" || g.Annotations[InstallationAnnotation] != "" {
		t.Fatal("the fixture persisted installation despite its conflicting write")
	}
	if err := inst.store.Use(g.Name); err != nil {
		t.Fatal("exec did not deliver a usable local grant")
	}
	g.Spec.Release = true
	if err := underlying.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	reconcileInstallation(t, b, g)
	if g.Status.Phase != v1alpha1.GrantReleased {
		t.Fatal("release did not end the grant")
	}
	if err := underlying.Get(context.Background(), client.ObjectKeyFromObject(g), &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
		t.Fatal("release retained the grant identity")
	}
	for _, path := range []string{filepath.Join(inst.store.Dir, g.Name), inst.store.KubeconfigPath()} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("release left the delivered token or selected grant context behind")
		}
	}
}

func TestInstallationCompatibilityRetriesPersist(t *testing.T) {
	inst := &compatibilityInstaller{}
	b, g, _, _, _ := installationFixture(t, inst, 0)
	for attempt := 1; attempt <= 3; attempt++ {
		// Rebuild the broker each time: the retry budget lives on the grant.
		b = &Broker{Client: b.Client, APIReader: b.APIReader, Clock: b.Clock, SessionNamespace: b.SessionNamespace, PolicyNamespace: b.PolicyNamespace, Installer: inst}
		reconcileInstallation(t, b, g)
		if installedState(g).Failures != attempt {
			t.Fatal("the compatibility retry count was not persisted")
		}
		if attempt < 3 && g.Status.Phase != v1alpha1.GrantActive {
			t.Fatal("a compatible retry ended too soon")
		}
	}
	if inst.attempts != 3 || g.Status.Phase != v1alpha1.GrantFailed {
		t.Fatal("an old pod was not failed after three attempts")
	}
	if err := b.Client.Get(context.Background(), client.ObjectKeyFromObject(g), &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
		t.Fatal("failed installation kept its identity")
	}
}

func TestInstallationReplacementResetsRetryBudget(t *testing.T) {
	b, g, pod, _, _ := installationFixture(t, &compatibilityInstaller{}, 0)
	reconcileInstallation(t, b, g)
	reconcileInstallation(t, b, g)
	if err := b.Client.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	pod.UID, pod.ResourceVersion = "pod-two", ""
	if err := b.Client.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	reconcileInstallation(t, b, g)
	if s := installedState(g); s.Failures != 1 || s.PodUID != "pod-two" || g.Status.Phase != v1alpha1.GrantActive {
		t.Fatal("a new pod inherited the old pod's retry failures")
	}
}

func TestInstallationRefreshesShortenedToken(t *testing.T) {
	b, g, _, clk, mints := installationFixture(t, &recorder{}, 10*time.Minute)
	res := reconcileInstallation(t, b, g)
	if res.RequeueAfter != 9*time.Minute || *mints != 1 {
		t.Fatal("shortened token was not scheduled for refresh")
	}
	clk.SetTime(clk.Now().Add(8 * time.Minute))
	reconcileInstallation(t, b, g)
	if *mints != 1 {
		t.Fatal("a healthy shortened token was reminted early")
	}
	clk.SetTime(clk.Now().Add(time.Minute))
	res = reconcileInstallation(t, b, g)
	if *mints != 2 || res.RequeueAfter != 9*time.Minute {
		t.Fatal("token was not refreshed before the API server's returned expiry")
	}
}

func TestInstallationRefusesForeignPod(t *testing.T) {
	b, g, pod, _, mints := installationFixture(t, &recorder{}, 0)
	pod.OwnerReferences[0].UID = "another-session"
	if err := b.Client.Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	res := reconcileInstallation(t, b, g)
	if *mints != 0 || res.RequeueAfter != installRetry {
		t.Fatal("a foreign pod received a grant")
	}
}

func TestInstallationMinimumLifetimeKeepsApprovedDeadline(t *testing.T) {
	inst := &recorder{}
	b, g, _, clk, _ := installationFixture(t, inst, 0)
	clk.SetTime(clk.Now().Add(56 * time.Minute))
	res := reconcileInstallation(t, b, g)
	s := installedState(g)
	ins, _ := inst.of(g.Name)
	if s.TokenExpires == nil || !s.TokenExpires.Time.Equal(clk.Now().Add(10*time.Minute)) ||
		len(ins) != 1 || !ins[0].expires.Equal(g.Status.ExpiresAt.Time) || res.RequeueAfter != 4*time.Minute {
		t.Fatal("the minimum token lifetime changed the approved deadline or was not recorded")
	}
}
