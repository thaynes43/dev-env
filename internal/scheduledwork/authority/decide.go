package authority

import (
	"fmt"
	"slices"
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

// liveAgentStatuses are the agent statuses that count as a running agent, as
// the rescue notification's owner check reads them
// (controller/rescue_notification.go): busy, idle and waiting. Every other
// status, pending, login-required, the end statuses, none and any unknown
// one, is not proof of a live agent.
var liveAgentStatuses = []string{protocol.AgentBusy, protocol.AgentIdle, protocol.AgentWaiting}

// The reasons Decide and Choose give. A reason with no basis and no wait is
// a refusal the executor parks with; ReasonOwnerUnproven, with Wait, keeps
// the occurrence Pending within its grace.
const (
	ReasonNoItem              = "no scheduled work"
	ReasonOwnerInvalid        = "owner is not a valid principal"
	ReasonEditorNotOwner      = "editor is not the owner"
	ReasonApprovalStale       = "approval does not match the current spec"
	ReasonApprovalNotHuman    = "approval is not by a human"
	ReasonApprovalNoTemplate  = "approval records no template digest"
	ReasonApprovalTemplate    = "approval does not match the current template"
	ReasonClientNeedsApproval = "client owners need approval"
	ReasonCoordNeedsApproval  = "coordinator owners need approval"
	ReasonOwnerNotFound       = "owner session not found"
	ReasonOwnerMisread        = "owner session read is not the owner's session"
	ReasonOwnerUIDMismatch    = "owner session UID does not match the owner"
	ReasonOwnerDeleting       = "owner session is being deleted"
	ReasonOwnerSuspended      = "owner session is suspended"
	ReasonOwnerEnded          = "owner session ended"
	ReasonOwnerBootFailed     = "owner session's boot failed"
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
	// status.phase. Phase Draining is a drain onto a new revision: neither
	// live nor ended.
	OperatingMode v1alpha1.OperatingMode
	Phase         v1alpha1.SessionPhase
	// AgentStatus is agentd's last agent status (status.agent.status). Only
	// busy, idle and waiting count as live.
	AgentStatus string
	// LastHeartbeat is when the operator last accepted a heartbeat from the
	// session's pod (status.agent.lastHeartbeat); zero is none.
	LastHeartbeat time.Time
	// Ended is DESIGN-054's session-end rule (scheduledwork.SessionEnded).
	Ended bool
	// BootFailed is agentd's report of a failed boot that cannot recover in
	// the same pod, made by the current pod: an end for an owner session.
	BootFailed bool
	// Booting is a session whose current pod has not proven a running agent:
	// agentd has made no report, its boot is not ready, its heartbeat is
	// absent or older than the pod (a report of the previous pod), or the
	// session has no pod or a pod being deleted.
	Booting bool
}

// OwnerStateOf reads an OwnerState from the owner session s and its pod, both
// read uncached at now: s nil is a session not found, pod nil is no pod.
//
// A failed boot reported by the current pod (a pod exists and the heartbeat is
// not older than it) ends a task or local owner, which agentd never starts
// again in the same pod. The previous pod's failed boot is only Booting: the
// owner may have moved to a new pod. A remote owner's failed boot is Booting
// too: agentd may start a remote agent again after a failed boot, for a held
// launch only, once a login window ends, and then reports the boot ready
// (agentd recordAgentStep).
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
	}
	st.Ended, _ = scheduledwork.SessionEnded(s, pod, now)
	a := s.Status.Agent
	if a == nil {
		st.Booting = true
		return st
	}
	st.AgentStatus = a.Status
	if a.LastHeartbeat != nil {
		st.LastHeartbeat = a.LastHeartbeat.Time
	}
	thisPod := pod != nil && a.LastHeartbeat != nil && !a.LastHeartbeat.Time.Before(pod.CreationTimestamp.Time)
	st.BootFailed = a.Boot == protocol.BootFailed && s.Spec.Mode != v1alpha1.ModeRemote && thisPod
	st.Booting = a.Boot != protocol.BootReady || pod == nil || !pod.DeletionTimestamp.IsZero() ||
		a.LastHeartbeat == nil || a.LastHeartbeat.Time.Before(pod.CreationTimestamp.Time)
	return st
}

