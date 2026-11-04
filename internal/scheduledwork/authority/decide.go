package authority

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
)

// Basis is the authority an occurrence dispatches on (DESIGN-054
// "Authority"). The values are also what a template admits, and they are the
// executor's Basis values.
type Basis string

const (
	// BasisNone is no basis: the occurrence parks, or waits when Wait is set.
	BasisNone Basis = ""
	// BasisHuman is a human owner who wrote the current spec.
	BasisHuman Basis = "human"
	// BasisApproved is a human approval of the current spec's hash.
	BasisApproved Basis = "approved"
	// BasisSession is a live session owner who wrote the current spec.
	BasisSession Basis = "session"
)

// The principal classes of spec.owner, spec.editor and approval.by, in the
// form a Reminder records: human/<namespace>/<name>, client/<namespace>/<name>,
// coordinator/<namespace>/<name> or session/<name>/<uid>.
const (
	ClassHuman       = "human"
	ClassClient      = "client"
	ClassCoordinator = "coordinator"
	ClassSession     = "session"
)

// DefaultHeartbeatStaleAfter is a staleness bound for the owner's heartbeat:
// three of agentd's 60-second heartbeats, as the rescue notification reads a
// session's liveness.
const DefaultHeartbeatStaleAfter = 3 * time.Minute

// The reasons Decide gives. A reason with no basis and no wait is a refusal
// the executor parks with; ReasonOwnerUnproven, with Wait, keeps the
// occurrence Pending within its grace.
const (
	ReasonNoItem              = "no scheduled work"
	ReasonOwnerInvalid        = "owner is not a valid principal"
	ReasonEditorNotOwner      = "editor is not the owner"
	ReasonApprovalStale       = "approval does not match the current spec"
	ReasonApprovalNotHuman    = "approval is not by a human"
	ReasonClientNeedsApproval = "client owners need approval"
	ReasonCoordNeedsApproval  = "coordinator owners need approval"
	ReasonOwnerNotFound       = "owner session not found"
	ReasonOwnerMisread        = "owner session read is not the owner's session"
	ReasonOwnerUIDMismatch    = "owner session UID does not match the owner"
	ReasonOwnerDeleting       = "owner session is being deleted"
	ReasonOwnerSuspended      = "owner session is suspended"
	ReasonOwnerEnded          = "owner session ended"
	ReasonOwnerUnproven       = "owner liveness unproven"
)

// Principal is one parsed owner, editor or approver.
type Principal struct {
	// Class is human, client, coordinator or session.
	Class string
	// Namespace and Name are a ServiceAccount's, for every class but
	// session. For a session, Name is the session's name and UID its UID.
	Namespace, Name, UID string
}

// ParsePrincipal parses class/<a>/<b>: exactly three non-empty parts, a known
// class first.
func ParsePrincipal(s string) (Principal, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return Principal{}, fmt.Errorf("%q is not class/<a>/<b>", s)
	}
	switch parts[0] {
	case ClassHuman, ClassClient, ClassCoordinator:
		return Principal{Class: parts[0], Namespace: parts[1], Name: parts[2]}, nil
	case ClassSession:
		return Principal{Class: ClassSession, Name: parts[1], UID: parts[2]}, nil
	}
	return Principal{}, fmt.Errorf("%q has no known class", s)
}

// OwnerSession is the owner session of w, when spec.owner is a session: the
// session the caller reads, by name, before Decide.
func OwnerSession(w *v1alpha1.ScheduledWork) (name, uid string, ok bool) {
	if w == nil {
		return "", "", false
	}
	p, err := ParsePrincipal(w.Spec.Owner)
	if err != nil || p.Class != ClassSession {
		return "", "", false
	}
	return p.Name, p.UID, true
}

