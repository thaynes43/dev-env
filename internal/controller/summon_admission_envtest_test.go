package controller

// OP777 with a real API server and the manager (envtest): the gate before
// ensure holds a summoned session's volume and pod until it is admitted, with
// --enable-summoned-lanes on and off, and an admitted one is built with only
// its session as owner.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// summonLatchOn turns on both task latch flags, as --summon-create-kinds=task
// requires (and the two flags --enable-terminal-task-pod-reclaim needs).
func summonLatchOn(r *Reconciler) {
	r.PrivateHistoryRetention, r.PrivateTerminalRecord, r.PrivateExecutorStop, r.TerminalTaskPodReclaim = true, true, true, true
}

// admitSession stores admittedAt as the leader's admission does: once the
// reconciler has recorded a task's latch.
func admitSession(t *testing.T, name string) {
	t.Helper()
	eventually(t, "admit "+name, func() error {
		live := session(t, name)
		if live.Spec.Mode == v1alpha1.ModeTask && live.Status.PrivateHistory == nil {
			return errors.New("the reconciler has not recorded the task latch yet")
		}
		at := metav1.Now()
		if live.Status.Summon == nil {
			live.Status.Summon = &v1alpha1.SummonStatus{}
		}
		live.Status.Summon.AdmittedAt = &at
		return k8s.Status().Update(context.Background(), live)
	})
}

func summonedRemediation(s *v1alpha1.AgentSession) {
	s.Spec.Caller, s.Spec.Lane, s.Spec.Profile = "alert-responder", v1alpha1.LaneRemediation, "ops"
	s.Spec.Parent = "alert-ns/alert-responder"
	s.Spec.Summon = remediationSummon()
	s.Spec.Summon.DispatchID = "d-" + s.Name
}

func TestSummonedSessionIsBuiltOnlyAfterAdmission(t *testing.T) {
	for _, lanes := range []bool{false, true} {
		t.Run(fmt.Sprintf("lanes=%v", lanes), func(t *testing.T) {
			startOperator(t, func(r *Reconciler) {
				summonLatchOn(r)
				r.SummonedLanes = lanes
			})
			s := newSession(t, summonedRemediation)
			// A reconcile has passed the gate: the latch is recorded and the
			// status observed. ensure would have created the volume and the
			// pod before that status write.
			waitStatus(t, s.Name, "observed and held", func(st *v1alpha1.AgentSessionStatus) error {
				if st.PrivateHistory == nil || st.PrivateHistory.Admission != sharedNeverStarted || st.Phase == "" {
					return fmt.Errorf("latch %+v phase %q", st.PrivateHistory, st.Phase)
				}
				return nil
			})
			for _, obj := range []struct {
				o    interface{ GetName() string }
				name string
			}{{&corev1.Pod{}, s.Name}, {&corev1.PersistentVolumeClaim{}, HomeClaimName(s.Name)}} {
				var err error
				switch o := obj.o.(type) {
				case *corev1.Pod:
					_, err = get(t, sessionNS, obj.name, o)
				case *corev1.PersistentVolumeClaim:
					_, err = get(t, sessionNS, obj.name, o)
				}
				if !apierrors.IsNotFound(err) {
					t.Fatalf("%s exists before admission (err %v)", obj.name, err)
				}
			}
			admitSession(t, s.Name)
			pod := waitPod(t, s.Name)
			claim := waitClaim(t, s.Name)
			live := session(t, s.Name)
			assertOnlyOwnerIsSession(t, pod.OwnerReferences, live)
			assertOnlyOwnerIsSession(t, claim.OwnerReferences, live)
			if envOf(pod.Spec.Containers[0], protocol.StartDeadlineEnv) == nil {
				t.Fatal("the admitted summoned pod has no start deadline")
			}
		})
	}
}
