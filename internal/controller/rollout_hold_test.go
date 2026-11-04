package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

const (
	rolloutDigest1 = "0000000000000000000000000000000000000000000000000000000000000001"
	rolloutDigest2 = "0000000000000000000000000000000000000000000000000000000000000002"
	rolloutDigest3 = "0000000000000000000000000000000000000000000000000000000000000003"
)

// rolloutDoc is the example templates on the image with digest, holding
// automatic drain for the image with digest hold ("" for no rollout field).
func rolloutDoc(t *testing.T, digest, hold string) string {
	t.Helper()
	doc := strings.Replace(exampleDoc, rolloutDigest1, digest, 1)
	if doc == exampleDoc && digest != rolloutDigest1 {
		t.Fatal("the example image digest was not in the templates")
	}
	if hold != "" {
		tmpl, err := templates.Parse(map[string]string{templates.Key: exampleDoc})
		if err != nil {
			t.Fatal(err)
		}
		doc += "\nrollout:\n  newSessionsOnlyImage: " + strings.Replace(tmpl.Image, rolloutDigest1, hold, 1) + "\n"
	}
	return doc
}

func parseRollout(t *testing.T, digest, hold string) *templates.Templates {
	t.Helper()
	tmpl, err := templates.Parse(map[string]string{templates.Key: rolloutDoc(t, digest, hold)})
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

// While the templates hold automatic drain for their own image, no outdated
// session drains, whatever its mode or agent state, and a pending Draining
// mark without a recorded stop is cancelled. A hold that names another image
// does nothing, so an idle outdated session drains again (Q-03).
func TestNewSessionsOnlyRolloutHoldsAutomaticDrain(t *testing.T) {
	held := parseRollout(t, rolloutDigest2, rolloutDigest2)
	stale := parseRollout(t, rolloutDigest3, rolloutDigest2)
	if !held.NewSessionsOnly() || stale.NewSessionsOnly() {
		t.Fatalf("held %v stale %v", held.NewSessionsOnly(), stale.NewSessionsOnly())
	}
	for _, mode := range []v1alpha1.SessionMode{v1alpha1.ModeTask, v1alpha1.ModeLocal} {
		for _, phase := range []v1alpha1.SessionPhase{v1alpha1.PhaseRunning, v1alpha1.PhaseDraining} {
			t.Run(fmt.Sprintf("held %s %s", mode, phase), func(t *testing.T) {
				s := drainFixture("held")
				s.Spec.Mode, s.Spec.Agent = mode, v1alpha1.AgentClaude
				s.Status.Phase = phase
				pod := readyDrainPod(s.Name, "old-revision")
				cl, stored, storedPod := drainClient(t, s, pod)
				rt := &fakeRestarter{fn: func(*corev1.Pod) (protocol.RestartReport, error) {
					t.Error("unexpected restart read")
					return protocol.RestartReport{}, errors.New("unexpected")
				}}
				r := &Reconciler{Client: cl, APIReader: cl, Restarter: rt}
				obs := observation{pod: owned[*corev1.Pod]{obj: storedPod}, templates: held}
				hold, _, stop, err := r.drainIdle(context.Background(), stored, held, &obs)
				if err != nil || hold || stop {
					t.Fatalf("drainIdle hold %v stop %v err %v", hold, stop, err)
				}
				if got, _ := podPhase(stored, obs); got != v1alpha1.PhaseRunning {
					t.Fatalf("phase %s, want Running", got)
				}
				var got corev1.Pod
				if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &got); err != nil || got.UID != pod.UID || !got.DeletionTimestamp.IsZero() {
					t.Fatal("the held pod changed", err)
				}
				if rt.readCount(pod.Name) != 0 || rt.stopCount(pod.Name) != 0 {
					t.Fatalf("reads %d stops %d, want 0", rt.readCount(pod.Name), rt.stopCount(pod.Name))
				}
			})
		}
	}
	t.Run("stale hold drains an idle task", func(t *testing.T) {
		s := drainFixture("stale")
		s.Spec.Mode = v1alpha1.ModeTask
		pod := readyDrainPod(s.Name, "old-revision")
		if !drainEligible(s, pod, drainRevision(stale)) {
			t.Fatal("a hold for another image kept an idle outdated session")
		}
		if got, _ := podPhase(s, observation{pod: owned[*corev1.Pod]{obj: pod}, templates: stale}); got != v1alpha1.PhaseDraining {
			t.Fatalf("phase %s, want Draining", got)
		}
	})
	t.Run("a recorded stop still finishes", func(t *testing.T) {
		s := drainFixture("held-stopped")
		s.Status.Phase = v1alpha1.PhaseDraining
		pod := readyDrainPod(s.Name, "old-revision")
		s.Status.Restart = &v1alpha1.RestartStatus{ConversationID: "conv-1", PodUID: string(pod.UID), Generation: s.Generation, Stopped: true}
		cl, stored, storedPod := drainClient(t, s, pod)
		rt := &fakeRestarter{}
		r := &Reconciler{Client: cl, APIReader: cl, Restarter: rt}
		obs := observation{pod: owned[*corev1.Pod]{obj: storedPod}, templates: held}
		if _, _, _, err := r.drainIdle(context.Background(), stored, held, &obs); err != nil || rt.stopCount(pod.Name) != 0 {
			t.Fatal("recovery failed", err)
		}
		if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
			t.Fatal("an already stopped pod was not recovered", err)
		}
	})
}