// OwnerState is the owner session as the caller read it, uncached, at the
// time of the decision. OwnerStateOf fills it from a read session and pod.
// For an owner that is not a session it is ignored.
type OwnerState struct {
	// Found is a session of the owner's name read; false is NotFound. A
	// failed read is the caller's error, never an OwnerState.
	Found bool
	// Name and UID are the read session's.
	Name, UID string
	// Deleting is a deletion timestamp on the session.
	Deleting bool
	// OperatingMode and Phase are the session's spec.operatingMode and
	// status.phase.
	OperatingMode v1alpha1.OperatingMode
	Phase         v1alpha1.SessionPhase
	// LastHeartbeat is when the operator last accepted a heartbeat from the
	// session's pod (status.agent.lastHeartbeat); zero is none.
	LastHeartbeat time.Time
	// Ended is DESIGN-054's session-end rule (scheduledwork.SessionEnded).
	Ended bool
	// Booting is a session whose agent cannot be proven running yet: agentd
	// has made no report, its boot is not ready (agentd has reported its boot
	// phase since plan 01), or the session has no pod or a pod being deleted.
	Booting bool
	// Draining is a drain onto a new revision in progress: neither live nor
	// ended.
	Draining bool
	// AgentStopped is agentd's report that the agent exited, failed or was
	// interrupted, which the session-end rule has not proven an end (a young
	// pod): not ended, and not live either.
	AgentStopped bool
}

// OwnerStateOf reads an OwnerState from the owner session s and its pod, both
// read uncached at now: s nil is a session not found, pod nil is no pod.
func OwnerStateOf(s *v1alpha1.AgentSession, pod *corev1.Pod, now time.Time) OwnerState {
	if s == nil {
		return OwnerState{}
	}
	st := OwnerState{
		Found:         true,
		Name:          s.Name,
		UID:           string(s.UID),
		Deleting:      !s.DeletionTimestamp.IsZero(),
		OperatingMode: s.Spec.OperatingMode,
		Phase:         s.Status.Phase,
		Draining:      s.Status.Phase == v1alpha1.PhaseDraining,
	}
	st.Ended, _ = scheduledwork.SessionEnded(s, pod, now)
	a := s.Status.Agent
	if a != nil && a.LastHeartbeat != nil {
		st.LastHeartbeat = a.LastHeartbeat.Time
	}
	st.Booting = a == nil || a.Boot != protocol.BootReady || pod == nil || !pod.DeletionTimestamp.IsZero()
	if a != nil {
		switch a.Status {
		case protocol.AgentExited, protocol.AgentFailed, protocol.AgentInterrupted:
			st.AgentStopped = true
		}
	}
	return st
}

// SessionRef names the live owner session by name and UID, for the session
// basis: the executor creates the occurrence's session as its child.
type SessionRef struct {
	Name, UID string
}

// Decision is Decide's answer for one item at one time.
type Decision struct {
	// Basis is the authority the occurrence may dispatch on, or BasisNone.
	Basis Basis
	// Owner is the live owner session, set exactly for BasisSession.
	Owner *SessionRef
	// Wait is an owner session whose liveness is not proven now (booting,
	// draining, a stale heartbeat). It never comes with a basis. The
	// executor keeps the occurrence Pending within its grace, and parks it
	// when grace ends or after the claim.
	Wait bool
	// Reason says why there is no basis; empty with a basis.
	Reason string
}

// Decide is DESIGN-054's authority basis of w at now, from what is true now
// (R7): owner is the owner session as read at now, used only for a session
// owner, and staleAfter bounds the age of its heartbeat. In order:
//
//  1. human approval: status.approval is by a human and its specHash is
//     SpecHash of the current spec. The owner's class is not checked, because
//     a human approved this exact spec; that is how client and coordinator
//     owners run. It comes first, so a matching approval holds whatever the
//     owner's state.
//  2. a client or coordinator owner has no standing basis: it needs
//     approval.
//  3. a standing basis holds only while spec.editor is the owner.
//  4. human owner: BasisHuman.
//  5. live session owner: the session read is the owner's (name and UID), not
//     being deleted, not suspended, not ended by the session-end rule, and
//     proven live: not draining or booting, no stopped agent, operating mode
//     Running, phase Running or Idle, and a heartbeat no older than
//     staleAfter and not later than now. Proven live is BasisSession.
//     Draining, booting, a stopped agent not proven ended, another operating
//     mode or phase that is not an end, or a missing, stale or future
//     heartbeat is Wait, with ReasonOwnerUnproven.
//
// Without a basis, Reason is the standing basis's refusal, and an approval
// that does not hold adds its own reason after "; ".
func Decide(w *v1alpha1.ScheduledWork, owner OwnerState, now time.Time, staleAfter time.Duration) Decision {
	if w == nil {
		return Decision{Reason: ReasonNoItem}
	}
	holds, refusal := approval(w)
	if holds {
		return Decision{Basis: BasisApproved}
	}
	d := standing(w, owner, now, staleAfter)
	if d.Basis == BasisNone && refusal != "" {
		d.Reason += "; " + refusal
	}
	return d
}

