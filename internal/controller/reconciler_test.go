package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// A new session gets its volume and its pod, built from the templates, as the
// API server stores them.
func TestCreatesTheVolumeAndThePod(t *testing.T) {
	op := startOperator(t)
	s := newSession(t, func(s *v1alpha1.AgentSession) { s.Spec.Size = v1alpha1.SizeL })

	claim := waitClaim(t, s.Name)
	if *claim.Spec.StorageClassName != "gasha01-rbd" || claim.Spec.Resources.Requests.Storage().String() != "20Gi" {
		t.Errorf("volume %s: class %s, size %s", claim.Name, *claim.Spec.StorageClassName, claim.Spec.Resources.Requests.Storage())
	}
	pod := waitPod(t, s.Name)
	c := pod.Spec.Containers[0]
	if got := c.Resources.Limits.Cpu().String(); got != "8" {
		t.Errorf("L's CPU limit %s, want 8", got)
	}
	if pod.Spec.PriorityClassName != PriorityClassName || pod.Spec.Priority == nil || *pod.Spec.Priority != -10 {
		t.Errorf("priority class %q, priority %v: the API server resolves -10 from dev-env-agent", pod.Spec.PriorityClassName, pod.Spec.Priority)
	}
	if e := envOf(c, protocol.SessionEnv); e == nil {
		t.Error("no session document")
	} else if doc, err := protocol.ParseSession([]byte(e.Value)); err != nil || doc.Name != s.Name || doc.Base != "origin/main" {
		t.Errorf("session document %+v, %v (base is the CRD's default)", doc, err)
	}

	st := waitStatus(t, s.Name, "Pending with the pod named", func(st *v1alpha1.AgentSessionStatus) error {
		if st.Phase != v1alpha1.PhasePending || st.PodName != s.Name || st.Revision == "" {
			return fmt.Errorf("status %+v", st)
		}
		return nil
	}).Status
	if st.PendingReason != "waiting for the scheduler to place the pod" {
		t.Errorf("pending reason %q", st.PendingReason)
	}
	assertNoPodOrVolumeWrites(t, op)
}

// 5.1, first rule: no owner reference from any session object to the operator
// Deployment. The pod and the volume of every kind of session have exactly one
// owner reference, the session's; and the operator adds none to the session.
func TestInvariantNoOwnerReferenceToTheOperator(t *testing.T) {
	startOperator(t)
	sessions := []*v1alpha1.AgentSession{
		newSession(t, nil),
		newSession(t, func(s *v1alpha1.AgentSession) { s.Spec.Mode, s.Spec.Prompt = v1alpha1.ModeLocal, "" }),
		newSession(t, func(s *v1alpha1.AgentSession) { s.Spec.Mode, s.Spec.Prompt = v1alpha1.ModeRemote, "" }),
		newSession(t, func(s *v1alpha1.AgentSession) {
			s.Spec.Caller, s.Spec.Lane, s.Spec.Profile = "alert-responder", v1alpha1.LaneRemediation, "ops"
		}),
	}
	for _, s := range sessions {
		live := session(t, s.Name)
		assertOnlyOwnerIsSession(t, waitPod(t, s.Name).OwnerReferences, live)
		assertOnlyOwnerIsSession(t, waitClaim(t, s.Name).OwnerReferences, live)
		if len(live.OwnerReferences) != 0 {
			t.Errorf("session %s got owner references %+v", s.Name, live.OwnerReferences)
		}
	}
	// No pod or volume in the namespace points at a Deployment, a ReplicaSet
	// or a pod: a rollout of the operator can cascade to none of them.
	var pods corev1.PodList
	var claims corev1.PersistentVolumeClaimList
	if err := k8s.List(context.Background(), &pods, client.InNamespace(sessionNS)); err != nil {
		t.Fatal(err)
	}
	if err := k8s.List(context.Background(), &claims, client.InNamespace(sessionNS)); err != nil {
		t.Fatal(err)
	}
	var objs []metav1.Object
	for i := range pods.Items {
		objs = append(objs, &pods.Items[i])
	}
	for i := range claims.Items {
		objs = append(objs, &claims.Items[i])
	}
	for _, o := range objs {
		for _, r := range o.GetOwnerReferences() {
			if r.Kind != "AgentSession" || r.APIVersion != v1alpha1.GroupVersion.String() {
				t.Errorf("%s has owner reference %s %s/%s", o.GetName(), r.APIVersion, r.Kind, r.Name)
			}
		}
	}
}

