// Package summonoutcome owns the shared optimistic terminal transition for
// summoned reports and the operator leader's guaranteed close (DESIGN-064 6.6).
package summonoutcome

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	MaxNoteBytes = 1024
	Report       = "report"
	Unreported   = "unreported"
	FailedPrefix = "reported failed: "
)

// Terminal is shared with create-time idempotency and accounting readers.
func Terminal(o *v1alpha1.OutcomeStatus) bool {
	if o == nil {
		return false
	}
	return o.State == v1alpha1.OutcomeDone || o.State == v1alpha1.OutcomeFailed || o.State == v1alpha1.OutcomeEscalated
}

// Queued is the one definition of a Queued summoned session (DESIGN-064 5.9,
// "States of a summoned session"), shared by the operator leader's admission
// (internal/controller) and the API's queue counts (internal/apiserver), so
// the two cannot drift. A Queued session carries spec.summon, is not admitted
// (no status.summon.admittedAt, and granted is false: the create ledger's
// reserved key holds no grant for its name and UID), is not closed (no
// status.summon.closed and no terminal outcome), and is neither suspended
// nor being deleted. An admitted session, a granted one included, is not
// Queued; a closed one is neither. A suspended or deleting session that was
// never admitted is not Queued either: the leader does not admit it, and story
// 779 decides what that stop becomes.
func Queued(s *v1alpha1.AgentSession, granted bool) bool {
	if s == nil || s.Spec.Summon == nil || granted {
		return false
	}
	st := s.Status.Summon
	return (st == nil || st.AdmittedAt == nil && st.Closed == nil) && !Terminal(s.Status.Outcome) &&
		s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended && s.DeletionTimestamp.IsZero()
}

// Decision is selected before the write. Source and Reason are controlled words;
// the note is data and never goes into logs. Nonterminal observations have no source.
// Released frees the session's reservation in the same terminal write: the
// caller sets it only when no launch can follow (DESIGN-064 5.9). A report
// always releases at once, whatever Released says.
type Decision struct {
	State                v1alpha1.OutcomeState
	Note, Source, Reason string
	Released             bool
}

// Write uses the resource version of a live snapshot. On conflict the caller must
// read again: a terminal winner is final. Nothing logs or files for a losing write.
// Both the route and the leader use this function, including nonterminal states.
func Write(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, d Decision, now time.Time) (bool, error) {
	if s.Spec.Caller == "" || Terminal(s.Status.Outcome) {
		return false, nil
	}
	base := s.DeepCopy()
	at := metav1.NewTime(now.Truncate(time.Second))
	s.Status.Outcome = &v1alpha1.OutcomeStatus{State: d.State, Note: d.Note, At: &at}
	terminal := Terminal(s.Status.Outcome)
	filing := terminal && s.Spec.Lane == v1alpha1.LaneRemediation && d.State == v1alpha1.OutcomeEscalated
	if terminal {
		if s.Status.Summon == nil {
			s.Status.Summon = &v1alpha1.SummonStatus{}
		}
		// closed is the durable terminal record. A report releases the
		// reservation at once (5.9); an operator close releases here only when
		// its caller proved no launch can follow, and otherwise later, through
		// the leader's release step (internal/controller, OP777).
		released := d.Source == Report || d.Released
		s.Status.Summon.Closed = &v1alpha1.SummonClosedStatus{At: at, Source: d.Source, Reason: d.Reason, Released: released}
		// OP742: derive and add the terminal page event here, before this same CAS.
		if filing {
			// OP768 step 1: retain Pending here; the leader's filing worker will charge,
			// resolve an occupied name and create the child. Nothing creates a child now.
			s.Status.Summon.Escalation = &v1alpha1.SummonEscalationStatus{State: "Pending", Name: EscalationName(s)}
		}
	}
	patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	data, err := patch.Data(s)
	if err != nil {
		*s = *base
		return false, err
	}
	// Decode the answer into a separate object, so the guard below reads what
	// the API server stored and not what was submitted. The real client zeroes
	// its decode target first (controller-runtime's targetZeroingDecoder), so
	// there a pruned status.summon is seen either way; a client that does not
	// zero would hide it. The CAS patch is computed once, so the separate
	// response body does not change what is sent.
	stored := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace}}
	if err := c.Status().Patch(ctx, s, client.RawPatch(patch.Type(), data), client.WithSubResourceBody(stored)); err != nil {
		*s = *base
		return false, err
	}
	*s = *stored
	if terminal {
		if s.Status.Summon == nil || s.Status.Summon.Closed == nil {
			err := fmt.Errorf("terminal outcome patch did not retain status.summon.closed; install the current AgentSession CRD before the operator")
			log.FromContext(ctx).Error(err, "summoned terminal outcome is incomplete", "session", s.Name)
			return false, err
		}
		// The operator's general logger writes stderr. These bounded event lines
		// explicitly go to stdout, as DESIGN-064 requires. Never include note text.
		_, _ = fmt.Fprintf(os.Stdout, "ops-event: event=outcome session=%s caller=%s lane=%s state=%s source=%s reason=%s note_bytes=%d\n", s.Name, s.Spec.Caller, s.Spec.Lane, d.State, d.Source, d.Reason, len(d.Note))
		if filing {
			_, _ = fmt.Fprintf(os.Stdout, "ops-event: event=filing session=%s caller=%s lane=%s state=Pending name=%s\n", s.Name, s.Spec.Caller, s.Spec.Lane, s.Status.Summon.Escalation.Name)
		}
	}
	return true, nil
}

// EscalationName is the first candidate from 6.6. OP768 owns occupied-name
// handling (-2 through -99) before a child create; a Pending record creates none.
func EscalationName(s *v1alpha1.AgentSession) string {
	name := "esc-" + s.Name
	if len(name) <= 60 {
		return name
	}
	hash := sha256.Sum256([]byte(s.UID))
	return fmt.Sprintf("esc-rem-%x", hash[:4])
}