// approval reports whether w's approval holds, and why not when w has one
// that does not; refusal is empty when w has no approval.
func approval(w *v1alpha1.ScheduledWork) (holds bool, refusal string) {
	a := w.Status.Approval
	switch {
	case a == nil:
		return false, ""
	case !isHuman(a.By):
		return false, ReasonApprovalNotHuman
	case a.SpecHash != SpecHash(w.Spec):
		return false, ReasonApprovalStale
	}
	return true, ""
}

func isHuman(by string) bool {
	p, err := ParsePrincipal(by)
	return err == nil && p.Class == ClassHuman
}

// standing is the owner's standing basis: the human owner or the live session
// owner, while the owner wrote the current spec.
func standing(w *v1alpha1.ScheduledWork, owner OwnerState, now time.Time, staleAfter time.Duration) Decision {
	p, err := ParsePrincipal(w.Spec.Owner)
	if err != nil {
		return Decision{Reason: ReasonOwnerInvalid}
	}
	switch p.Class {
	case ClassClient:
		return Decision{Reason: ReasonClientNeedsApproval}
	case ClassCoordinator:
		return Decision{Reason: ReasonCoordNeedsApproval}
	}
	if w.Spec.Editor != w.Spec.Owner {
		return Decision{Reason: ReasonEditorNotOwner}
	}
	if p.Class == ClassHuman {
		return Decision{Basis: BasisHuman}
	}
	if why := ownerEnded(p, owner); why != "" {
		return Decision{Reason: why}
	}
	if why := unproven(owner, now, staleAfter); why != "" {
		return Decision{Wait: true, Reason: ReasonOwnerUnproven + ": " + why}
	}
	return Decision{Basis: BasisSession, Owner: &SessionRef{Name: p.Name, UID: p.UID}}
}

// ownerEnded is the refusal, or "", for an owner session that is not the owner's, or
// that will not come back: absent, recreated, being deleted, suspended or
// ended. A suspended or ended session's authority does not outlive it.
func ownerEnded(p Principal, o OwnerState) string {
	switch {
	case !o.Found:
		return ReasonOwnerNotFound
	case o.Name != p.Name:
		return ReasonOwnerMisread
	case o.UID != p.UID:
		return ReasonOwnerUIDMismatch
	case o.Deleting:
		return ReasonOwnerDeleting
	case o.OperatingMode == v1alpha1.OperatingModeSuspended:
		return ReasonOwnerSuspended
	}
	switch o.Phase {
	case v1alpha1.PhaseSuspended, v1alpha1.PhaseArchived, v1alpha1.PhaseFailed:
		return fmt.Sprintf("%s (phase %s)", ReasonOwnerEnded, o.Phase)
	}
	if o.Ended {
		return ReasonOwnerEnded
	}
	return ""
}

// unproven is why a session that has not ended is not proven live, or "".
func unproven(o OwnerState, now time.Time, staleAfter time.Duration) string {
	switch {
	case o.Draining || o.Phase == v1alpha1.PhaseDraining:
		return "draining"
	case o.Booting:
		return "booting"
	case o.AgentStopped:
		return "the agent is not running"
	case o.OperatingMode != v1alpha1.OperatingModeRunning:
		return fmt.Sprintf("operating mode %q is not Running", o.OperatingMode)
	case o.Phase != v1alpha1.PhaseRunning && o.Phase != v1alpha1.PhaseIdle:
		return fmt.Sprintf("phase %q is not Running or Idle", o.Phase)
	case staleAfter <= 0:
		return "no heartbeat staleness bound"
	case o.LastHeartbeat.IsZero():
		return "no heartbeat"
	case o.LastHeartbeat.After(now):
		return "heartbeat is later than now"
	case now.Sub(o.LastHeartbeat) > staleAfter:
		return fmt.Sprintf("heartbeat older than %s", staleAfter)
	}
	return ""
}