// D-21: a Pending session reports the scheduler's reason as it is.
func TestPendingReportsTheSchedulersReason(t *testing.T) {
	startOperator(t)
	s := newSession(t, nil)
	pod := waitPod(t, s.Name)
	msg := "0/9 nodes are available: 3 Insufficient memory, 6 node(s) didn't match Pod's node affinity/selector."
	pod.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable, Message: msg,
		LastTransitionTime: metav1.Now(),
	}}
	if err := k8s.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	st := waitStatus(t, s.Name, "the scheduler's reason", func(st *v1alpha1.AgentSessionStatus) error {
		if st.PendingReason != msg {
			return fmt.Errorf("pending %q", st.PendingReason)
		}
		return nil
	}).Status
	if c := condition(&st, ConditionPodReady); c == nil || c.Status != metav1.ConditionFalse || c.Reason != corev1.PodReasonUnschedulable {
		t.Errorf("PodReady %+v", c)
	}
}

// A Ready pod makes the session Running, with its node and revision.
func TestRunningReportsThePodAndTheNode(t *testing.T) {
	startOperator(t)
	s := newSession(t, nil)
	pod := markRunning(t, waitPod(t, s.Name), "talosw02")
	st := waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning)).Status
	if st.NodeName != "talosw02" || st.PodName != s.Name || st.PendingReason != "" || st.Revision != pod.Labels[v1alpha1.LabelRevision] {
		t.Errorf("status %+v", st)
	}
	if c := condition(&st, ConditionOutdated); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("Outdated %+v, want False on the current revision", c)
	}
}

// 5.1, second and third rules: an operator restart, and a template change while
// it is down, leave a Running session's pod and volume untouched. The fresh
// operator reads the session, its pod and the templates, resumes, and only
// reports: the pod is outdated. Same UIDs, same resourceVersions, no writes.
func TestInvariantOperatorRestartLeavesARunningPodUntouched(t *testing.T) {
	first := startOperator(t)
	s := newSession(t, nil)
	waitClaim(t, s.Name)
	markRunning(t, waitPod(t, s.Name), "talosw03")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	pod := waitPod(t, s.Name)
	claim := waitClaim(t, s.Name)
	first.stop()

	newImage := strings.Replace(exampleDoc, "0000000000000000000000000000000000000000000000000000000000000001", "0000000000000000000000000000000000000000000000000000000000000002", 1)
	setTemplates(t, newImage)
	second := startOperator(t)

	waitStatus(t, s.Name, "the fresh operator reports the pod outdated", func(st *v1alpha1.AgentSessionStatus) error {
		c := condition(st, ConditionOutdated)
		if c == nil || c.Status != metav1.ConditionTrue {
			return fmt.Errorf("Outdated %+v", c)
		}
		if st.Phase != v1alpha1.PhaseRunning || st.Revision != pod.Labels[v1alpha1.LabelRevision] {
			return fmt.Errorf("status %+v, want Running on the old revision", st)
		}
		return nil
	})
	after := waitPod(t, s.Name)
	afterClaim := waitClaim(t, s.Name)
	if after.UID != pod.UID || after.ResourceVersion != pod.ResourceVersion || after.Spec.Containers[0].Image != pod.Spec.Containers[0].Image {
		t.Errorf("the pod changed across the restart: uid %s to %s, resourceVersion %s to %s, image %s to %s",
			pod.UID, after.UID, pod.ResourceVersion, after.ResourceVersion, pod.Spec.Containers[0].Image, after.Spec.Containers[0].Image)
	}
	if afterClaim.UID != claim.UID || afterClaim.ResourceVersion != claim.ResourceVersion {
		t.Errorf("the volume changed across the restart")
	}
	assertNoPodOrVolumeWrites(t, first, second)
}

// Missing or broken templates leave a new session Pending with the reason; the
// pod follows as soon as they are fixed, through the ConfigMap watch.
func TestTemplatesProblemsArePendingUntilFixed(t *testing.T) {
	startOperator(t)
	deleteTemplates(t)
	s := newSession(t, nil)
	waitStatus(t, s.Name, "Pending on the missing templates", func(st *v1alpha1.AgentSessionStatus) error {
		if st.Phase != v1alpha1.PhasePending || !strings.Contains(st.PendingReason, "not found") {
			return fmt.Errorf("status %+v", st)
		}
		return nil
	})

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace},
		Data:       map[string]string{templates.Key: strings.Replace(exampleDoc, "@sha256:", ":nodigest-", 1)},
	}
	if err := k8s.Create(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s.Name, "Pending on the invalid templates", func(st *v1alpha1.AgentSessionStatus) error {
		if !strings.Contains(st.PendingReason, "not pinned by digest") {
			return fmt.Errorf("pending %q", st.PendingReason)
		}
		return nil
	})
	if _, err := get(t, sessionNS, s.Name, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a pod exists before the templates are valid: %v", err)
	}
	if _, err := get(t, sessionNS, HomeClaimName(s.Name), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a volume exists before the templates are valid: %v", err)
	}

	cm.Data = map[string]string{templates.Key: exampleDoc}
	if err := k8s.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	waitPod(t, s.Name)
}

