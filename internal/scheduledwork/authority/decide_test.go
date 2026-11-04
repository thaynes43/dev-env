package authority

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	humanTom    = "human/dev-env-system/tom"
	humanOther  = "human/dev-env-system/other"
	ownerSess   = "session/coord-1/uid-1"
	clientV1    = "client/dev/dev-env"
	coordinator = "coordinator/dev-env-system/dot"
)

var decideNow = time.Date(2030, 6, 3, 14, 0, 0, 0, time.UTC)

func item(owner, editor string) *v1alpha1.ScheduledWork {
	w := &v1alpha1.ScheduledWork{Spec: goldenSpec()}
	w.Spec.Owner, w.Spec.Editor = owner, editor
	return w
}

// approved adds an approval by by of w's spec as it is now.
func approved(w *v1alpha1.ScheduledWork, by string) *v1alpha1.ScheduledWork {
	w.Status.Approval = &v1alpha1.ScheduledWorkApproval{By: by, At: metav1.NewTime(decideNow.Add(-time.Hour)), SpecHash: SpecHash(w.Spec)}
	return w
}

// edited changes w's prompt after its approval.
func edited(w *v1alpha1.ScheduledWork) *v1alpha1.ScheduledWork {
	w.Spec.Work.Prompt += " and more"
	return w
}

func liveOwner() OwnerState {
	return OwnerState{
		Found: true, Name: "coord-1", UID: "uid-1",
		OperatingMode: v1alpha1.OperatingModeRunning, Phase: v1alpha1.PhaseRunning,
		LastHeartbeat: decideNow.Add(-30 * time.Second),
	}
}

func owner(edit func(*OwnerState)) OwnerState {
	o := liveOwner()
	edit(&o)
	return o
}

func unprovenReason(why string) string { return ReasonOwnerUnproven + ": " + why }

