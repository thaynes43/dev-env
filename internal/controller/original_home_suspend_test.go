package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/homeguard"
)

func originalHomeInitFailedFixture(t *testing.T) (*v1alpha1.AgentSession, *corev1.PersistentVolumeClaim, *corev1.PersistentVolume, *corev1.Pod) {
	t.Helper()
	s, claim, pv, binding := originalFixture()
	s.Finalizers = []string{Finalizer}
	pod, err := buildPod(s, exampleTemplates(t), "")
	if err != nil {
		t.Fatal(err)
	}
	addOriginalHomeVerifier(pod, binding)
	// Model ServiceAccount admission: both containers also receive the
	// standard read-only projected token/CA/namespace mount.
	tokenMount := corev1.VolumeMount{Name: "kube-api-access-fixture", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: tokenMount.Name, VolumeSource: corev1.VolumeSource{
		Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
			{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}},
			{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}},
			{DownwardAPI: &corev1.DownwardAPIProjection{Items: []corev1.DownwardAPIVolumeFile{{Path: "namespace", FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.namespace"}}}}},
		}},
	}})
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, tokenMount)
	pod.Spec.InitContainers[0].VolumeMounts = append(pod.Spec.InitContainers[0].VolumeMounts, tokenMount)
	pod.UID = "init-failed-pod"
	pod.ResourceVersion = "1"
	pod.Spec.NodeName = "fixture-node"
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}},
		InitContainerStatuses: []corev1.ContainerStatus{{Name: "original-home-verify", RestartCount: 3,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}},
	}
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	s.Status.Conditions = []metav1.Condition{{Type: ConditionRemovalBlocked, Status: metav1.ConditionTrue,
		Reason: "SuspendNeedsRescue", Message: "previous rescue could not exec into main"}}
	return s, claim, pv, pod
}