// SessionRef names the live owner session by name and UID, for the session
// basis: the executor creates the occurrence's session as its child.
type SessionRef struct {
	Name, UID string
}

// Decision holds both candidate bases of one item at one time, the owner's
// standing basis and a human approval. Choose picks the one an occurrence
// dispatches on, against the template's admitted bases.
type Decision struct {
	// Standing is the owner's standing basis, BasisHuman or BasisSession, or
	// BasisNone.
	Standing Basis
	// Owner is the live owner session, set exactly when Standing is
	// BasisSession.
	Owner *SessionRef
	// Wait is a session owner who wrote the current spec but whose liveness
	// is not proven now (booting, draining, an agent status that is not live,
	// a stale heartbeat). It never comes with a standing basis.
	Wait bool
	// StandingReason says why there is no standing basis.
	StandingReason string
	// Approved is a human approval of the current spec's hash, under the
	// current template, that holds.
	Approved bool
	// ApprovalReason says why the item's approval does not hold; empty when
	// it holds or the item has none.
	ApprovalReason string
}

// TemplateBinding is what an approval's template check compares: Current is
// TemplateDigest of the template the item names, read now; Recorded is the
// digest the approval recorded when the human approved. S5 adds that field to
// status.approval (templateDigest; the S2 type has none), so until then the
// caller passes what S5 stores. An empty Recorded voids the approval, so
// neither S5 nor the wiring can skip the check by leaving it out.
type TemplateBinding struct {
	Current, Recorded string
}

// Decide is DESIGN-054's two candidate bases of w at now, from what is true
// now (R7). owner is the owner session as read at now, used only for a
// session owner; tmpl is the approval's template check; staleAfter bounds
// the age of the owner's heartbeat.
//
// The approval holds when status.approval is by a human, its specHash is
// SpecHash of the current spec, and its recorded template digest is the
// current template's (an empty recorded digest never matches). The owner's
// class is not checked, because a human approved this exact spec under this
// exact template; that is how client and coordinator owners run.
//
// The standing basis, in order:
//  1. a client or coordinator owner has none: it needs approval;
//  2. it holds only while spec.editor is the owner;
//  3. a human owner is BasisHuman;
//  4. a session owner is BasisSession when the session read is the owner's
//     (name and UID), not being deleted, suspended, ended by the session-end
//     rule or failed at boot, and is proven live: not draining or booting,
//     an agent status of busy, idle or waiting, operating mode Running,
//     phase Running or Idle, and a heartbeat no older than staleAfter and not
//     later than now. A session that has not ended but is not proven live is
//     Wait, with ReasonOwnerUnproven.
func Decide(w *v1alpha1.ScheduledWork, owner OwnerState, tmpl TemplateBinding, now time.Time, staleAfter time.Duration) Decision {
	if w == nil {
		return Decision{StandingReason: ReasonNoItem}
	}
	d := standing(w, owner, now, staleAfter)
	d.Approved, d.ApprovalReason = approval(w, tmpl)
	return d
}