func TestDecide(t *testing.T) {
	sessionRef := &SessionRef{Name: "coord-1", UID: "uid-1"}
	for name, c := range map[string]struct {
		w     *v1alpha1.ScheduledWork
		owner OwnerState
		stale time.Duration
		want  Decision
	}{
		// Human owner.
		"human owner wrote the spec":         {w: item(humanTom, humanTom), want: Decision{Basis: BasisHuman}},
		"human owner, another human edited":  {w: item(humanTom, humanOther), want: Decision{Reason: ReasonEditorNotOwner}},
		"human owner, a session edited":      {w: item(humanTom, ownerSess), want: Decision{Reason: ReasonEditorNotOwner}},
		"human owner, matching approval":     {w: approved(item(humanTom, humanTom), humanTom), want: Decision{Basis: BasisApproved}},
		"human owner holds past stale appr.": {w: edited(approved(item(humanTom, humanTom), humanTom)), want: Decision{Basis: BasisHuman}},
		"human owner, editor and stale appr": {w: edited(approved(item(humanTom, humanOther), humanTom)), want: Decision{Reason: ReasonEditorNotOwner + "; " + ReasonApprovalStale}},

		// Human approval: the owner's class is not checked.
		"approval, client owner":        {w: approved(item(clientV1, clientV1), humanTom), want: Decision{Basis: BasisApproved}},
		"approval, coordinator owner":   {w: approved(item(coordinator, coordinator), humanTom), want: Decision{Basis: BasisApproved}},
		"approval, another editor":      {w: approved(item(ownerSess, humanOther), humanTom), want: Decision{Basis: BasisApproved}},
		"approval, owner session gone":  {w: approved(item(ownerSess, ownerSess), humanTom), owner: OwnerState{}, want: Decision{Basis: BasisApproved}},
		"approval, owner draining":      {w: approved(item(ownerSess, ownerSess), humanTom), owner: owner(func(o *OwnerState) { o.Draining = true }), want: Decision{Basis: BasisApproved}},
		"approval stale after an edit":  {w: edited(approved(item(clientV1, clientV1), humanTom)), want: Decision{Reason: ReasonClientNeedsApproval + "; " + ReasonApprovalStale}},
		"approval by a session":         {w: approved(item(clientV1, clientV1), ownerSess), want: Decision{Reason: ReasonClientNeedsApproval + "; " + ReasonApprovalNotHuman}},
		"approval by a client":          {w: approved(item(coordinator, coordinator), clientV1), want: Decision{Reason: ReasonCoordNeedsApproval + "; " + ReasonApprovalNotHuman}},
		"approval by a malformed human": {w: approved(item(clientV1, clientV1), "human/tom"), want: Decision{Reason: ReasonClientNeedsApproval + "; " + ReasonApprovalNotHuman}},
		"approval with an empty specHash": {w: func() *v1alpha1.ScheduledWork {
			w := approved(item(clientV1, clientV1), humanTom)
			w.Status.Approval.SpecHash = ""
			return w
		}(), want: Decision{Reason: ReasonClientNeedsApproval + "; " + ReasonApprovalStale}},
		"approval kept over editor+cancel": {w: func() *v1alpha1.ScheduledWork {
			w := approved(item(clientV1, clientV1), humanTom)
			w.Spec.Editor, w.Spec.Cancelled = humanOther, true
			return w
		}(), want: Decision{Basis: BasisApproved}},

		// Client and coordinator owners have no standing basis.
		"client owner needs approval":      {w: item(clientV1, clientV1), want: Decision{Reason: ReasonClientNeedsApproval}},
		"coordinator owner needs approval": {w: item(coordinator, coordinator), want: Decision{Reason: ReasonCoordNeedsApproval}},
		"owner not a principal":            {w: item("robot/a/b", "robot/a/b"), want: Decision{Reason: ReasonOwnerInvalid}},
		"owner with four parts":            {w: item("human/a/b/c", "human/a/b/c"), want: Decision{Reason: ReasonOwnerInvalid}},
		"no item":                          {w: nil, want: Decision{Reason: ReasonNoItem}},

		// Live session owner.
		"session owner live":           {w: item(ownerSess, ownerSess), owner: liveOwner(), want: Decision{Basis: BasisSession, Owner: sessionRef}},
		"session owner idle":           {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseIdle }), want: Decision{Basis: BasisSession, Owner: sessionRef}},
		"heartbeat at the bound":       {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.LastHeartbeat = decideNow.Add(-DefaultHeartbeatStaleAfter) }), want: Decision{Basis: BasisSession, Owner: sessionRef}},
		"heartbeat now":                {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.LastHeartbeat = decideNow }), want: Decision{Basis: BasisSession, Owner: sessionRef}},
		"session owner, human edited":  {w: item(ownerSess, humanTom), owner: liveOwner(), want: Decision{Reason: ReasonEditorNotOwner}},
		"session owner, other session": {w: item(ownerSess, "session/coord-1/uid-2"), owner: liveOwner(), want: Decision{Reason: ReasonEditorNotOwner}},

		// An owner session that will not come back: no basis, no wait.
		"owner not found":     {w: item(ownerSess, ownerSess), owner: OwnerState{}, want: Decision{Reason: ReasonOwnerNotFound}},
		"owner name misread":  {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Name = "coord-2" }), want: Decision{Reason: ReasonOwnerMisread}},
		"owner UID mismatch":  {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.UID = "uid-2" }), want: Decision{Reason: ReasonOwnerUIDMismatch}},
		"owner deleting":      {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Deleting = true }), want: Decision{Reason: ReasonOwnerDeleting}},
		"owner suspended":     {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.OperatingMode = v1alpha1.OperatingModeSuspended }), want: Decision{Reason: ReasonOwnerSuspended}},
		"owner phase Failed":  {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseFailed }), want: Decision{Reason: ReasonOwnerEnded + " (phase Failed)"}},
		"owner phase Susp.":   {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseSuspended }), want: Decision{Reason: ReasonOwnerEnded + " (phase Suspended)"}},
		"owner phase Archive": {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseArchived }), want: Decision{Reason: ReasonOwnerEnded + " (phase Archived)"}},
		"owner ended by rule": {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Ended = true }), want: Decision{Reason: ReasonOwnerEnded}},
		"owner ended, draining too": {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Ended, o.Draining = true, true }),
			want: Decision{Reason: ReasonOwnerEnded}},
		"owner gone, stale approval": {w: edited(approved(item(ownerSess, ownerSess), humanTom)), owner: OwnerState{}, want: Decision{Reason: ReasonOwnerNotFound + "; " + ReasonApprovalStale}},

		// An owner whose liveness is not proven: wait.
		"owner draining":         {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Draining = true }), want: Decision{Wait: true, Reason: unprovenReason("draining")}},
		"owner phase Draining":   {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseDraining }), want: Decision{Wait: true, Reason: unprovenReason("draining")}},
		"owner booting":          {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Booting = true }), want: Decision{Wait: true, Reason: unprovenReason("booting")}},
		"owner agent stopped":    {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.AgentStopped = true }), want: Decision{Wait: true, Reason: unprovenReason("the agent is not running")}},
		"owner phase Pending":    {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhasePending }), want: Decision{Wait: true, Reason: unprovenReason(`phase "Pending" is not Running or Idle`)}},
		"owner no phase":         {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = "" }), want: Decision{Wait: true, Reason: unprovenReason(`phase "" is not Running or Idle`)}},
		"owner no op. mode":      {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.OperatingMode = "" }), want: Decision{Wait: true, Reason: unprovenReason(`operating mode "" is not Running`)}},
		"heartbeat stale":        {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.LastHeartbeat = decideNow.Add(-DefaultHeartbeatStaleAfter - time.Second) }), want: Decision{Wait: true, Reason: unprovenReason("heartbeat older than 3m0s")}},
		"no heartbeat":           {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.LastHeartbeat = time.Time{} }), want: Decision{Wait: true, Reason: unprovenReason("no heartbeat")}},
		"heartbeat in future":    {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.LastHeartbeat = decideNow.Add(time.Second) }), want: Decision{Wait: true, Reason: unprovenReason("heartbeat is later than now")}},
		"no staleness bound":     {w: item(ownerSess, ownerSess), owner: liveOwner(), stale: -1, want: Decision{Wait: true, Reason: unprovenReason("no heartbeat staleness bound")}},
		"unproven, stale appr.":  {w: edited(approved(item(ownerSess, ownerSess), humanTom)), owner: owner(func(o *OwnerState) { o.Booting = true }), want: Decision{Wait: true, Reason: unprovenReason("booting") + "; " + ReasonApprovalStale}},
		"unproven, editor wrong": {w: item(ownerSess, humanTom), owner: owner(func(o *OwnerState) { o.Booting = true }), want: Decision{Reason: ReasonEditorNotOwner}},
	} {
		t.Run(name, func(t *testing.T) {
			stale := c.stale
			switch stale {
			case 0:
				stale = DefaultHeartbeatStaleAfter
			case -1:
				stale = 0
			}
			got := Decide(c.w, c.owner, decideNow, stale)
			if got.Basis != c.want.Basis || got.Wait != c.want.Wait || got.Reason != c.want.Reason ||
				(got.Owner == nil) != (c.want.Owner == nil) || (got.Owner != nil && *got.Owner != *c.want.Owner) {
				t.Errorf("Decide = %+v (owner %v), want %+v (owner %v)", got, got.Owner, c.want, c.want.Owner)
			}
			// The invariants the executor relies on.
			if got.Wait && got.Basis != BasisNone {
				t.Error("a wait came with a basis")
			}
			if (got.Basis == BasisSession) != (got.Owner != nil) {
				t.Error("the owner is set without the session basis, or the session basis without its owner")
			}
			if (got.Basis == BasisNone) == (got.Reason == "") {
				t.Error("a refusal without a reason, or a reason with a basis")
			}
		})
	}
}