// envtest: a template change that holds automatic drain for its new image
// leaves an idle outdated session Running on its pod, reported Outdated, with
// no prepare-restart. The next image bump, whose hold no longer matches, drains
// it onto the same volume (Q-03 again).
func TestNewSessionsOnlyRolloutKeepsRunningPodsUntilTheNextImage(t *testing.T) {
	rt := &fakeRestarter{fn: func(pod *corev1.Pod) (protocol.RestartReport, error) {
		return protocol.RestartReport{Session: pod.Name, ConversationID: "conv-rollout", Resumable: true}, nil
	}}
	startOperator(t, func(r *Reconciler) { r.Restarter = rt })
	live := newSession(t, nil)
	leaveBusy(t, live.Name)
	origClaim := waitClaim(t, live.Name)
	orig := markRunning(t, waitPod(t, live.Name), "talosw02")
	waitStatus(t, live.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	setTemplates(t, rolloutDoc(t, rolloutDigest2, rolloutDigest2))
	setAgent(t, live.Name, protocol.AgentIdle, time.Now())
	waitStatus(t, live.Name, "held and outdated stays Running", func(st *v1alpha1.AgentSessionStatus) error {
		c := condition(st, ConditionOutdated)
		if c == nil || c.Status != metav1.ConditionTrue {
			return fmt.Errorf("Outdated %+v", c)
		}
		if st.Phase != v1alpha1.PhaseRunning {
			return fmt.Errorf("phase %s", st.Phase)
		}
		return nil
	})
	time.Sleep(2 * time.Second)
	if got := session(t, live.Name); got.Status.Phase != v1alpha1.PhaseRunning || got.Status.Restart != nil {
		t.Fatalf("phase %s restart %+v", got.Status.Phase, got.Status.Restart)
	}
	if p := waitPod(t, live.Name); p.UID != orig.UID || !p.DeletionTimestamp.IsZero() {
		t.Fatal("the held pod was deleted")
	}
	if rt.readCount(live.Name) != 0 || rt.stopCount(live.Name) != 0 {
		t.Fatalf("prepare-restart reads %d stops %d, want 0", rt.readCount(live.Name), rt.stopCount(live.Name))
	}

	setTemplates(t, rolloutDoc(t, rolloutDigest3, rolloutDigest2))
	finishPodDelete(t, orig)
	eventually(t, "the replacement pod after the next image", func() error {
		p, err := get(t, sessionNS, live.Name, &corev1.Pod{})
		if err != nil {
			return err
		}
		if p.UID == orig.UID {
			return errors.New("the outdated pod is still the only pod")
		}
		return nil
	})
	if c := waitClaim(t, live.Name); c.UID != origClaim.UID || !c.DeletionTimestamp.IsZero() {
		t.Fatalf("volume changed: uid %s to %s", origClaim.UID, c.UID)
	}
	if rt.readCount(live.Name) == 0 || rt.stopCount(live.Name) == 0 {
		t.Fatalf("reads %d stops %d after the next image", rt.readCount(live.Name), rt.stopCount(live.Name))
	}
}
