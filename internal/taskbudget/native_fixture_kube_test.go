package taskbudget

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const fixtureTestImage = "ghcr.io/thaynes43/dev-env:2.15.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestNativeFixtureCodexHomeFollowsPrivateHome(t *testing.T) {
	pod := NativeFixturePod(fixtureTestImage)
	env := make(map[string]string)
	for _, v := range pod.Spec.Containers[0].Env {
		env[v.Name] = v.Value
	}
	if !strings.HasPrefix(env["HOME"], "/fixture/") || env["CODEX_HOME"] != env["HOME"]+"/.codex" {
		t.Fatal("fixture must explicitly keep Codex state under its private HOME")
	}
}

func fixtureAdmissionObjects(t *testing.T) (*Ledger, *corev1.Pod, *unstructured.Unstructured, *runtime.Scheme, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	pod := NativeFixturePod(fixtureTestImage)
	pod.UID, pod.ResourceVersion = "pod-original", "1"
	pod.CreationTimestamp = metav1.NewTime(now.Add(-5 * time.Second))
	pod.Spec.NodeName = "worker-fixture"
	pod.Spec.DeprecatedServiceAccount = NativeFixtureServiceAccount
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "fixture", Ready: true, ImageID: fixtureTestImage, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: pod.CreationTimestamp}}}}
	policy := NativeFixturePolicy()
	policy.SetUID("policy-original")
	policy.SetResourceVersion("1")
	l := &Ledger{Binding: Binding{TaskUID: "native-lifecycle-once", Epoch: 1, HostID: NativeFixtureHostID, PodUID: string(pod.UID), Deadline: now.Add(20 * time.Second)},
		Spec: Spec{SuccessCondition: NativeFixtureSuccess, OverallSeconds: 20, EffortSeconds: 20, CheckpointSeconds: 20}, CreatedAt: now, Parent: NativeFixtureNamespace + "/" + NativeFixtureServiceAccount}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return l, pod, policy, scheme, now
}

func TestNativeFixturePrelaunchAndActiveAdmission(t *testing.T) {
	l, pod, policy, scheme, now := fixtureAdmissionObjects(t)
	i := &KubeNativeFixtureInspector{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, policy).Build(), Image: fixtureTestImage, Now: func() time.Time { return now }}
	if err := i.InspectNativeAdmission(context.Background(), l, l.Binding); err != nil {
		t.Fatal(err)
	}
	l.Workers = []Worker{{ID: "host:" + NativeFixtureHostID + ":1", Active: true, StartedAt: now}}
	if err := i.InspectNativeAdmission(context.Background(), l, l.Binding); err != nil {
		t.Fatal(err)
	}
	i.Now = func() time.Time { return l.Binding.Deadline }
	if !errors.Is(i.InspectNativeAdmission(context.Background(), l, l.Binding), ErrDenied) {
		t.Fatal("deadline boundary admitted")
	}
}

