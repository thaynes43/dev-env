package scheduledwork

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Dispatch is what the executor (S3b) asks the API's fixed-name creation entry
// (apiserver.Server.CreateScheduledSession) to create for one occurrence: the
// item it belongs to, the occurrence, the start deadline agentd enforces, and
// who the session is created as. The entry derives the session's name from
// ItemUID and At (SessionName) and sets the finalizer, label and annotations
// of DESIGN-054 in the one atomic Create.
type Dispatch struct {
	// ItemName and ItemUID are the ScheduledWork's name, in the operator's
	// namespace, and its UID.
	ItemName string
	ItemUID  types.UID
	// At is the occurrence instant.
	At time.Time
	// StartDeadline is the latest time the agent may first launch: the due
	// time plus grace, or retriedAt plus grace for a retried occurrence.
	StartDeadline time.Time
	// Owner is the live owner session, for the live session owner basis: the
	// session is created as its child, through the validation the owner's own
	// create would get. Nil creates it as the operator's fixed client identity
	// for scheduled work (the human owner and human approval bases).
	Owner *SessionRef
}

// SessionRef names one session by name and UID, so a session recreated under
// the same name is never taken for it.
type SessionRef struct {
	Name string
	UID  types.UID
}

// ErrCollision is the fixed-name entry's answer when a session of the derived
// name exists that this occurrence did not create: its scheduled-work label,
// its item-name annotation or its occurrence annotation differs. It is never
// adopted; the executor records the occurrence Ambiguous.
var ErrCollision = errors.New("a session of the derived name exists that this occurrence did not create")

// RefusedError is a definite refusal by the session creation checks: the
// request, the caller or a creation limit (a 4xx of the /v1 API). Nothing was
// created. The executor parks the occurrence with Reason. Any other error from
// the entry is a failed read or write, which the executor tries again while
// the occurrence is inside its grace.
type RefusedError struct {
	Reason string
}

func (e *RefusedError) Error() string { return "the session creation checks refused: " + e.Reason }

// SpecHash is the SHA-256, in hex, of every spec field except editor and
// cancelled (DESIGN-054 "Data contract"): the hash an occurrence records at
// its claim and a human approval binds to. Notify and byDay are sets, so
// their order does not change the hash.
func SpecHash(spec v1alpha1.ScheduledWorkSpec) string {
	s := *spec.DeepCopy()
	s.Editor, s.Cancelled = "", false
	slices.Sort(s.Notify)
	if s.Recurrence != nil {
		slices.Sort(s.Recurrence.ByDay)
	}
	// A spec of strings, numbers, booleans and times always marshals.
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// OccurrenceAnnotation is AnnotationScheduledWorkOccurrence's value for the
// occurrence at: its Unix seconds, decimal (OccurrenceAt reads it back).
func OccurrenceAnnotation(at time.Time) string {
	return strconv.FormatInt(at.Unix(), 10)
}

// StartDeadlineAnnotation is AnnotationScheduledWorkStartDeadline's value for
// the deadline t: RFC 3339 in UTC, ending in Z, to the second, as agentd's
// protocol.ParseStartDeadline reads it.
func StartDeadlineAnnotation(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// CreatedFor reports whether s carries the scheduled-work finalizer, label and
// annotations of the occurrence at of the item named itemName with UID itemUID,
// as the fixed-name entry sets them in its atomic Create. Only such a session
// of the derived name is adopted; any other is a collision. A session without
// the finalizer is not adopted even when its label and annotations match: the
// releaser acts only on finalizer holders, so its outcome would never be
// recorded and the occurrence would hold its slot for good.
func CreatedFor(s *v1alpha1.AgentSession, itemName string, itemUID types.UID, at time.Time) bool {
	return itemUID != "" && itemName != "" &&
		controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer) &&
		s.Labels[v1alpha1.LabelScheduledWork] == string(itemUID) &&
		s.Annotations[v1alpha1.AnnotationScheduledWorkName] == itemName &&
		s.Annotations[v1alpha1.AnnotationScheduledWorkOccurrence] == OccurrenceAnnotation(at)
}

// ReleaseMargin is how long after a session's start deadline the releaser
// keeps the scheduled-work finalizer, whatever the reason it may release: the
// stated bound on the clock skew between the operator and a node. While the
// finalizer holds the derived name, no second session of the occurrence can be
// created; once it can lift, any later create of that name comes after the
// deadline plus the margin, and agentd on a node whose clock is within the
// margin refuses its first launch (protocol.StartDeadlinePassed). So an
// occurrence gets at most one agent launch, given a skew under the margin.
const ReleaseMargin = 2 * time.Minute

// ReleaseHeldUntil is the earliest time the releaser may lift the
// scheduled-work finalizer from s: its start deadline plus ReleaseMargin. A
// missing or unreadable deadline is an error, and the finalizer stays.
func ReleaseHeldUntil(s *v1alpha1.AgentSession) (time.Time, error) {
	deadline, err := StartDeadline(s)
	if err != nil {
		return time.Time{}, err
	}
	return deadline.Add(ReleaseMargin), nil
}

// ErrPastStartDeadline is the fixed-name entry's answer when its clock is past
// the occurrence's start deadline just before the Create: such a session could
// not launch its agent, and a create that late could reuse a derived name the
// releaser has freed. Nothing was created. The executor records the occurrence
// Skipped, "not created before grace ended".
var ErrPastStartDeadline = errors.New("the start deadline has passed, so the session is not created")

// LaunchRefusal is DESIGN-054's launch gate, judged before the first pod of a
// session that carries the scheduled-work label: w is its ScheduledWork, read
// uncached by the session's name annotation, or nil when the item or its kind
// is gone. It refuses, with a reason, a session whose occurrence would never
// record it: the item is gone or was recreated under the name, it records no
// occurrence at the session's instant, the occurrence names another session or
// records another session object of this name (sessionUID: a second create,
// which can land only after the first session was recorded and deleted), the
// occurrence is already final without naming this one, or it carries the
// ending mark without naming this one (a create that landed after another
// leader decided to end it). A refused session never gets a pod, so its agent
// never launches unrecorded, and a session already Failed before its first pod
// stays refused: once its end is recorded its occurrence names it.
func LaunchRefusal(s *v1alpha1.AgentSession, w *v1alpha1.ScheduledWork) (string, bool) {
	at, err := OccurrenceAt(s)
	switch {
	case err != nil:
		return err.Error(), true
	case w == nil:
		return "its ScheduledWork is gone", true
	case string(w.UID) != s.Labels[v1alpha1.LabelScheduledWork]:
		return "its ScheduledWork was recreated under the same name", true
	}
	o := Find(w, at)
	switch {
	case o == nil:
		return "its ScheduledWork records no occurrence at " + at.Format(time.RFC3339), true
	case o.Session != "" && o.Session != s.Name:
		return "its occurrence names session " + o.Session, true
	case o.SessionUID != "" && o.SessionUID != string(s.UID):
		return "its occurrence records another session object of this name, UID " + o.SessionUID, true
	case v1alpha1.OccurrenceTerminal(o.State) && o.Session != s.Name:
		return "its occurrence is already " + o.State + " without it", true
	case o.Ending && o.Session != s.Name:
		return "its occurrence is ending without it", true
	case s.Status.Phase == v1alpha1.PhaseFailed:
		// Refused before (or invalid): the refusal stands, even once the
		// releaser has recorded the refused session's end into its
		// occurrence, which then names it.
		return "it already failed before its first pod", true
	}
	return "", false
}
