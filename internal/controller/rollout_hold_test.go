package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
// mark without a recorded stop is cancelled. Removing the field resumes Q-03:
// an idle outdated session drains again.
func TestNewSessionsOnlyRolloutHoldsAutomaticDrain(t *testing.T) {
	held := parseRollout(t, rolloutDigest2, rolloutDigest2)
	resumed := parseRollout(t, rolloutDigest3, "")
	if !held.NewSessionsOnly() || resumed.NewSessionsOnly() {
		t.Fatalf("held %v resumed %v", held.NewSessionsOnly(), resumed.NewSessionsOnly())
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
	t.Run("removing the field drains an idle task", func(t *testing.T) {
		s := drainFixture("resumed")
		s.Spec.Mode = v1alpha1.ModeTask
		pod := readyDrainPod(s.Name, "old-revision")
		if !drainEligible(s, pod, drainRevision(resumed)) {
			t.Fatal("templates without the field kept an idle outdated session")
		}
		if got, _ := podPhase(s, observation{pod: owned[*corev1.Pod]{obj: pod}, templates: resumed}); got != v1alpha1.PhaseDraining {
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

// An outdated session reports the hold on its Outdated condition: True, with
// reason OlderRevisionHeld and a message naming the field. Without the hold the
// reason stays OlderRevision, and a current pod is not Outdated either way.
func TestRolloutHoldIsReportedOnOutdated(t *testing.T) {
	held := parseRollout(t, rolloutDigest2, rolloutDigest2)
	plain := parseRollout(t, rolloutDigest2, "")
	for _, c := range []struct {
		name     string
		tmpl     *templates.Templates
		revision string
		status   metav1.ConditionStatus
		reason   string
		mentions string
	}{
		{"held outdated", held, "old-revision", metav1.ConditionTrue, "OlderRevisionHeld", "rollout.newSessionsOnlyImage"},
		{"plain outdated", plain, "old-revision", metav1.ConditionTrue, "OlderRevision", "the current one is"},
		{"held current", held, held.Revision(), metav1.ConditionFalse, "CurrentRevision", "current template revision"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := drainFixture("report")
			pod := readyDrainPod(s.Name, c.revision)
			st := s.Status.DeepCopy()
			observe(s, observation{pod: owned[*corev1.Pod]{obj: pod}, templates: c.tmpl}, st)
			got := condition(st, ConditionOutdated)
			if got == nil || got.Status != c.status || got.Reason != c.reason || !strings.Contains(got.Message, c.mentions) {
				t.Fatalf("Outdated %+v, want %s %s mentioning %q", got, c.status, c.reason, c.mentions)
			}
			if c.reason == "OlderRevisionHeld" && !strings.Contains(got.Message, held.Image) {
				t.Fatalf("the held message does not name the image: %s", got.Message)
			}
		})
	}
}

// The operator logs the hold state when it loads templates whose state differs
// from the last one it logged: once at the first load, again when the hold
// starts or ends or names a new image, never for an unchanged state.
func TestRolloutHoldIsLoggedWhenItChanges(t *testing.T) {
	var lines []string
	ctx := logr.NewContext(context.Background(), funcr.New(func(prefix, args string) { lines = append(lines, args) }, funcr.Options{}))
	r := &Reconciler{}
	plain := parseRollout(t, rolloutDigest2, "")
	held2 := parseRollout(t, rolloutDigest2, rolloutDigest2)
	held3 := parseRollout(t, rolloutDigest3, rolloutDigest3)
	for _, step := range []struct {
		tmpl *templates.Templates
		want int
		says string
	}{
		{plain, 1, `"held"=false`},
		{plain, 1, ""},
		{held2, 2, `"held"=true`},
		{held2, 2, ""},
		{held3, 3, rolloutDigest3},
		{plain, 4, `"held"=false`},
	} {
		r.noteRolloutHold(ctx, step.tmpl)
		if len(lines) != step.want {
			t.Fatalf("after %d logs, want %d: %q", len(lines), step.want, lines)
		}
		if step.says != "" && !strings.Contains(lines[len(lines)-1], step.says) {
			t.Fatalf("log %q does not say %s", lines[len(lines)-1], step.says)
		}
	}

	// The operator's own templates load reports it.
	lines = nil
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}, Data: map[string]string{templates.Key: rolloutDoc(t, rolloutDigest2, rolloutDigest2)}}
	loader := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build(), Templates: templatesKey}
	if _, err := loader.loadTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], `"held"=true`) {
		t.Fatalf("loadTemplates did not log the hold: %q", lines)
	}
}

// envtest: a template change that holds automatic drain for its new image
// leaves an idle outdated session Running on its pod, reported Outdated with
// reason OlderRevisionHeld, with no prepare-restart. A later image bump that
// leaves the field naming the old image makes the templates invalid: nothing
// drains and the condition says why. Removing the field drains the session
// onto the same volume (Q-03 again).
func TestNewSessionsOnlyRolloutKeepsRunningPodsUntilTheFieldIsRemoved(t *testing.T) {
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
		if c == nil || c.Status != metav1.ConditionTrue || c.Reason != "OlderRevisionHeld" {
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

	// The next image with the field still naming the old one: invalid, so
	// nothing drains, and every session with a pod says why.
	setTemplates(t, rolloutDoc(t, rolloutDigest3, rolloutDigest2))
	waitStatus(t, live.Name, "a mismatched hold is reported", func(st *v1alpha1.AgentSessionStatus) error {
		c := condition(st, ConditionOutdated)
		if c == nil || c.Status != metav1.ConditionUnknown || c.Reason != "TemplatesInvalid" || !strings.Contains(c.Message, "does not match image") {
			return fmt.Errorf("Outdated %+v", c)
		}
		return nil
	})
	time.Sleep(2 * time.Second)
	if p := waitPod(t, live.Name); p.UID != orig.UID || !p.DeletionTimestamp.IsZero() {
		t.Fatal("a mismatched hold drained the pod")
	}
	if rt.readCount(live.Name) != 0 || rt.stopCount(live.Name) != 0 {
		t.Fatalf("a mismatched hold ran prepare-restart: reads %d stops %d", rt.readCount(live.Name), rt.stopCount(live.Name))
	}

	setTemplates(t, rolloutDoc(t, rolloutDigest3, ""))
	finishPodDelete(t, orig)
	eventually(t, "the replacement pod after the field is removed", func() error {
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
		t.Fatalf("reads %d stops %d after the field was removed", rt.readCount(live.Name), rt.stopCount(live.Name))
	}
}