// A pod that is gone (preempted, or deleted by hand) is replaced on the same
// volume: the session resumes from it (D-20).
func TestAPodThatIsGoneIsRecreatedOnTheSameVolume(t *testing.T) {
	op := startOperator(t)
	s := newSession(t, nil)
	claim := waitClaim(t, s.Name)
	pod := waitPod(t, s.Name)
	if err := k8s.Delete(context.Background(), pod, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a new pod", func() error {
		p, err := get(t, sessionNS, s.Name, &corev1.Pod{})
		if err != nil {
			return err
		}
		if p.UID == pod.UID {
			return fmt.Errorf("still the old pod")
		}
		return nil
	})
	if again := waitClaim(t, s.Name); again.UID != claim.UID {
		t.Errorf("the volume was replaced: %s to %s", claim.UID, again.UID)
	}
	assertNoPodOrVolumeWrites(t, op)
}

// A pod or volume of the session's name that the session does not control is
// never touched, only reported.
func TestAForeignPodIsLeftAlone(t *testing.T) {
	op := startOperator(t)
	name := "foreign-" + fmt.Sprint(seq+1)
	foreign := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: "busybox"}}},
	}
	if err := k8s.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	s := newSession(t, func(s *v1alpha1.AgentSession) { s.Name = name })
	waitStatus(t, s.Name, "Pending on the taken name", func(st *v1alpha1.AgentSessionStatus) error {
		if !strings.Contains(st.PendingReason, "not this session") || st.PodName != "" {
			return fmt.Errorf("status %+v", st)
		}
		return nil
	})
	got, err := get(t, sessionNS, name, &corev1.Pod{})
	if err != nil || got.UID != foreign.UID || got.ResourceVersion != foreign.ResourceVersion || len(got.OwnerReferences) != 0 {
		t.Errorf("the foreign pod changed: %+v, %v", got.ObjectMeta, err)
	}
	assertNoPodOrVolumeWrites(t, op)
}

// A pod that ended (evicted under memory pressure, for example) is kept for its
// record, and the session is Failed with the pod's reason. The operator neither
// deletes it nor starts another in its place (step 5 rescues such a session).
func TestAnEndedPodIsKeptAndReported(t *testing.T) {
	op := startOperator(t)
	s := newSession(t, nil)
	pod := markRunning(t, waitPod(t, s.Name), "talosw01")
	pod.Status.Phase, pod.Status.Reason, pod.Status.Message = corev1.PodFailed, "Evicted", "The node was low on resource: memory."
	if err := k8s.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	st := waitStatus(t, s.Name, "Failed", phaseIs(v1alpha1.PhaseFailed)).Status
	if c := condition(&st, ConditionPodReady); c == nil || !strings.Contains(c.Message, "Evicted") {
		t.Errorf("PodReady %+v", c)
	}
	if got, err := get(t, sessionNS, s.Name, &corev1.Pod{}); err != nil || got.UID != pod.UID {
		t.Errorf("the ended pod is gone or replaced: %v", err)
	}
	assertNoPodOrVolumeWrites(t, op)
}

// A session agentd would refuse fails at once and gets no pod: spec is fixed,
// so it would never start.
func TestASessionAgentdWouldRefuseFails(t *testing.T) {
	startOperator(t)
	s := newSession(t, func(s *v1alpha1.AgentSession) { s.Spec.Prompt = strings.Repeat("x", protocol.MaxPromptBytes+1) })
	st := waitStatus(t, s.Name, "Failed", phaseIs(v1alpha1.PhaseFailed)).Status
	if c := condition(&st, ConditionPodReady); c == nil || c.Reason != "SessionInvalid" {
		t.Errorf("PodReady %+v", c)
	}
	if _, err := get(t, sessionNS, s.Name, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Errorf("an invalid session got a pod: %v", err)
	}
	if _, err := get(t, sessionNS, HomeClaimName(s.Name), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Errorf("an invalid session got a volume: %v", err)
	}
}

// A session created Suspended gets no pod.
func TestASuspendedSessionGetsNoPod(t *testing.T) {
	startOperator(t)
	s := newSession(t, func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended })
	waitStatus(t, s.Name, "Suspended", phaseIs(v1alpha1.PhaseSuspended))
	if _, err := get(t, sessionNS, s.Name, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Errorf("a suspended session got a pod: %v", err)
	}
}

// The cache sees the templates ConfigMap only: another ConfigMap in the
// operator's namespace does not reach the reconciler.
func TestCacheScope(t *testing.T) {
	opts := CacheOptions(sessionNS, templatesKey)
	if _, ok := opts.DefaultNamespaces[sessionNS]; !ok || len(opts.DefaultNamespaces) != 1 {
		t.Errorf("default namespaces %v", opts.DefaultNamespaces)
	}
	r := &Reconciler{Templates: templatesKey}
	other := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: systemNS}}
	if reqs := r.sessionsForTemplates(context.Background(), other); reqs != nil {
		t.Errorf("another ConfigMap enqueued %v", reqs)
	}
}
