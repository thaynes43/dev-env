package coordretention

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/taskclaim"
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
	// ReplyAck reports that a stored parent-note reply is the ack. The note is
	// marked without an InboxReadAck. Meatball writes no ack object.
	ReplyAck bool
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
	replies := indexParentReplies(summaries, c.OperatorNamespace)
	out := make([]Result, 0, len(summaries))
	for _, s := range summaries {
		d, pending, observations, replyAck := decide(ctx, c, s, memo, replies)
		if ctx.Err() != nil {
			d, pending, observations, replyAck = Unknown, nil, nil, false
		}
		out = append(out, Result{Summary: s, Decision: d, RequiredMessage: pending, Observations: observations, ReplyAck: replyAck})
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
		return validInboxSender(s) && validInboxRecipient(s)
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
func decide(ctx context.Context, c EvalConfig, s Summary, memo map[string]memoSession, replies map[noteKey][]Summary) (Decision, *MessageRef, []protocol.CoordArchiveObservation, bool) {
	if s.Kind == InboxMessage && ctx.Err() == nil && c.Reader != nil && validSummary(s, c.OperatorNamespace) {
		return decideInbox(ctx, c, s, memo, replies)
	}
	d, pending, observations := decideRecord(ctx, c, s, memo)
	return d, pending, observations, false
}

func decideRecord(ctx context.Context, c EvalConfig, s Summary, memo map[string]memoSession) (Decision, *MessageRef, []protocol.CoordArchiveObservation) {
	if ctx.Err() != nil || c.Reader == nil || !validSummary(s, c.OperatorNamespace) {
		return Unknown, nil, nil
	}
	switch s.Kind {
	case TaskClaim:
		return sessionDecision(ctx, c, "owner", s.Owner, memo)
	case PublishedContext:
		return sessionDecision(ctx, c, "source", s.Source, memo)
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
func validInboxSender(s Summary) bool {
	if s.CallerSenderUID != "" {
		return s.Sender == (SessionRef{}) && protocol.ValidKubeUID(s.CallerSenderUID)
	}
	return validRef(s.Sender)
}

func validInboxRecipient(s Summary) bool {
	if s.CallerRecipient {
		return s.Recipient.Namespace == "" && s.Recipient.Name == "" && protocol.ValidKubeUID(s.Recipient.UID)
	}
	return validRef(s.Recipient)
}

type noteKey struct{ project, name, uid string }

func indexParentReplies(summaries []Summary, namespace string) map[noteKey][]Summary {
	out := map[noteKey][]Summary{}
	for _, s := range summaries {
		if s.Kind != InboxMessage || s.MessageKind != inbox.KindReply || s.ReplyToName == "" || s.ReplyToUID == "" {
			continue
		}
		if !s.ParentNote && s.CallerSenderUID == "" {
			continue
		}
		if !validSummary(s, namespace) {
			continue
		}
		key := noteKey{s.Project, s.ReplyToName, s.ReplyToUID}
		out[key] = append(out[key], s)
	}
	return out
}

func noteUsesReplyAck(s Summary) bool {
	switch s.MessageKind {
	case inbox.KindQuestion, inbox.KindResult, inbox.KindBlocker:
	default:
		return false
	}
	// A caller sender's child is the recipient. That session keeps the record
	// until it is gone. Reply-as-ack is for a child-authored note whose parent
	// is still live, and for a caller recipient once the child sender is gone.
	if s.CallerSenderUID != "" {
		return false
	}
	return s.ParentNote || s.CallerRecipient
}

// callerPartyObservation records a Meatball party in the only shape the
// archive event accepts. The account is not a session. The namespace and
// name are the service account; the UID is the CallerPolicy UID.
func callerPartyObservation(role, policyUID, observed, ackUID string) (protocol.CoordArchiveObservation, bool) {
	namespace, name, ok := strings.Cut(coord.MeatballServiceAccount, "/")
	if !ok || !protocol.ValidKubeUID(policyUID) {
		return protocol.CoordArchiveObservation{}, false
	}
	return protocol.CoordArchiveObservation{Role: role, Namespace: namespace, Name: name, UID: policyUID, Observed: observed, AckUID: ackUID}, true
}

func decideInbox(ctx context.Context, c EvalConfig, s Summary, memo map[string]memoSession, replies map[noteKey][]Summary) (Decision, *MessageRef, []protocol.CoordArchiveObservation, bool) {
	senderAnswer, senderObs, ok := inboxSender(ctx, c, s, memo)
	if !ok {
		return Unknown, nil, nil, false
	}
	if senderAnswer == sessionPresent {
		return Kept, nil, []protocol.CoordArchiveObservation{senderObs}, false
	}
	if !s.CallerRecipient {
		decision, observations, done := inboxAck(ctx, c, s, senderObs)
		if done {
			return decision, nil, observations, false
		}
	}
	if noteUsesReplyAck(s) {
		replyUID, replyErr := provenReply(ctx, c, s, replies)
		if replyUID != "" {
			obs, ok := replyAckObservation(s, replyUID)
			if !ok {
				return Unknown, nil, nil, false
			}
			return Released, nil, []protocol.CoordArchiveObservation{senderObs, obs}, true
		}
		if replyErr != nil && !s.CallerRecipient {
			recipientAnswer, _, recipientOK := inboxRecipient(ctx, c, s, memo)
			if recipientOK && recipientAnswer == sessionAbsent {
				obs, obsOK := sessionObservation("recipient", s.Recipient, memo)
				if !obsOK {
					return Unknown, nil, nil, false
				}
				return Released, nil, []protocol.CoordArchiveObservation{senderObs, obs}, false
			}
			return Unknown, nil, nil, false
		}
	}
	if s.CallerRecipient {
		obs, ok := callerPartyObservation("recipient", s.Recipient.UID, "not-found", "")
		if !ok {
			return Unknown, nil, nil, false
		}
		return Released, nil, []protocol.CoordArchiveObservation{senderObs, obs}, false
	}
	recipientAnswer, recipientObs, ok := inboxRecipient(ctx, c, s, memo)
	if !ok {
		return Unknown, nil, nil, false
	}
	decision := Released
	if recipientAnswer == sessionPresent {
		decision = Kept
	}
	return decision, nil, []protocol.CoordArchiveObservation{senderObs, recipientObs}, false
}

func inboxSender(ctx context.Context, c EvalConfig, s Summary, memo map[string]memoSession) (sessionAnswer, protocol.CoordArchiveObservation, bool) {
	if s.CallerSenderUID != "" {
		obs, ok := callerPartyObservation("sender", s.CallerSenderUID, "not-found", "")
		return sessionAbsent, obs, ok
	}
	answer := session(ctx, c, s.Sender, memo)
	obs, ok := sessionObservation("sender", s.Sender, memo)
	return answer, obs, ok
}

func inboxRecipient(ctx context.Context, c EvalConfig, s Summary, memo map[string]memoSession) (sessionAnswer, protocol.CoordArchiveObservation, bool) {
	answer := session(ctx, c, s.Recipient, memo)
	obs, ok := sessionObservation("recipient", s.Recipient, memo)
	return answer, obs, ok
}

// inboxAck is the InboxReadAck lookup. done is false only when the ack is
// absent, so the caller can apply the recipient test. Every other result is final.
func inboxAck(ctx context.Context, c EvalConfig, s Summary, senderObs protocol.CoordArchiveObservation) (Decision, []protocol.CoordArchiveObservation, bool) {
	var a coord.InboxReadAck
	key := client.ObjectKey{Namespace: s.Namespace, Name: inbox.AckName(s.UID, s.Recipient.UID)}
	err := c.Reader.Get(ctx, key, &a)
	if ctx.Err() != nil {
		return Unknown, nil, true
	}
	if err == nil {
		if !matchesObject(&a, key) || a.ResourceVersion == "" || !inbox.ValidAck(a.Spec) || a.Spec.Project != s.Project || a.Spec.MessageName != s.Name || a.Spec.MessageUID != s.UID || a.Spec.Recipient != (coord.SessionIdentity{Namespace: s.Recipient.Namespace, Name: s.Recipient.Name, UID: s.Recipient.UID}) {
			return Unknown, nil, true
		}
		return Released, []protocol.CoordArchiveObservation{senderObs, {Role: "recipient", Namespace: s.Recipient.Namespace, Name: s.Recipient.Name, UID: s.Recipient.UID, Observed: "acknowledged", AckUID: string(a.UID)}}, true
	}
	if !apierrors.IsNotFound(err) {
		return Unknown, nil, true
	}
	return "", nil, false
}

func replyAckObservation(s Summary, replyUID string) (protocol.CoordArchiveObservation, bool) {
	if !protocol.ValidKubeUID(replyUID) {
		return protocol.CoordArchiveObservation{}, false
	}
	if s.CallerRecipient {
		return callerPartyObservation("recipient", s.Recipient.UID, "acknowledged", replyUID)
	}
	return protocol.CoordArchiveObservation{Role: "recipient", Namespace: s.Recipient.Namespace, Name: s.Recipient.Name, UID: s.Recipient.UID, Observed: "acknowledged", AckUID: replyUID}, true
}

func provenReply(ctx context.Context, c EvalConfig, note Summary, replies map[noteKey][]Summary) (string, error) {
	// A nil index has not seen the pass's list. The delete recheck has one
	// record, so it reads replies itself. An empty index already did.
	if replies == nil {
		return listedReply(ctx, c, note)
	}
	var lastErr error
	for _, candidate := range replies[noteKey{note.Project, note.Name, note.UID}] {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var msg coord.InboxMessage
		key := client.ObjectKey{Namespace: candidate.Namespace, Name: candidate.Name}
		err := c.Reader.Get(ctx, key, &msg)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			lastErr = err
			continue
		}
		if !matchesObject(&msg, key) || string(msg.UID) != candidate.UID || !replyBinds(note, msg) {
			continue
		}
		return string(msg.UID), nil
	}
	return "", lastErr
}

func listedReply(ctx context.Context, c EvalConfig, note Summary) (string, error) {
	// Kind and project only. replyBinds then keeps a labelled parent reply or
	// a caller-sender reply, which is the same set the pass index keeps. A
	// parent-note label on this list would miss a caller reply the pass had
	// already counted as the ack.
	selector := client.MatchingLabels{
		inbox.KindLabel:        inbox.KindReply,
		taskclaim.ProjectLabel: note.Project,
	}
	cursor, seen := "", map[string]struct{}{}
	found := ""
	for page := 0; page < 128; page++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var list coord.InboxMessageList
		err := c.Reader.List(ctx, &list, client.InNamespace(c.OperatorNamespace), selector, client.Limit(50), client.Continue(cursor))
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil {
			return "", err
		}
		if len(list.Items) > 50 {
			return "", fmt.Errorf("parent reply list page exceeds its bound")
		}
		for i := range list.Items {
			msg := &list.Items[i]
			if msg.Namespace != c.OperatorNamespace || !protocol.ValidKubeUID(string(msg.UID)) || !replyBinds(note, *msg) {
				continue
			}
			if found == "" {
				found = string(msg.UID)
			}
		}
		if list.Continue == "" {
			return found, nil
		}
		if _, ok := seen[list.Continue]; ok || list.Continue == cursor {
			return "", fmt.Errorf("parent reply list repeated its cursor")
		}
		seen[list.Continue] = struct{}{}
		cursor = list.Continue
	}
	return "", fmt.Errorf("parent reply list did not end")
}

func replyBinds(note Summary, reply coord.InboxMessage) bool {
	if reply.Spec.Kind != inbox.KindReply || reply.Spec.Project != note.Project || reply.Spec.InReplyTo == nil {
		return false
	}
	if reply.Spec.InReplyTo.Name != note.Name || reply.Spec.InReplyTo.UID != note.UID {
		return false
	}
	parentReply := reply.Labels[inbox.ParentNoteLabel] == "true" || (reply.Spec.CallerSender != nil && reply.Spec.Sender == (coord.SessionIdentity{}))
	if !parentReply {
		return false
	}
	child := coord.SessionIdentity{Namespace: note.Sender.Namespace, Name: note.Sender.Name, UID: note.Sender.UID}
	switch {
	case note.CallerRecipient:
		return reply.Spec.CallerSender != nil && reply.Spec.Sender == (coord.SessionIdentity{}) && reply.Spec.CallerRecipient == nil &&
			reply.Spec.CallerSender.PolicyUID == note.Recipient.UID && reply.Spec.Recipient == child
	case note.CallerSenderUID != "":
		parent := coord.SessionIdentity{Namespace: note.Recipient.Namespace, Name: note.Recipient.Name, UID: note.Recipient.UID}
		return reply.Spec.CallerSender == nil && reply.Spec.CallerRecipient != nil && reply.Spec.Recipient == (coord.SessionIdentity{}) &&
			reply.Spec.Sender == parent && reply.Spec.CallerRecipient.PolicyUID == note.CallerSenderUID
	default:
		parent := coord.SessionIdentity{Namespace: note.Recipient.Namespace, Name: note.Recipient.Name, UID: note.Recipient.UID}
		return reply.Spec.CallerSender == nil && reply.Spec.CallerRecipient == nil && reply.Spec.Sender == parent && reply.Spec.Recipient == child
	}
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
