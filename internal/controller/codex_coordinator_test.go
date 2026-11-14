package controller

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexworkbench"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestCodexCoordinatorPodMetadata(t *testing.T) {
	s, cm := workbenchHostSession(), workbenchHostTemplate()
	tm, err := templates.Parse(cm.Data)
	if err != nil {
		t.Fatal(err)
	}
	tm.Env = append(tm.Env, corev1.EnvVar{Name: protocol.CodexCoordinatorHostEnv, Value: "forged"}, corev1.EnvVar{Name: protocol.CodexCoordinatorUIDEnv, Value: "forged"})
	r := &Reconciler{CodexWorkbenchHosts: codexworkbench.Policy{Name: s.Name, Namespace: s.Namespace}}
	for _, shape := range []string{"admitted", "ordinary", "hold"} {
		t.Run(shape, func(t *testing.T) {
			host := s.DeepCopy()
			var pod *corev1.Pod
			var err error
			if shape == "ordinary" {
				host.Name = "ordinary"
				host.Spec.Profile = "dev"
			}
			if shape == "hold" {
				pod, err = buildHoldPod(host, tm)
			} else {
				pod, err = r.sessionPod(host, tm, r.resolveCodexHost(host, tm))
			}
			if err != nil {
				t.Fatal(err)
			}
			metadata := map[string]string{}
			for _, e := range pod.Spec.Containers[0].Env {
				if e.Name == protocol.CodexCoordinatorHostEnv || e.Name == protocol.CodexCoordinatorUIDEnv {
					if _, ok := metadata[e.Name]; ok {
						t.Fatal("duplicate admission metadata")
					}
					metadata[e.Name] = e.Value
				}
			}
			if shape == "admitted" {
				if metadata[protocol.CodexCoordinatorHostEnv] != s.Name || metadata[protocol.CodexCoordinatorUIDEnv] != string(s.UID) || pod.Spec.ServiceAccountName != templates.WorkbenchServiceAccount {
					t.Fatalf("metadata=%v account=%s", metadata, pod.Spec.ServiceAccountName)
				}
			} else if len(metadata) != 0 {
				t.Fatalf("metadata on %s: %v", shape, metadata)
			}
		})
	}
	if _, err := r.sessionPod(s, tm, nil); err == nil {
		t.Fatal("implicit admission fallback")
	}
	s.UID = ""
	if _, err := r.sessionPod(s, tm, r.resolveCodexHost(s, tm)); err == nil {
		t.Fatal("missing Session UID admitted")
	}
}

// L3: a removed profile is unavailable even though templates loaded. Compare
// the complete status/result with the ordinary Claude path, ignoring only the
// wall-clock condition transition timestamp, and preserve the exact pod/HOME.
func TestOrdinaryRunningRemovedProfileMatchesMainPath(t *testing.T) {
	var baseline *v1alpha1.AgentSessionStatus
	var baselineResult ctrl.Result
	for _, agent := range []v1alpha1.AgentKind{v1alpha1.AgentClaude, v1alpha1.AgentCodex} {
		s, cm := workbenchHostSession(), workbenchHostTemplate()
		s.Name = "ordinary"
		s.Spec.Agent = agent
		s.Spec.Profile = "removed"
		s.Status.Phase = v1alpha1.PhaseRunning
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "ordinary-pod", OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Labels: map[string]string{v1alpha1.LabelRevision: "previous"}}, Spec: corev1.PodSpec{ServiceAccountName: ServiceAccountName, NodeName: "test-node"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
		claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "ordinary-home", OwnerReferences: pod.OwnerReferences}}
		r := hostReconciler(t, s, cm, interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			t.Fatal("created resource on removed profile")
			return nil
		}, Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			t.Fatal("deleted resource on removed profile")
			return nil
		}}, pod, claim)
		r.CodexWorkbenchHosts.Name = ""
		result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)})
		if err != nil {
			t.Fatal(err)
		}
		var got v1alpha1.AgentSession
		if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(s), &got); err != nil {
			t.Fatal(err)
		}
		for i := range got.Status.Conditions {
			got.Status.Conditions[i].LastTransitionTime = metav1.Time{}
		}
		if baseline == nil {
			baseline = got.Status.DeepCopy()
			baselineResult = result
		} else if !reflect.DeepEqual(*baseline, got.Status) || result != baselineResult {
			t.Fatalf("Codex deviates from ordinary main path: got=%+v result=%+v want=%+v result=%+v", got.Status, result, *baseline, baselineResult)
		}
		var kept corev1.Pod
		if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(pod), &kept); err != nil || !reflect.DeepEqual(pod, &kept) {
			t.Fatalf("pod changed: %v", err)
		}
		var home corev1.PersistentVolumeClaim
		if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(claim), &home); err != nil || home.UID != claim.UID {
			t.Fatalf("HOME changed: %v", err)
		}
	}
}
