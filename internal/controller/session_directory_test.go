package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
)

func TestDirectoryControllerConfirmsRecordBeforeOnlyExecutor(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := testutil.New(t, provider, "dev/dev-env")
			f.Session.CreationTimestamp = metav1.NewTime(time.Now())
			f.Session.Spec.SessionDirectory = &v1alpha1.SessionDirectoryEnrollment{Version: 1, Project: "sample"}
			f.Record.CaptureOutput = true
			f.Session.Spec.ProjectCanary.CaptureOutput = true
			commonCanaryController(t, &f, "DIRECTORY_COMMON_FIXTURE")
			commonCM, headerCM := commonfixture.Sources("DIRECTORY_COMMON_FIXTURE", "HEADER")
			local := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{v1alpha1.AddToScheme, coord.AddToScheme, corev1.AddToScheme} {
				if err := add(local); err != nil {
					t.Fatal(err)
				}
			}
			base := fake.NewClientBuilder().WithScheme(local).WithStatusSubresource(&v1alpha1.AgentSession{}, &coord.SessionDirectory{}, &corev1.Pod{}).WithObjects(f.Session, f.Catalog, f.Template, commonCM, headerCM).Build()
			podCreates := 0
			c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				switch object := o.(type) {
				case *coord.SessionDirectory:
					object.UID = "directory-original"
					object.CreationTimestamp = metav1.NewTime(time.Now())
				case *corev1.PersistentVolumeClaim:
					object.UID = "home-original"
				case *corev1.Pod:
					podCreates++
					object.UID = "pod-original"
					var source v1alpha1.AgentSession
					if err := base.Get(ctx, client.ObjectKeyFromObject(f.Session), &source); err != nil {
						t.Fatal(err)
					}
					if source.Status.SessionDirectory == nil || source.Status.SessionDirectory.State != "Confirmed" || source.Status.SessionDirectory.UID != "directory-original" {
						t.Fatal("executor before independent directory confirmation")
					}
					var d coord.SessionDirectory
					if err := base.Get(ctx, client.ObjectKey{Namespace: "dev-env-system", Name: source.Status.SessionDirectory.Name}, &d); err != nil || len(d.OwnerReferences) != 0 || d.Spec.Request.Model != coord.DirectoryText(f.Record.Model) {
						t.Fatal("missing original durable metadata", err)
					}
				}
				return c.Create(ctx, o, opts...)
			}})
			r := canaryReconciler(c, base, f)
			r.RetainedCommonRules = true
			r.SessionDirectory = &sessiondirectory.KubeStore{Client: c, Live: base, Namespace: "dev-env-system"}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)}
			if _, err := r.Reconcile(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if podCreates != 1 {
				t.Fatal("first executor refused", podCreates)
			}
			var p corev1.Pod
			if base.Get(context.Background(), request.NamespacedName, &p) != nil {
				t.Fatal("original Pod missing")
			}
			parsed, err := protocol.ParseSessionFile([]byte(p.Annotations[protocol.SessionInputAnnotation]))
			if err != nil || !parsed.CaptureOutput || len(parsed.CommonRules) == 0 || parsed.SessionUID != string(f.Session.UID) {
				t.Fatal("directory lost original common/capture fixed-file transport", err)
			}
			for _, v := range p.Spec.Volumes {
				if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName != HomeClaimName(f.Session.Name) {
					t.Fatal("directory changed own HOME privacy")
				}
			}
			if err := base.Delete(context.Background(), &p); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if podCreates != 1 {
				t.Fatal("directory rearmed provider attempt")
			}
		})
	}
}
