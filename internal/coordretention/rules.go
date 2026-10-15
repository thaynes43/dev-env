package coordretention

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/inbox"
)

// Decision is eligibility only. Released never authorizes a mutation.
type Decision string

const (
	Released Decision = "Released"
	Kept     Decision = "Kept"
	Unknown  Decision = "Unknown"
)

// ArchiveEvidence is supplied by a later bulk lookup, never a per-record exec.
// Missing or Known=false evidence is pending only after live UID absence is proved.
type ArchiveEvidence struct{ Known, Holds bool }
type EvalConfig struct {
	Reader               client.Reader // the operator's uncached APIReader
	OperatorNamespace    string
	ReconciledNamespaces map[string]bool
	Archive              map[MessageRef]ArchiveEvidence
}
type Result struct {
	Summary         Summary
	Decision        Decision
	RequiredMessage *MessageRef // non-nil only after scoped live UID absence
	// Observations are the reads this pass already performed. They confer no mark or delete authority.
	Observations []protocol.CoordArchiveObservation
}
type sessionAnswer uint8

const (
	sessionUnknown sessionAnswer = iota
	sessionAbsent
	sessionPresent
)

type memoSession struct {
	answer sessionAnswer
	uid    string
}

// Evaluate owns one pass's namespace/name memo. Future delete rechecks must
// perform fresh uncached reads, never reuse these eligibility observations.
func Evaluate(ctx context.Context, c EvalConfig, summaries []Summary) []Result {
	memo := map[string]memoSession{}
	out := make([]Result, 0, len(summaries))
	for _, s := range summaries {
		d, pending, observations := decide(ctx, c, s, memo)
		if ctx.Err() != nil {
			d, pending, observations = Unknown, nil, nil
		}
		out = append(out, Result{Summary: s, Decision: d, RequiredMessage: pending, Observations: observations})
	}
	return out
}