// approval reports whether w's approval holds under tmpl, and why not when w
// has one that does not; refusal is empty when w has no approval.
func approval(w *v1alpha1.ScheduledWork, tmpl TemplateBinding) (holds bool, refusal string) {
	a := w.Status.Approval
	switch {
	case a == nil:
		return false, ""
	case !isHuman(a.By):
		return false, ReasonApprovalNotHuman
	case a.SpecHash != SpecHash(w.Spec):
		return false, ReasonApprovalStale
	case tmpl.Recorded == "":
		return false, ReasonApprovalNoTemplate
	case tmpl.Recorded != tmpl.Current:
		return false, ReasonApprovalTemplate
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
		return Decision{StandingReason: ReasonOwnerInvalid}
	}
	switch p.Class {
	case ClassClient:
		return Decision{StandingReason: ReasonClientNeedsApproval}
	case ClassCoordinator:
		return Decision{StandingReason: ReasonCoordNeedsApproval}
	}
	if w.Spec.Editor != w.Spec.Owner {
		return Decision{StandingReason: ReasonEditorNotOwner}
	}
	if p.Class == ClassHuman {
		return Decision{Standing: BasisHuman}
	}
	if why := ownerEnded(p, owner); why != "" {
		return Decision{StandingReason: why}
	}
	if why := unproven(owner, now, staleAfter); why != "" {
		return Decision{Wait: true, StandingReason: ReasonOwnerUnproven + ": " + why}
	}
	return Decision{Standing: BasisSession, Owner: &SessionRef{Name: p.Name, UID: p.UID}}
}

// ownerEnded is the refusal, or "", for an owner session that is not the
// owner's or has no basis now: absent, recreated (another UID is another
// owner), being deleted, suspended, ended or failed at boot. A suspended
// owner has no basis while it is suspended. Its authority was not revoked:
// once it is resumed with the same UID and proven live, it has its basis
// again.
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
	switch {
	case o.Ended:
		return ReasonOwnerEnded
	case o.BootFailed:
		return ReasonOwnerBootFailed
	}
	return ""
}

// unproven is why a session that has not ended is not proven live, or "".
func unproven(o OwnerState, now time.Time, staleAfter time.Duration) string {
	switch {
	case o.Phase == v1alpha1.PhaseDraining:
		return "draining"
	case o.Booting:
		return "booting"
	case !slices.Contains(liveAgentStatuses, o.AgentStatus):
		return fmt.Sprintf("agent status %q is not busy, idle or waiting", o.AgentStatus)
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

// Choice is the one basis an occurrence dispatches on under one template.
type Choice struct {
	// Basis is the chosen basis, or BasisNone.
	Basis Basis
	// Owner is the live owner session, set exactly for BasisSession.
	Owner *SessionRef
	// Wait keeps the occurrence Pending within its grace: the owner's
	// liveness is unproven, the template admits session work, and no admitted
	// approval holds. It never comes with a basis.
	Wait bool
	// Reason says why there is no basis; empty with a basis.
	Reason string
}

// Choose picks d's basis under a template that admits admits. The standing
// basis comes first when it holds and the template admits it, because it is
// the narrower one: a session owner's child stays within the owner's own
// limits, where an approval dispatches as the fixed client. Otherwise a
// holding approval is chosen when the template admits approved work.
// Otherwise there is no basis, with the reasons joined by "; ", and Wait when
// the standing basis is unproven and the template admits session work.
func Choose(d Decision, admits []Basis) Choice {
	admitted := func(b Basis) bool { return b != BasisNone && slices.Contains(admits, b) }
	if admitted(d.Standing) {
		c := Choice{Basis: d.Standing}
		if d.Standing == BasisSession && d.Owner != nil {
			ref := *d.Owner
			c.Owner = &ref
		}
		return c
	}
	if d.Approved && admitted(BasisApproved) {
		return Choice{Basis: BasisApproved}
	}
	var reasons []string
	wait := false
	switch {
	case d.Standing != BasisNone:
		reasons = append(reasons, fmt.Sprintf("the template does not admit %s work", d.Standing))
	case d.Wait && !admitted(BasisSession):
		reasons = append(reasons, "the template does not admit session work")
	case d.Wait:
		wait = true
		reasons = append(reasons, d.StandingReason)
	case d.StandingReason != "":
		reasons = append(reasons, d.StandingReason)
	}
	switch {
	case d.Approved:
		reasons = append(reasons, "the template does not admit approved work")
	case d.ApprovalReason != "":
		reasons = append(reasons, d.ApprovalReason)
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "no basis")
	}
	return Choice{Wait: wait, Reason: strings.Join(reasons, "; ")}
}
