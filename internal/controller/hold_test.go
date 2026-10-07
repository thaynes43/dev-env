package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The envtest cases of D-55, the hold pod: a volume that needs a rescue and has
// no pod to run it in gets one.

// waitHoldPod waits for the session's hold pod and returns it.
func waitHoldPod(t *testing.T, name string) *corev1.Pod {
	t.Helper()
	var pod *corev1.Pod
	eventually(t, "the hold pod of "+name, func() error {
		p, err := get(t, sessionNS, name, &corev1.Pod{})
		if err != nil {
			return err
		}
		if !isHoldPod(p) {
			return fmt.Errorf("pod %s is not a hold pod", p.UID)
		}
		pod = p
		return nil
	})
	return pod
}

// waitEndedPodGone waits until the operator has deleted an ended pod. The API
// server deletes a pod in a terminal phase at once, so a hold pod may already
// have its name (D-55).
func waitEndedPodGone(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	eventually(t, "the ended pod "+pod.Name+" goes", func() error {
		p, err := get(t, sessionNS, pod.Name, &corev1.Pod{})
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		case p.UID != pod.UID && isHoldPod(p):
			return nil
		case p.UID != pod.UID:
			return fmt.Errorf("pod %s, not a hold pod, replaced it", p.UID)
		}
		return errors.New("not deleted yet")
	})
}

// emptyReport is agentd's rescue of a volume no pod wrote to (D-55).
func emptyReport(session string) protocol.RescueReport {
	return protocol.RescueReport{Session: session, Stamp: "20261007-2100", Repos: []protocol.RepoRescue{}, OK: true,
		CleanAndPushed: true, VolumeEmpty: true, Agent: &protocol.AgentStop{}}
}

// Plan 01's Pending check, dev-env-1007-050756: a session whose pod never
// started is reaped. The pod goes without a rescue (nothing ever ran in it), and
// the volume, which holds nothing, gets a hold pod; the rescue in it proves the
// volume empty, and the archive follows. The events tell the story.
func TestAHoldPodArchivesAVolumeWhosePodNeverStarted(t *testing.T) {
	op := startOperator(t)
	s := newSession(t, func(s *v1alpha1.AgentSession) { s.Spec.Size = v1alpha1.SizeL })
	claim := waitClaim(t, s.Name)
	pod := waitPod(t, s.Name)
	reap(t, s.Name)
	hold := waitHoldPod(t, s.Name)
	if hold.UID == pod.UID || hold.Labels[v1alpha1.LabelSize] != "S" || !slices.Equal(hold.Spec.Containers[0].Args, HoldArgs) {
		t.Errorf("hold pod %s, labels %v, args %q", hold.UID, hold.Labels, hold.Spec.Containers[0].Args)
	}
	if envOf(hold.Spec.Containers[0], "CLAUDE_CODE_OAUTH_TOKEN") != nil {
		t.Error("the hold pod has the static token")
	}
	rescuer.answer(t, s.Name, reportFor(emptyReport))
	markRunning(t, hold, "talosw03")
	finishPodDelete(t, hold)
	finishClaimDelete(t, s.Name)
	waitGone(t, s.Name)
	if n := rescuer.callsFor(s.Name); n != 1 {
		t.Errorf("%d rescues, want one, in the hold pod", n)
	}
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name, "delete *v1.PersistentVolumeClaim "+claim.Name, "patch *v1.PersistentVolumeClaim "+claim.Name)
	eventually(t, "the events", func() error {
		var list eventsv1.EventList
		if err := k8s.List(context.Background(), &list, client.InNamespace(sessionNS)); err != nil {
			return err
		}
		var reasons []string
		for _, e := range list.Items {
			if e.Regarding.Name == s.Name {
				reasons = append(reasons, e.Reason)
			}
		}
		for _, want := range []string{"HoldPod", "VolumeEmpty", "Archived"} {
			if !slices.Contains(reasons, want) {
				return fmt.Errorf("events %v, want %s", reasons, want)
			}
		}
		return nil
	})
}

// A hold pod that ended (evicted, say) is replaced by a new one: no agent runs
// in it, so nothing is lost, and the volume still needs its rescue.
func TestAnEndedHoldPodIsReplaced(t *testing.T) {
	startOperator(t)
	s := newSession(t, nil)
	waitClaim(t, s.Name)
	reap(t, s.Name)
	hold := markRunning(t, waitHoldPod(t, s.Name), "talosw02")
	hold.Status.Phase, hold.Status.Reason = corev1.PodFailed, "Evicted"
	if err := k8s.Status().Update(context.Background(), hold); err != nil {
		t.Fatal(err)
	}
	waitEndedPodGone(t, hold)
	eventually(t, "a new hold pod", func() error {
		p := waitHoldPod(t, s.Name)
		if p.UID == hold.UID {
			return fmt.Errorf("still the old hold pod")
		}
		return nil
	})
	if r := session(t, s.Name).Status.Rescue; r != nil {
		t.Errorf("a rescue was recorded, though none could run: %+v", r)
	}
}