func validRef(r SessionRef) bool {
	return inbox.ValidSession(coord.SessionIdentity{Namespace: r.Namespace, Name: r.Name, UID: r.UID})
}
func validSummary(s Summary, namespace string) bool {
	if !s.Valid || !validMeta(s, namespace) {
		return false
	}
	switch s.Kind {
	case TaskClaim:
		return validRef(s.Owner)
	case PublishedContext:
		return validRef(s.Source)
	case InboxMessage:
		return validRef(s.Sender) && validRef(s.Recipient)
	case InboxReadAck:
		if !validRef(s.Recipient) || s.Message.Namespace != s.Namespace || s.Message.Project != s.Project || s.Name != inbox.AckName(s.Message.UID, s.Recipient.UID) {
			return false
		}
		_, err := (protocol.CoordArchiveIdentity{Resource: "inboxmessages", Project: s.Message.Project, Name: s.Message.Name, UID: s.Message.UID}).RecordDir()
		return err == nil
	}
	return false
}
func matchesObject(o client.Object, key client.ObjectKey) bool {
	return o.GetNamespace() == key.Namespace && o.GetName() == key.Name && protocol.ValidKubeUID(string(o.GetUID()))
}
func session(ctx context.Context, c EvalConfig, r SessionRef, memo map[string]memoSession) sessionAnswer {
	if ctx.Err() != nil || c.Reader == nil || !c.ReconciledNamespaces[r.Namespace] || !validRef(r) {
		return sessionUnknown
	}
	key := r.Namespace + "\x00" + r.Name
	if a, ok := memo[key]; ok {
		if a.answer == sessionPresent && a.uid != r.UID {
			return sessionAbsent
		}
		return a.answer
	}
	var got devv1.AgentSession
	objectKey := client.ObjectKey{Namespace: r.Namespace, Name: r.Name}
	err := c.Reader.Get(ctx, objectKey, &got)
	if ctx.Err() != nil {
		return sessionUnknown
	}
	if apierrors.IsNotFound(err) {
		memo[key] = memoSession{answer: sessionAbsent}
		return sessionAbsent
	}
	if err != nil || !matchesObject(&got, objectKey) {
		memo[key] = memoSession{answer: sessionUnknown}
		return sessionUnknown
	}
	memo[key] = memoSession{answer: sessionPresent, uid: string(got.UID)}
	if string(got.UID) != r.UID {
		return sessionAbsent
	}
	return sessionPresent
}
func sessionObservation(role string, ref SessionRef, memo map[string]memoSession) (protocol.CoordArchiveObservation, bool) {
	a, ok := memo[ref.Namespace+"\x00"+ref.Name]
	if !ok || a.answer == sessionUnknown {
		return protocol.CoordArchiveObservation{}, false
	}
	obs := protocol.CoordArchiveObservation{Role: role, Namespace: ref.Namespace, Name: ref.Name, UID: ref.UID}
	switch a.answer {
	case sessionPresent:
		if a.uid == ref.UID {
			obs.Observed = "present"
		} else {
			obs.Observed = "uid-replaced"
		}
	case sessionAbsent:
		obs.Observed = "not-found"
	default:
		return protocol.CoordArchiveObservation{}, false
	}
	return obs, true
}
func absentFor(ctx context.Context, c EvalConfig, r SessionRef, memo map[string]memoSession) Decision {
	switch session(ctx, c, r, memo) {
	case sessionAbsent:
		return Released
	case sessionPresent:
		return Kept
	default:
		return Unknown
	}
}
func decide(ctx context.Context, c EvalConfig, s Summary, memo map[string]memoSession) (Decision, *MessageRef, []protocol.CoordArchiveObservation) {
	if ctx.Err() != nil || c.Reader == nil || !validSummary(s, c.OperatorNamespace) {
		return Unknown, nil, nil
	}
	switch s.Kind {
	case TaskClaim:
		return sessionDecision(ctx, c, "owner", s.Owner, memo)
	case PublishedContext:
		return sessionDecision(ctx, c, "source", s.Source, memo)
	case InboxMessage:
		answer := session(ctx, c, s.Sender, memo)
		senderObs, ok := sessionObservation("sender", s.Sender, memo)
		if !ok {
			return Unknown, nil, nil
		}
		if answer == sessionPresent {
			return Kept, nil, []protocol.CoordArchiveObservation{senderObs}
		}
		var a coord.InboxReadAck
		key := client.ObjectKey{Namespace: s.Namespace, Name: inbox.AckName(s.UID, s.Recipient.UID)}
		err := c.Reader.Get(ctx, key, &a)
		if ctx.Err() != nil {
			return Unknown, nil, nil
		}
		if err == nil {
			if !matchesObject(&a, key) || a.ResourceVersion == "" || !inbox.ValidAck(a.Spec) || a.Spec.Project != s.Project || a.Spec.MessageName != s.Name || a.Spec.MessageUID != s.UID || a.Spec.Recipient != (coord.SessionIdentity{Namespace: s.Recipient.Namespace, Name: s.Recipient.Name, UID: s.Recipient.UID}) {
				return Unknown, nil, nil
			}
			return Released, nil, []protocol.CoordArchiveObservation{senderObs, {Role: "recipient", Namespace: s.Recipient.Namespace, Name: s.Recipient.Name, UID: s.Recipient.UID, Observed: "acknowledged", AckUID: string(a.UID)}}
		}
		if !apierrors.IsNotFound(err) {
			return Unknown, nil, nil
		}
		recipientAnswer := session(ctx, c, s.Recipient, memo)
		recipientObs, ok := sessionObservation("recipient", s.Recipient, memo)
		if !ok {
			return Unknown, nil, nil
		}
		decision := Released
		if recipientAnswer == sessionPresent {
			decision = Kept
		}
		return decision, nil, []protocol.CoordArchiveObservation{senderObs, recipientObs}
	case InboxReadAck:
		var m coord.InboxMessage
		key := client.ObjectKey{Namespace: s.Message.Namespace, Name: s.Message.Name}
		err := c.Reader.Get(ctx, key, &m)
		if ctx.Err() != nil {
			return Unknown, nil, nil
		}
		if err == nil {
			if !matchesObject(&m, key) {
				return Unknown, nil, nil
			}
			if string(m.UID) == s.Message.UID {
				return Kept, nil, []protocol.CoordArchiveObservation{{Role: "message", Namespace: s.Message.Namespace, Name: s.Message.Name, UID: s.Message.UID, Observed: "present"}}
			}
		} else if !apierrors.IsNotFound(err) {
			return Unknown, nil, nil
		}
		// Only this branch has proved the original live message UID absent.
		evidence, ok := c.Archive[s.Message]
		if !ok || !evidence.Known {
			needed := s.Message
			return Unknown, &needed, nil
		}
		if !evidence.Holds {
			return Kept, nil, nil
		}
		return Released, nil, []protocol.CoordArchiveObservation{{Role: "message", Namespace: s.Message.Namespace, Name: s.Message.Name, UID: s.Message.UID, Observed: "message-released"}}
	}
	return Unknown, nil, nil
}
func sessionDecision(ctx context.Context, c EvalConfig, role string, ref SessionRef, memo map[string]memoSession) (Decision, *MessageRef, []protocol.CoordArchiveObservation) {
	answer := session(ctx, c, ref, memo)
	obs, ok := sessionObservation(role, ref, memo)
	if !ok {
		return Unknown, nil, nil
	}
	if answer == sessionPresent {
		return Kept, nil, []protocol.CoordArchiveObservation{obs}
	}
	return Released, nil, []protocol.CoordArchiveObservation{obs}
}

func (s Summary) String() string { return fmt.Sprintf("%s/%s/%s", s.Kind, s.Project, s.Name) }
