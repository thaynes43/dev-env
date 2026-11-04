package authority

import (
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
		AgentStatus: protocol.AgentBusy, LastHeartbeat: decideNow.Add(-30 * time.Second),
	}
}

func owner(edit func(*OwnerState)) OwnerState {
	o := liveOwner()
	edit(&o)
	return o
}

func unprovenReason(why string) string { return ReasonOwnerUnproven + ": " + why }

var sessionRef = &SessionRef{Name: "coord-1", UID: "uid-1"}

func sameRef(a, b *SessionRef) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func TestDecide(t *testing.T) {
	for name, c := range map[string]struct {
		w     *v1alpha1.ScheduledWork
		owner OwnerState
		stale time.Duration
		want  Decision
	}{
		// Human owner.
		"human owner wrote the spec":        {w: item(humanTom, humanTom), want: Decision{Standing: BasisHuman}},
		"human owner, another human edited": {w: item(humanTom, humanOther), want: Decision{StandingReason: ReasonEditorNotOwner}},
		"human owner, a session edited":     {w: item(humanTom, ownerSess), want: Decision{StandingReason: ReasonEditorNotOwner}},
		"human owner, matching approval":    {w: approved(item(humanTom, humanTom), humanTom), want: Decision{Standing: BasisHuman, Approved: true}},
		"human owner, stale approval":       {w: edited(approved(item(humanTom, humanTom), humanTom)), want: Decision{Standing: BasisHuman, ApprovalReason: ReasonApprovalStale}},
		"human owner, editor, stale":        {w: edited(approved(item(humanTom, humanOther), humanTom)), want: Decision{StandingReason: ReasonEditorNotOwner, ApprovalReason: ReasonApprovalStale}},

		// Human approval: the owner's class is not checked.
		"approval, client owner":        {w: approved(item(clientV1, clientV1), humanTom), want: Decision{StandingReason: ReasonClientNeedsApproval, Approved: true}},
		"approval, coordinator owner":   {w: approved(item(coordinator, coordinator), humanTom), want: Decision{StandingReason: ReasonCoordNeedsApproval, Approved: true}},
		"approval, another editor":      {w: approved(item(ownerSess, humanOther), humanTom), want: Decision{StandingReason: ReasonEditorNotOwner, Approved: true}},
		"approval, owner session gone":  {w: approved(item(ownerSess, ownerSess), humanTom), owner: OwnerState{}, want: Decision{StandingReason: ReasonOwnerNotFound, Approved: true}},
		"approval, owner draining":      {w: approved(item(ownerSess, ownerSess), humanTom), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseDraining }), want: Decision{Wait: true, StandingReason: unprovenReason("draining"), Approved: true}},
		"approval stale after an edit":  {w: edited(approved(item(clientV1, clientV1), humanTom)), want: Decision{StandingReason: ReasonClientNeedsApproval, ApprovalReason: ReasonApprovalStale}},
		"approval by a session":         {w: approved(item(clientV1, clientV1), ownerSess), want: Decision{StandingReason: ReasonClientNeedsApproval, ApprovalReason: ReasonApprovalNotHuman}},
		"approval by a client":          {w: approved(item(coordinator, coordinator), clientV1), want: Decision{StandingReason: ReasonCoordNeedsApproval, ApprovalReason: ReasonApprovalNotHuman}},
		"approval by a malformed human": {w: approved(item(clientV1, clientV1), "human/tom"), want: Decision{StandingReason: ReasonClientNeedsApproval, ApprovalReason: ReasonApprovalNotHuman}},
		"approval with an empty specHash": {w: func() *v1alpha1.ScheduledWork {
			w := approved(item(clientV1, clientV1), humanTom)
			w.Status.Approval.SpecHash = ""
			return w
		}(), want: Decision{StandingReason: ReasonClientNeedsApproval, ApprovalReason: ReasonApprovalStale}},
		"approval kept over editor+cancel": {w: func() *v1alpha1.ScheduledWork {
			w := approved(item(clientV1, clientV1), humanTom)
			w.Spec.Editor, w.Spec.Cancelled = humanOther, true
			return w
		}(), want: Decision{StandingReason: ReasonClientNeedsApproval, Approved: true}},

		// Client and coordinator owners have no standing basis.
		"client owner needs approval":      {w: item(clientV1, clientV1), want: Decision{StandingReason: ReasonClientNeedsApproval}},
		"coordinator owner needs approval": {w: item(coordinator, coordinator), want: Decision{StandingReason: ReasonCoordNeedsApproval}},
		"owner not a principal":            {w: item("robot/a/b", "robot/a/b"), want: Decision{StandingReason: ReasonOwnerInvalid}},
		"owner with four parts":            {w: item("human/a/b/c", "human/a/b/c"), want: Decision{StandingReason: ReasonOwnerInvalid}},
		"no item":                          {w: nil, want: Decision{StandingReason: ReasonNoItem}},

		// Live session owner.
		"session owner live":            {w: item(ownerSess, ownerSess), owner: liveOwner(), want: Decision{Standing: BasisSession, Owner: sessionRef}},
		"session owner idle":            {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseIdle }), want: Decision{Standing: BasisSession, Owner: sessionRef}},
		"agent idle":                    {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.AgentStatus = protocol.AgentIdle }), want: Decision{Standing: BasisSession, Owner: sessionRef}},
		"agent waiting":                 {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.AgentStatus = protocol.AgentWaiting }), want: Decision{Standing: BasisSession, Owner: sessionRef}},
		"heartbeat at the bound":        {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.LastHeartbeat = decideNow.Add(-DefaultHeartbeatStaleAfter) }), want: Decision{Standing: BasisSession, Owner: sessionRef}},
		"heartbeat now":                 {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.LastHeartbeat = decideNow }), want: Decision{Standing: BasisSession, Owner: sessionRef}},
		"live with a leftover approval": {w: approved(item(ownerSess, ownerSess), humanTom), owner: liveOwner(), want: Decision{Standing: BasisSession, Owner: sessionRef, Approved: true}},
		"session owner, human edited":   {w: item(ownerSess, humanTom), owner: liveOwner(), want: Decision{StandingReason: ReasonEditorNotOwner}},
		"session owner, other session":  {w: item(ownerSess, "session/coord-1/uid-2"), owner: liveOwner(), want: Decision{StandingReason: ReasonEditorNotOwner}},

		// An owner session with no basis now: no wait.
		"owner not found":            {w: item(ownerSess, ownerSess), owner: OwnerState{}, want: Decision{StandingReason: ReasonOwnerNotFound}},
		"owner name misread":         {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Name = "coord-2" }), want: Decision{StandingReason: ReasonOwnerMisread}},
		"owner UID mismatch":         {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.UID = "uid-2" }), want: Decision{StandingReason: ReasonOwnerUIDMismatch}},
		"owner deleting":             {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Deleting = true }), want: Decision{StandingReason: ReasonOwnerDeleting}},
		"owner suspended":            {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.OperatingMode = v1alpha1.OperatingModeSuspended }), want: Decision{StandingReason: ReasonOwnerSuspended}},
		"owner phase Failed":         {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseFailed }), want: Decision{StandingReason: ReasonOwnerEnded + " (phase Failed)"}},
		"owner phase Suspended":      {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseSuspended }), want: Decision{StandingReason: ReasonOwnerEnded + " (phase Suspended)"}},
		"owner phase Archived":       {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseArchived }), want: Decision{StandingReason: ReasonOwnerEnded + " (phase Archived)"}},
		"owner ended by rule":        {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Ended = true }), want: Decision{StandingReason: ReasonOwnerEnded}},
		"owner ended while draining": {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Ended, o.Phase = true, v1alpha1.PhaseDraining }), want: Decision{StandingReason: ReasonOwnerEnded}},
		"owner boot failed":          {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.BootFailed = true }), want: Decision{StandingReason: ReasonOwnerBootFailed}},
		"owner gone, stale approval": {w: edited(approved(item(ownerSess, ownerSess), humanTom)), owner: OwnerState{}, want: Decision{StandingReason: ReasonOwnerNotFound, ApprovalReason: ReasonApprovalStale}},

		// An owner whose liveness is not proven: wait.
		"owner phase Draining":   {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhaseDraining }), want: Decision{Wait: true, StandingReason: unprovenReason("draining")}},
		"owner booting":          {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Booting = true }), want: Decision{Wait: true, StandingReason: unprovenReason("booting")}},
		"agent pending":          {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.AgentStatus = protocol.AgentPending }), want: Decision{Wait: true, StandingReason: unprovenReason(`agent status "pending" is not busy, idle or waiting`)}},
		"agent login-required":   {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.AgentStatus = protocol.AgentLoginRequired }), want: Decision{Wait: true, StandingReason: unprovenReason(`agent status "login-required" is not busy, idle or waiting`)}},
		"agent status empty":     {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.AgentStatus = "" }), want: Decision{Wait: true, StandingReason: unprovenReason(`agent status "" is not busy, idle or waiting`)}},
		"agent status unknown":   {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.AgentStatus = "zombie" }), want: Decision{Wait: true, StandingReason: unprovenReason(`agent status "zombie" is not busy, idle or waiting`)}},
		"agent exited, unproven": {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.AgentStatus = protocol.AgentExited }), want: Decision{Wait: true, StandingReason: unprovenReason(`agent status "exited" is not busy, idle or waiting`)}},
		"owner phase Pending":    {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = v1alpha1.PhasePending }), want: Decision{Wait: true, StandingReason: unprovenReason(`phase "Pending" is not Running or Idle`)}},
		"owner no phase":         {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.Phase = "" }), want: Decision{Wait: true, StandingReason: unprovenReason(`phase "" is not Running or Idle`)}},
		"owner no op. mode":      {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.OperatingMode = "" }), want: Decision{Wait: true, StandingReason: unprovenReason(`operating mode "" is not Running`)}},
		"heartbeat stale":        {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.LastHeartbeat = decideNow.Add(-DefaultHeartbeatStaleAfter - time.Second) }), want: Decision{Wait: true, StandingReason: unprovenReason("heartbeat older than 3m0s")}},
		"no heartbeat":           {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.LastHeartbeat = time.Time{} }), want: Decision{Wait: true, StandingReason: unprovenReason("no heartbeat")}},
		"heartbeat in future":    {w: item(ownerSess, ownerSess), owner: owner(func(o *OwnerState) { o.LastHeartbeat = decideNow.Add(time.Second) }), want: Decision{Wait: true, StandingReason: unprovenReason("heartbeat is later than now")}},
		"no staleness bound":     {w: item(ownerSess, ownerSess), owner: liveOwner(), stale: -1, want: Decision{Wait: true, StandingReason: unprovenReason("no heartbeat staleness bound")}},
		"unproven, stale appr.":  {w: edited(approved(item(ownerSess, ownerSess), humanTom)), owner: owner(func(o *OwnerState) { o.Booting = true }), want: Decision{Wait: true, StandingReason: unprovenReason("booting"), ApprovalReason: ReasonApprovalStale}},
		"unproven, editor wrong": {w: item(ownerSess, humanTom), owner: owner(func(o *OwnerState) { o.Booting = true }), want: Decision{StandingReason: ReasonEditorNotOwner}},
	} {
		t.Run(name, func(t *testing.T) {
			s := c.stale
			switch s {
			case 0:
				s = DefaultHeartbeatStaleAfter
			case -1:
				s = 0
			}
			got := Decide(c.w, c.owner, decideNow, s)
			if got.Standing != c.want.Standing || got.Wait != c.want.Wait || got.StandingReason != c.want.StandingReason ||
				got.Approved != c.want.Approved || got.ApprovalReason != c.want.ApprovalReason || !sameRef(got.Owner, c.want.Owner) {
				t.Errorf("Decide = %+v (owner %v), want %+v (owner %v)", got, got.Owner, c.want, c.want.Owner)
			}
			// The invariants Choose relies on.
			if got.Wait && got.Standing != BasisNone {
				t.Error("a wait came with a standing basis")
			}
			if (got.Standing == BasisSession) != (got.Owner != nil) {
				t.Error("the owner is set without the session basis, or the session basis without its owner")
			}
			if (got.Standing == BasisNone) == (got.StandingReason == "") {
				t.Error("no standing basis without a reason, or a reason with one")
			}
			if got.Approved && got.ApprovalReason != "" {
				t.Error("a holding approval with a refusal")
			}
		})
	}
}