func TestNativeFixtureRejectsCapabilityAndAuthorityDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Ledger, *corev1.Pod, *unstructured.Unstructured)
	}{
		{"inherited-codex-home", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			for n := range p.Spec.Containers[0].Env {
				if p.Spec.Containers[0].Env[n].Name == "CODEX_HOME" {
					p.Spec.Containers[0].Env[n].Value = "/home/dev/.codex"
				}
			}
		}},
		{"foreign-pod", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) { p.UID = types.UID("replaced") }},
		{"runtime-digest", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			p.Status.ContainerStatuses[0].ImageID = "ghcr.io/thaynes43/dev-env@sha256:other"
		}},
		{"container-restart", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			p.Status.ContainerStatuses[0].RestartCount = 1
		}},
		{"late-container-campaign", func(l *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			p.CreationTimestamp = metav1.NewTime(l.CreatedAt.Add(-85 * time.Second))
			p.Status.ContainerStatuses[0].State.Running.StartedAt = p.CreationTimestamp
		}},
		{"late-pod-campaign", func(l *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			p.CreationTimestamp = metav1.NewTime(l.CreatedAt.Add(-100 * time.Second))
			p.Status.ContainerStatuses[0].State.Running.StartedAt = metav1.NewTime(l.CreatedAt.Add(-10 * time.Second))
		}},
		{"later-deadline", func(l *Ledger, _ *corev1.Pod, _ *unstructured.Unstructured) {
			l.Binding.Deadline = l.Binding.Deadline.Add(time.Second)
		}},
		{"second-epoch", func(l *Ledger, _ *corev1.Pod, _ *unstructured.Unstructured) { l.Binding.Epoch = 2 }},
		{"other-campaign", func(l *Ledger, _ *corev1.Pod, _ *unstructured.Unstructured) { l.Parent = "dev-agents/other" }},
		{"managed-child", func(l *Ledger, _ *corev1.Pod, _ *unstructured.Unstructured) {
			l.Workers = []Worker{{ID: "managed-child", Active: true}}
		}},
		{"host-looking-managed-child", func(l *Ledger, _ *corev1.Pod, _ *unstructured.Unstructured) {
			l.Workers = []Worker{{ID: "host:" + NativeFixtureHostID + ":1", Active: true, Managed: &ManagedWorkerRef{Namespace: NativeFixtureNamespace, Name: "child", SessionUID: "child-original"}}}
		}},
		{"old-home", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			p.Spec.Volumes[0].EmptyDir = nil
			p.Spec.Volumes[0].PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "old-home"}
		}},
		{"unbounded-init", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			p.Spec.InitContainers = []corev1.Container{{Name: "injected", Image: fixtureTestImage}}
		}},
		{"sidecar", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "other"})
		}},
		{"ambient-credential", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "OPENAI_API_KEY", Value: "synthetic"})
		}},
		{"host-network", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) { p.Spec.HostNetwork = true }},
		{"unbounded-cpu", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			delete(p.Spec.Containers[0].Resources.Limits, corev1.ResourceCPU)
		}},
		{"different-command", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			p.Spec.Containers[0].Args = []string{"codex exec synthetic"}
		}},
		{"token-auto-mount", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) {
			p.Spec.AutomountServiceAccountToken = nil
		}},
		{"world-deny-removed", func(_ *Ledger, _ *corev1.Pod, p *unstructured.Unstructured) {
			delete(p.Object["spec"].(map[string]any), "egressDeny")
		}},
		{"second-policy-spec", func(_ *Ledger, _ *corev1.Pod, p *unstructured.Unstructured) {
			p.Object["specs"] = []any{map[string]any{}}
		}},
		{"injected-annotations", func(_ *Ledger, p *corev1.Pod, _ *unstructured.Unstructured) { p.Annotations["k8tz.io/inject"] = "true" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, pod, policy, scheme, now := fixtureAdmissionObjects(t)
			tc.mutate(l, pod, policy)
			i := &KubeNativeFixtureInspector{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, policy).Build(), Image: fixtureTestImage, Now: func() time.Time { return now }}
			if err := i.InspectNativeAdmission(context.Background(), l, l.Binding); !errors.Is(err, ErrDenied) {
				t.Fatalf("drift accepted or misclassified: %v", err)
			}
		})
	}
}

func TestNativeFixtureRefusesMissingAuthority(t *testing.T) {
	l, pod, _, scheme, now := fixtureAdmissionObjects(t)
	for _, i := range []*KubeNativeFixtureInspector{nil, {}, {Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(), Image: fixtureTestImage, Now: func() time.Time { return now }}, {Reader: fake.NewClientBuilder().WithScheme(scheme).Build(), Image: strings.TrimSuffix(fixtureTestImage, "a")}} {
		if !errors.Is(i.InspectNativeAdmission(context.Background(), l, l.Binding), ErrUnavailable) {
			t.Fatal("missing authority accepted")
		}
	}
}

// A read failure is not transformed into positive admission by the host.
type failingFixtureReader struct{ client.Reader }

func (f failingFixtureReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("read unavailable")
}

func TestNativeFixtureLiveReadFailure(t *testing.T) {
	l, _, _, _, now := fixtureAdmissionObjects(t)
	i := &KubeNativeFixtureInspector{Reader: failingFixtureReader{}, Image: fixtureTestImage, Now: func() time.Time { return now }}
	if !errors.Is(i.InspectNativeAdmission(context.Background(), l, l.Binding), ErrUnavailable) {
		t.Fatal("unknown read admitted")
	}
}

func TestNativeFixtureRealLedgerAdmissionAndLatch(t *testing.T) {
	_, pod, policy, scheme, now := fixtureAdmissionObjects(t)
	store := &memoryStore{}
	svc := &Service{Store: store, Validator: receiptValidator{}, Now: func() time.Time { return now }, NativeInspector: &KubeNativeFixtureInspector{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, policy).Build(), Image: fixtureTestImage, Now: func() time.Time { return now }}}
	l, err := svc.Create(context.Background(), "native-lifecycle-once", NativeFixtureHostID, string(pod.UID), NativeFixtureNamespace+"/"+NativeFixtureServiceAccount, Spec{SuccessCondition: NativeFixtureSuccess, OverallSeconds: 20, EffortSeconds: 20, CheckpointSeconds: 20})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := svc.Admit(context.Background(), l.Binding)
	if err != nil || len(admitted.Workers) != 1 || !admitted.Workers[0].Active {
		t.Fatal("trusted finite profile not charged", err)
	}
	now = l.Binding.Deadline
	latched, err := svc.ObserveNative(context.Background(), l.Binding)
	if err != nil || !latched.Latched || latched.Reason != "OverallBudget" {
		t.Fatal("deadline did not retain latch", err)
	}
	if _, err = svc.Admit(context.Background(), l.Binding); !errors.Is(err, ErrDenied) {
		t.Fatal("latched deadline admitted", err)
	}
}