func TestOriginalHomeInitFailedSuspend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*v1alpha1.AgentSession, *corev1.Pod)
		remove  bool
		foreign bool
	}{
		{name: "verifier failed and main never ran", remove: true},
		{name: "init running", remove: true, edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0] = corev1.ContainerStatus{Name: "original-home-verify",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
		}},
		{name: "init waiting after exit 1", remove: true, edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0].RestartCount = 1
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodInitialized, Status: corev1.ConditionFalse}}
		}},
		{name: "init terminated exit 1", remove: true, edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0] = corev1.ContainerStatus{Name: "original-home-verify",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}
		}},
		{name: "init succeeded ready and initialized", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0] = corev1.ContainerStatus{Name: "original-home-verify", Ready: true,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodInitialized, Status: corev1.ConditionTrue}}
		}},
		{name: "init succeeded not ready", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0] = corev1.ContainerStatus{Name: "original-home-verify",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}
		}},
		{name: "initialized while init waiting", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodInitialized, Status: corev1.ConditionTrue}}
		}},
		{name: "init waiting restarted without last state", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0].LastTerminationState = corev1.ContainerState{}
		}},
		{name: "init waiting after success", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0].LastTerminationState.Terminated.ExitCode = 0
		}},
		{name: "init running after earlier success", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0] = corev1.ContainerStatus{Name: "original-home-verify", RestartCount: 1,
				State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}
		}},
		{name: "init terminated exit 1 after earlier success", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0] = corev1.ContainerStatus{Name: "original-home-verify", RestartCount: 1,
				State:                corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}
		}},
		{name: "init running after earlier failure", remove: true, edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0] = corev1.ContainerStatus{Name: "original-home-verify", RestartCount: 1,
				State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}
		}},
		{name: "init terminated exit 1 after earlier failure", remove: true, edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0] = corev1.ContainerStatus{Name: "original-home-verify", RestartCount: 1,
				State:                corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2}}}
		}},
		{name: "init running with ambiguous last state", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0] = corev1.ContainerStatus{Name: "original-home-verify", RestartCount: 1,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{},
					Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}
		}},
		{name: "init reports ready while failed", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0].Ready = true
		}},
		{name: "init state unknown", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0].State = corev1.ContainerState{}
		}},
		{name: "init running and waiting together", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.InitContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
		}},
		{name: "pod phase Running", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Status.Phase = corev1.PodRunning }},
		{name: "pod phase Unknown", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Status.Phase = corev1.PodUnknown }},
		{name: "native sidecar init", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			restart := corev1.ContainerRestartPolicyAlways
			p.Spec.InitContainers[0].RestartPolicy = &restart
		}},
		{name: "subPath HOME", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].VolumeMounts[0].SubPath = "partial-home"
		}},
		{name: "hold pod", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Labels[v1alpha1.LabelHold] = "true" }},
		{name: "spoofed init command", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].Command = []string{"/tmp/agentd"}
		}},
		{name: "init lifecycle hooks", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].Lifecycle = &corev1.Lifecycle{PostStart: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{Command: []string{"touch", "/home/dev/changed"}},
			}}
		}},
		{name: "init volume devices", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].VolumeDevices = []corev1.VolumeDevice{{Name: "raw-home", DevicePath: "/dev/home"}}
		}},
		{name: "init image differs from agent", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].Image = "example.invalid/other:fixture"
		}},
		{name: "privileged init", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			privileged := true
			p.Spec.InitContainers[0].SecurityContext.Privileged = &privileged
		}},
		{name: "init adds capabilities", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_ADMIN"}
		}},
		{name: "init allows privilege escalation", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			escalate := true
			p.Spec.InitContainers[0].SecurityContext.AllowPrivilegeEscalation = &escalate
		}},
		{name: "init escalation default unknown", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].SecurityContext.AllowPrivilegeEscalation = nil
		}},
		{name: "init security context missing", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].SecurityContext = nil
		}},
		{name: "Draining and Suspended verifier failed", remove: true, edit: func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Status.Phase = v1alpha1.PhaseDraining
		}},
		{name: "Draining and Suspended main ran", edit: func(s *v1alpha1.AgentSession, p *corev1.Pod) {
			s.Status.Phase = v1alpha1.PhaseDraining
			p.Status.ContainerStatuses[0].RestartCount = 1
		}},
		{name: "all main containers waiting", remove: true, edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			other := *p.Spec.Containers[0].DeepCopy()
			other.Name = "other-main"
			p.Spec.Containers = append(p.Spec.Containers, other)
			status := p.Status.ContainerStatuses[0]
			status.Name = other.Name
			p.Status.ContainerStatuses = append([]corev1.ContainerStatus{status}, p.Status.ContainerStatuses...)
		}},
		{name: "main running", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		}},
		{name: "main running and waiting together", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
		}},
		{name: "waiting main has ContainerID", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].ContainerID = "containerd://uncertain-state"
		}},
		{name: "main restarted", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Status.ContainerStatuses[0].RestartCount = 1 }},
		{name: "main reports started", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			started := true
			p.Status.ContainerStatuses[0].Started = &started
		}},
		{name: "main reports ready", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Status.ContainerStatuses[0].Ready = true }},
		{name: "main terminated", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}
		}},
		{name: "main last termination", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ExitCode: 1}
		}},
		{name: "main last running", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].LastTerminationState.Running = &corev1.ContainerStateRunning{}
		}},
		{name: "main status missing", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Status.ContainerStatuses = nil }},
		{name: "main status unknown", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State = corev1.ContainerState{}
		}},
		{name: "main status wrong name", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Status.ContainerStatuses[0].Name = "unknown" }},
		{name: "duplicate main status hides missing main", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "other-main"})
			p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, p.Status.ContainerStatuses[0])
		}},
		{name: "another init ran", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{Name: "other-init"})
			p.Status.InitContainerStatuses = append(p.Status.InitContainerStatuses, corev1.ContainerStatus{Name: "other-init",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}})
		}},
		{name: "writable init HOME", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].VolumeMounts[0].ReadOnly = false
		}},
		{name: "writable HOME alias", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].VolumeMounts = append(p.Spec.InitContainers[0].VolumeMounts, corev1.VolumeMount{Name: "home", MountPath: "/writable"})
		}},
		{name: "writable non-HOME mount", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.InitContainers[0].VolumeMounts = append(p.Spec.InitContainers[0].VolumeMounts, corev1.VolumeMount{Name: "tmp", MountPath: "/scratch"})
		}},
		{name: "ephemeral container ran", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}}}
			p.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{{Name: "debug", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}}
		}},
		{name: "init name alone is not proof", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.Spec.InitContainers[0].Args = []string{"run"} }},
		{name: "unguarded pod unchanged", edit: func(s *v1alpha1.AgentSession, _ *corev1.Pod) { s.Annotations = nil }},
		{name: "binding names another Session UID", edit: func(s *v1alpha1.AgentSession, _ *corev1.Pod) { s.UID = "replacement-session" }, foreign: true},
		{name: "foreign pod", edit: func(_ *v1alpha1.AgentSession, p *corev1.Pod) { p.OwnerReferences[0].UID = "another-session" }, foreign: true},
		{name: "reap unchanged", edit: func(s *v1alpha1.AgentSession, _ *corev1.Pod) { now := metav1.Now(); s.DeletionTimestamp = &now }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, claim, pv, pod := originalHomeInitFailedFixture(t)
			if tc.edit != nil {
				tc.edit(s, pod)
			}
			if got := needsRescue(s, pod, time.Now(), HoldRescueRetry); got == tc.remove {
				t.Fatalf("needsRescue=%v, remove=%v", got, tc.remove)
			}
			if err := podRemovalAllowed(s, pod); (err == nil) != tc.remove {
				t.Fatalf("pod guard=%v, remove=%v", err, tc.remove)
			}
			scheme := runtime.NewScheme()
			_ = v1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(s, pod).WithObjects(s, claim, pv, pod).Build()
			deletes := 0
			cl := interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
				if _, ok := o.(*corev1.Pod); !ok {
					t.Fatalf("unexpected deletion of %T", o)
				}
				options := (&client.DeleteOptions{}).ApplyOptions(opts)
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != pod.UID ||
					options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != pod.ResourceVersion || options.GracePeriodSeconds != nil {
					t.Fatalf("normal guarded delete preconditions changed: %+v", options)
				}
				deletes++
				return c.Delete(ctx, o, opts...)
			}})
			rescuer := &fakeRescuer{calls: map[string]int{}}
			r := &Reconciler{Client: cl, APIReader: base, Rescuer: rescuer}
			res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)})
			if err != nil {
				t.Fatal(err)
			}
			var after v1alpha1.AgentSession
			if err := base.Get(ctx, client.ObjectKeyFromObject(s), &after); err != nil {
				t.Fatal(err)
			}
			var retained corev1.Pod
			podErr := base.Get(ctx, client.ObjectKeyFromObject(pod), &retained)
			switch {
			case tc.remove:
				if !apierrors.IsNotFound(podErr) || deletes != 1 || rescuer.callsFor(s.Name) != 0 || condition(&after.Status, ConditionRemovalBlocked) != nil || after.Status.Rescue != nil {
					t.Fatalf("suspend: pod=%v deletes=%d execs=%d status=%+v", podErr, deletes, rescuer.callsFor(s.Name), after.Status)
				}
			case tc.foreign:
				if podErr != nil || deletes != 0 || rescuer.callsFor(s.Name) != 0 {
					t.Fatalf("foreign pod touched: pod=%v deletes=%d execs=%d", podErr, deletes, rescuer.callsFor(s.Name))
				}
			default:
				c := condition(&after.Status, ConditionRemovalBlocked)
				if podErr != nil || deletes != 0 || rescuer.callsFor(s.Name) != 1 || res.RequeueAfter != rescueRetry ||
					c == nil || c.Status != metav1.ConditionTrue || !strings.Contains(c.Message, "the rescue could not run") {
					t.Fatalf("rescue bypassed: pod=%v deletes=%d execs=%d result=%+v condition=%+v", podErr, deletes, rescuer.callsFor(s.Name), res, c)
				}
			}
			var afterClaim corev1.PersistentVolumeClaim
			var afterPV corev1.PersistentVolume
			if err := base.Get(ctx, client.ObjectKeyFromObject(claim), &afterClaim); err != nil {
				t.Fatal(err)
			}
			if err := base.Get(ctx, client.ObjectKeyFromObject(pv), &afterPV); err != nil {
				t.Fatal(err)
			}
			// Fake storage supplies resourceVersions on initial insertion.
			afterClaim.ResourceVersion, afterPV.ResourceVersion = claim.ResourceVersion, pv.ResourceVersion
			if !reflect.DeepEqual(*claim, afterClaim) || !reflect.DeepEqual(*pv, afterPV) || !reflect.DeepEqual(s.Finalizers, after.Finalizers) {
				t.Fatal("storage or finalizers changed")
			}
		})
	}
}