// Choose picks the standing basis first when the template admits it, then
// a holding approval, and waits only for an unproven owner the template would
// admit.
func TestChoose(t *testing.T) {
	all := []Basis{BasisHuman, BasisSession, BasisApproved}
	unprovenOwner := owner(func(o *OwnerState) { o.Booting = true })
	for name, c := range map[string]struct {
		w      *v1alpha1.ScheduledWork
		owner  OwnerState
		admits []Basis
		want   Choice
	}{
		// The two rulings of the S3b2 review.
		"human-owned, matching approval, [human]": {w: approved(item(humanTom, humanTom), humanTom), admits: []Basis{BasisHuman}, want: Choice{Basis: BasisHuman}},
		"live owner, leftover approval, [session, approved]": {w: approved(item(ownerSess, ownerSess), humanTom), owner: liveOwner(),
			admits: []Basis{BasisSession, BasisApproved}, want: Choice{Basis: BasisSession, Owner: sessionRef}},

		"human-owned, approval, [approved]":         {w: approved(item(humanTom, humanTom), humanTom), admits: []Basis{BasisApproved}, want: Choice{Basis: BasisApproved}},
		"human-owned, approval, [session]":          {w: approved(item(humanTom, humanTom), humanTom), admits: []Basis{BasisSession}, want: Choice{Reason: "the template does not admit human work; the template does not admit approved work"}},
		"human-owned, no approval, [approved]":      {w: item(humanTom, humanTom), admits: []Basis{BasisApproved}, want: Choice{Reason: "the template does not admit human work"}},
		"human-owned, all":                          {w: item(humanTom, humanTom), admits: all, want: Choice{Basis: BasisHuman}},
		"live owner, approval, [approved]":          {w: approved(item(ownerSess, ownerSess), humanTom), owner: liveOwner(), admits: []Basis{BasisApproved}, want: Choice{Basis: BasisApproved}},
		"live owner, no approval, [human,approved]": {w: item(ownerSess, ownerSess), owner: liveOwner(), admits: []Basis{BasisHuman, BasisApproved}, want: Choice{Reason: "the template does not admit session work"}},
		"unproven owner, approval, [session,appr.]": {w: approved(item(ownerSess, ownerSess), humanTom), owner: unprovenOwner, admits: []Basis{BasisSession, BasisApproved}, want: Choice{Basis: BasisApproved}},
		"unproven owner, [session]":                 {w: item(ownerSess, ownerSess), owner: unprovenOwner, admits: []Basis{BasisSession}, want: Choice{Wait: true, Reason: unprovenReason("booting")}},
		"unproven owner, approval, [session]":       {w: approved(item(ownerSess, ownerSess), humanTom), owner: unprovenOwner, admits: []Basis{BasisSession}, want: Choice{Wait: true, Reason: unprovenReason("booting") + "; the template does not admit approved work"}},
		"unproven owner, [human, approved]":         {w: item(ownerSess, ownerSess), owner: unprovenOwner, admits: []Basis{BasisHuman, BasisApproved}, want: Choice{Reason: "the template does not admit session work"}},
		"unproven owner, stale approval, [session]": {w: edited(approved(item(ownerSess, ownerSess), humanTom)), owner: unprovenOwner, admits: all, want: Choice{Wait: true, Reason: unprovenReason("booting") + "; " + ReasonApprovalStale}},
		"ended owner, approval, all":                {w: approved(item(ownerSess, ownerSess), humanTom), owner: OwnerState{}, admits: all, want: Choice{Basis: BasisApproved}},
		"client, approval, [approved]":              {w: approved(item(clientV1, clientV1), humanTom), admits: []Basis{BasisApproved}, want: Choice{Basis: BasisApproved}},
		"client, approval, [human, session]":        {w: approved(item(clientV1, clientV1), humanTom), admits: []Basis{BasisHuman, BasisSession}, want: Choice{Reason: ReasonClientNeedsApproval + "; the template does not admit approved work"}},
		"client, stale approval, all":               {w: edited(approved(item(clientV1, clientV1), humanTom)), admits: all, want: Choice{Reason: ReasonClientNeedsApproval + "; " + ReasonApprovalStale}},
		"coordinator, no approval, all":             {w: item(coordinator, coordinator), admits: all, want: Choice{Reason: ReasonCoordNeedsApproval}},
		"nothing admitted":                          {w: approved(item(humanTom, humanTom), humanTom), admits: nil, want: Choice{Reason: "the template does not admit human work; the template does not admit approved work"}},
		"BasisNone is never admitted":               {w: item(clientV1, clientV1), admits: []Basis{BasisNone}, want: Choice{Reason: ReasonClientNeedsApproval}},
	} {
		t.Run(name, func(t *testing.T) {
			d := Decide(c.w, c.owner, decideNow, DefaultHeartbeatStaleAfter)
			got := Choose(d, c.admits)
			if got.Basis != c.want.Basis || got.Wait != c.want.Wait || got.Reason != c.want.Reason || !sameRef(got.Owner, c.want.Owner) {
				t.Errorf("Choose = %+v (owner %v), want %+v (owner %v)", got, got.Owner, c.want, c.want.Owner)
			}
			if got.Wait && got.Basis != BasisNone {
				t.Error("a wait came with a basis")
			}
			if (got.Basis == BasisSession) != (got.Owner != nil) {
				t.Error("the owner is set without the session basis, or the session basis without its owner")
			}
			if (got.Basis == BasisNone) == (got.Reason == "") {
				t.Error("no basis without a reason, or a reason with a basis")
			}
			if got.Owner != nil {
				got.Owner.Name = "changed"
				if d.Owner.Name != "coord-1" {
					t.Error("the choice shares its owner with the decision")
				}
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
	if d := Decide(w, OwnerState{}, decideNow, DefaultHeartbeatStaleAfter); !d.Approved {
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

func heartbeatAt(d time.Duration) func(*v1alpha1.AgentSession) {
	return func(s *v1alpha1.AgentSession) { s.Status.Agent.LastHeartbeat = &metav1.Time{Time: decideNow.Add(d)} }
}

func podCreatedAt(d time.Duration) func(*corev1.Pod) {
	return func(p *corev1.Pod) { p.CreationTimestamp = metav1.NewTime(decideNow.Add(d)) }
}

// OwnerStateOf reads a live owner as live, and each way a session is not.
func TestOwnerStateOf(t *testing.T) {
	w := item(ownerSess, ownerSess)
	deleting := metav1.NewTime(decideNow)
	for name, c := range map[string]struct {
		s      func(*v1alpha1.AgentSession)
		absent bool
		noPod  bool
		pod    func(*corev1.Pod)
		want   Decision
	}{
		"live":          {want: Decision{Standing: BasisSession, Owner: sessionRef}},
		"live, idle":    {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentIdle }, want: Decision{Standing: BasisSession, Owner: sessionRef}},
		"live, waiting": {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentWaiting }, want: Decision{Standing: BasisSession, Owner: sessionRef}},
		"resumed, same UID, proven live": {s: heartbeatAt(-20 * time.Second), pod: podCreatedAt(-10 * time.Minute),
			want: Decision{Standing: BasisSession, Owner: sessionRef}},

		"not found": {absent: true, want: Decision{StandingReason: ReasonOwnerNotFound}},
		"recreated": {s: func(s *v1alpha1.AgentSession) { s.UID = "uid-2" }, want: Decision{StandingReason: ReasonOwnerUIDMismatch}},
		"deleting":  {s: func(s *v1alpha1.AgentSession) { s.DeletionTimestamp = &deleting }, want: Decision{StandingReason: ReasonOwnerDeleting}},
		"suspended": {s: func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended }, want: Decision{StandingReason: ReasonOwnerSuspended}},
		"task with verdict": {s: func(s *v1alpha1.AgentSession) {
			s.Spec.Mode = v1alpha1.ModeTask
			s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded}
		}, want: Decision{StandingReason: ReasonOwnerEnded}},
		"agent exited": {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentExited }, want: Decision{StandingReason: ReasonOwnerEnded}},
		"task boot failed": {s: func(s *v1alpha1.AgentSession) {
			s.Spec.Mode, s.Status.Agent.Boot, s.Status.Agent.Status = v1alpha1.ModeTask, protocol.BootFailed, protocol.AgentPending
		}, want: Decision{StandingReason: ReasonOwnerBootFailed}},
		"local boot failed": {s: func(s *v1alpha1.AgentSession) {
			s.Spec.Mode, s.Status.Agent.Boot, s.Status.Agent.Status = v1alpha1.ModeLocal, protocol.BootFailed, protocol.AgentPending
		}, want: Decision{StandingReason: ReasonOwnerBootFailed}},

		"remote boot failed": {s: func(s *v1alpha1.AgentSession) {
			s.Status.Agent.Boot, s.Status.Agent.Status = protocol.BootFailed, protocol.AgentPending
		},
			want: Decision{Wait: true, StandingReason: unprovenReason("booting")}},
		"agent exited, young pod": {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentExited }, pod: podCreatedAt(-time.Minute),
			want: Decision{Wait: true, StandingReason: unprovenReason(`agent status "exited" is not busy, idle or waiting`)}},
		"agent pending":        {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentPending }, want: Decision{Wait: true, StandingReason: unprovenReason(`agent status "pending" is not busy, idle or waiting`)}},
		"agent login-required": {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = protocol.AgentLoginRequired }, want: Decision{Wait: true, StandingReason: unprovenReason(`agent status "login-required" is not busy, idle or waiting`)}},
		"agent status empty":   {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = "" }, want: Decision{Wait: true, StandingReason: unprovenReason(`agent status "" is not busy, idle or waiting`)}},
		"agent status unknown": {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Status = "zombie" }, want: Decision{Wait: true, StandingReason: unprovenReason(`agent status "zombie" is not busy, idle or waiting`)}},
		"heartbeat older than the pod": {s: heartbeatAt(-60 * time.Second), pod: podCreatedAt(-30 * time.Second),
			want: Decision{Wait: true, StandingReason: unprovenReason("booting")}},
		"draining":      {s: func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhaseDraining }, want: Decision{Wait: true, StandingReason: unprovenReason("draining")}},
		"booting":       {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.Boot = protocol.BootBooting }, want: Decision{Wait: true, StandingReason: unprovenReason("booting")}},
		"no agent":      {s: func(s *v1alpha1.AgentSession) { s.Status.Agent = nil }, want: Decision{Wait: true, StandingReason: unprovenReason("booting")}},
		"no pod":        {noPod: true, want: Decision{Wait: true, StandingReason: unprovenReason("booting")}},
		"pod deleting":  {pod: func(p *corev1.Pod) { p.DeletionTimestamp = &deleting }, want: Decision{Wait: true, StandingReason: unprovenReason("booting")}},
		"no heartbeat":  {s: func(s *v1alpha1.AgentSession) { s.Status.Agent.LastHeartbeat = nil }, want: Decision{Wait: true, StandingReason: unprovenReason("booting")}},
		"stale":         {s: heartbeatAt(-50 * time.Minute), want: Decision{Wait: true, StandingReason: unprovenReason("heartbeat older than 3m0s")}},
		"phase Pending": {s: func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhasePending }, want: Decision{Wait: true, StandingReason: unprovenReason(`phase "Pending" is not Running or Idle`)}},
	} {
		t.Run(name, func(t *testing.T) {
			var s *v1alpha1.AgentSession
			if !c.absent {
				s = ownerSession()
				if c.s != nil {
					c.s(s)
				}
			}
			var pod *corev1.Pod
			if !c.noPod {
				pod = ownerPod()
				if c.pod != nil {
					c.pod(pod)
				}
			}
			got := Decide(w, OwnerStateOf(s, pod, decideNow), decideNow, DefaultHeartbeatStaleAfter)
			if got.Standing != c.want.Standing || got.Wait != c.want.Wait || got.StandingReason != c.want.StandingReason || !sameRef(got.Owner, c.want.Owner) {
				t.Errorf("Decide = %+v (owner %v), want %+v", got, got.Owner, c.want)
			}
		})
	}
}