// Decide reads the item and never changes it.
func TestDecideLeavesItem(t *testing.T) {
	w := approved(item(clientV1, clientV1), humanTom)
	w.Spec.Notify = []string{"pushover:phone", "pushover:default"}
	w.Status.Approval.SpecHash = SpecHash(w.Spec)
	before := w.DeepCopy()
	if d := Decide(w, OwnerState{}, decideNow, DefaultHeartbeatStaleAfter); d.Basis != BasisApproved {
		t.Fatalf("Decide = %+v", d)
	}
	if w.Spec.Notify[0] != before.Spec.Notify[0] || w.Status.Approval.SpecHash != before.Status.Approval.SpecHash {
		t.Error("Decide changed the item")
	}
}

func TestParsePrincipal(t *testing.T) {
	for in, want := range map[string]Principal{
		humanTom:    {Class: ClassHuman, Namespace: "dev-env-system", Name: "tom"},
		clientV1:    {Class: ClassClient, Namespace: "dev", Name: "dev-env"},
		coordinator: {Class: ClassCoordinator, Namespace: "dev-env-system", Name: "dot"},
		ownerSess:   {Class: ClassSession, Name: "coord-1", UID: "uid-1"},
	} {
		got, err := ParsePrincipal(in)
		if err != nil || got != want {
			t.Errorf("ParsePrincipal(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "human", "human/tom", "human//tom", "human/a/", "human/a/b/c", "robot/a/b", "Human/a/b"} {
		if _, err := ParsePrincipal(in); err == nil {
			t.Errorf("ParsePrincipal(%q) accepted", in)
		}
	}
	if name, uid, ok := OwnerSession(item(ownerSess, ownerSess)); !ok || name != "coord-1" || uid != "uid-1" {
		t.Errorf("OwnerSession = %q %q %v", name, uid, ok)
	}
	for _, w := range []*v1alpha1.ScheduledWork{nil, item(humanTom, humanTom), item("session/x", "session/x")} {
		if _, _, ok := OwnerSession(w); ok {
			t.Errorf("OwnerSession of %v is a session", w)
		}
	}
}

func ownerSession() *v1alpha1.AgentSession {
	hb := metav1.NewTime(decideNow.Add(-20 * time.Second))
	return &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "coord-1", UID: types.UID("uid-1")},
		Spec:       v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeRemote, OperatingMode: v1alpha1.OperatingModeRunning},
		Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning, Agent: &v1alpha1.AgentStatus{
			Status: protocol.AgentBusy, Boot: protocol.BootReady, LastHeartbeat: &hb,
		}},
	}
}