func TestOriginalHomeInitFailedArchiveStillRetainsHOME(t *testing.T) {
	for _, result := range []v1alpha1.RescueResult{"", v1alpha1.RescueFailed, v1alpha1.RescueVerified} {
		t.Run(string(result), func(t *testing.T) {
			s, claim, pv, pod := originalHomeInitFailedFixture(t)
			if result != "" {
				s.Status.Rescue = &v1alpha1.RescueStatus{Result: result, PodUID: string(pod.UID), Generation: s.Generation}
			}
			scheme := runtime.NewScheme()
			_ = v1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s, claim, pv).Build() // suspended Pod already gone
			r := &Reconciler{Client: cl, APIReader: cl}
			obs := &observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{obj: claim}}
			if _, _, err := r.archive(context.Background(), s, obs, true); err != nil || !errors.Is(obs.removalBlocked, homeguard.ErrUnsafe) {
				t.Fatalf("guarded HOME archive allowed: err=%v blocked=%v", err, obs.removalBlocked)
			}
			if err := cl.Get(context.Background(), client.ObjectKeyFromObject(claim), &corev1.PersistentVolumeClaim{}); err != nil {
				t.Fatal("guarded HOME lost", err)
			}
			// Independently exercise the legacy volume guard: a never-started
			// Pod supplies no rescue proof, so an unguarded HOME still needs
			// the hold Pod's verified rescue before it can be archived.
			legacy := s.DeepCopy()
			legacy.Annotations = nil
			legacy.Status.ArchivedAt = &metav1.Time{Time: time.Now()}
			if err := volumeRemovalAllowed(legacy, false, true); (err == nil) != (result == v1alpha1.RescueVerified) {
				t.Fatalf("legacy archive rescue requirement changed: result=%q err=%v", result, err)
			}
		})
	}
}