func ownerPod() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "coord-1", CreationTimestamp: metav1.NewTime(decideNow.Add(-time.Hour))}}
}

// OwnerStateOf reads a live owner as live, and each way a session is not.
func TestOwnerStateOf(t *testing.T) {
	w := item(ownerSess, ownerSess)
	decide := func(s *v1alpha1.AgentSession, pod *corev1.Pod) Decision {
		return Decide(w, OwnerStateOf(s, pod, decideNow), decideNow, DefaultHeartbeatStaleAfter)
	}
	if d := decide(ownerSession(), ownerPod()); d.Basis != BasisSession {
		t.Fatalf("a live owner: %+v", d)
	}
	deleting := metav1.NewTime(decideNow)
	for name, c := range map[string]struct {
		s      func(*v1alpha1.AgentSession)
		noPod  bool
		pod    func(*corev1.Pod)
		reason string
		wait   bool
	}{
		"not found": {s: nil, reason: ReasonOwnerNotFound},
		"recreated": {s: func(s *v1alpha1.AgentSession) { s.UID = "uid-2" }, reason: ReasonOwnerUIDMismatch},
		"deleting":  {s: func(s *v1alpha1.AgentSession) { s.DeletionTimestamp = &deleting }, reason: ReasonOwnerDeleting},
		"suspended": {s: func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended }, reason: ReasonOwnerSuspended},
		"task with verdict": {s: func(s *v1alpha1.AgentSession) {
			s.Spec.Mode = v1alpha1.ModeTask
			s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded}
		}, reason: ReasonOwnerEnded},
		"agent exited": {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentExited }, reason: ReasonOwnerEnded},
		"agent exited, young pod": {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentExited },
			pod: func(p *corev1.Pod) { p.CreationTimestamp = metav1.NewTime(decideNow.Add(-time.Minute)) }, reason: unprovenReason("the agent is not running"), wait: true},
		"draining":      {s: func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhaseDraining }, reason: unprovenReason("draining"), wait: true},
		"booting":       {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Boot = protocol.BootBooting }, reason: unprovenReason("booting"), wait: true},
		"no agent":      {s: func(s *v1alpha1.AgentSession) { s.Status.Agent = nil }, reason: unprovenReason("booting"), wait: true},
		"no pod":        {s: func(*v1alpha1.AgentSession) {}, noPod: true, reason: unprovenReason("booting"), wait: true},
		"pod deleting":  {s: func(*v1alpha1.AgentSession) {}, pod: func(p *corev1.Pod) { p.DeletionTimestamp = &deleting }, reason: unprovenReason("booting"), wait: true},
		"stale":         {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.LastHeartbeat.Time = decideNow.Add(-time.Hour) }, reason: unprovenReason("heartbeat older than 3m0s"), wait: true},
		"no heartbeat":  {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.LastHeartbeat = nil }, reason: unprovenReason("no heartbeat"), wait: true},
		"phase Pending": {s: func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhasePending }, reason: unprovenReason(`phase "Pending" is not Running or Idle`), wait: true},
	} {
		t.Run(name, func(t *testing.T) {
			var s *v1alpha1.AgentSession
			if c.s != nil {
				s = ownerSession()
				c.s(s)
			}
			var pod *corev1.Pod
			if !c.noPod {
				pod = ownerPod()
				if c.pod != nil {
					c.pod(pod)
				}
			}
			d := decide(s, pod)
			if d.Basis != BasisNone || d.Reason != c.reason || d.Wait != c.wait {
				t.Errorf("Decide = %+v, want reason %q wait %v", d, c.reason, c.wait)
			}
			if strings.HasPrefix(d.Reason, ReasonOwnerUnproven) != d.Wait {
				t.Errorf("Decide = %+v: an unproven owner waits, any other refusal does not", d)
			}
		})
	}
}
