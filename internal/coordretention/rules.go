package coordretention

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
	// ReplyAck reports that a stored reply is the ack. Reply is that exact
	// message. A note released this way is not deleted unless Reply is archived
	// and verified first. Meatball writes no InboxReadAck.
	ReplyAck bool
	Reply    *Summary
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
	replies := indexParentReplies(ctx, c, summaries)
	out := make([]Result, 0, len(summaries))
	for _, s := range summaries {
		d, pending, observations, replyAck, reply := decide(ctx, c, s, memo, replies)
		if ctx.Err() != nil {
			d, pending, observations, replyAck, reply = Unknown, nil, nil, false, nil
		}
		out = append(out, Result{Summary: s, Decision: d, RequiredMessage: pending, Observations: observations, ReplyAck: replyAck, Reply: reply})
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
func decide(ctx context.Context, c EvalConfig, s Summary, memo map[string]memoSession, replies *replyIndex) (Decision, *MessageRef, []protocol.CoordArchiveObservation, bool, *Summary) {
	if s.Kind == InboxMessage && ctx.Err() == nil && c.Reader != nil && validSummary(s, c.OperatorNamespace) {
		return decideInbox(ctx, c, s, memo, replies)
	}
	d, pending, observations := decideRecord(ctx, c, s, memo)
	return d, pending, observations, false, nil
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

// replyIndex is the pass's reply list. A nil index means the delete recheck
// lists replies itself. err records a read that failed for one note; that note
// stays Unknown rather than releasing without its reply.
type replyIndex struct {
	byNote map[noteKey][]Summary
	err    map[noteKey]error
}

func indexParentReplies(ctx context.Context, c EvalConfig, summaries []Summary) *replyIndex {
	idx := &replyIndex{byNote: map[noteKey][]Summary{}, err: map[noteKey]error{}}
	if c.Reader == nil {
		return idx
	}
	notes := map[noteKey]Summary{}
	var replies []Summary
	for _, s := range summaries {
		if s.Kind != InboxMessage {
			continue
		}
		notes[noteKey{s.Project, s.Name, s.UID}] = s
		if s.MessageKind == inbox.KindReply && s.ReplyToName != "" && s.ReplyToUID != "" {
			replies = append(replies, s)
		}
	}
	for _, s := range replies {
		if err := ctx.Err(); err != nil {
			return idx
		}
		key := noteKey{s.Project, s.ReplyToName, s.ReplyToUID}
		note, ok := notes[key]
		if !ok {
			continue
		}
		var msg coord.InboxMessage
		objectKey := client.ObjectKey{Namespace: s.Namespace, Name: s.Name}
		err := c.Reader.Get(ctx, objectKey, &msg)
		if ctx.Err() != nil {
			return idx
		}
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			idx.err[key] = err
			continue
		}
		if !matchesObject(&msg, objectKey) || string(msg.UID) != s.UID || !validReply(note, &msg, c.OperatorNamespace) {
			continue
		}
		fresh, ok := summarizeInbox(&msg, c.OperatorNamespace)
		if !ok {
			continue
		}
		idx.byNote[key] = append(idx.byNote[key], fresh)
	}
	return idx
}

func noteUsesReplyAck(s Summary) bool {
	switch s.MessageKind {
	case inbox.KindQuestion, inbox.KindResult, inbox.KindBlocker:
	default:
		return false
	}
	// A caller sender's child is the recipient. That session keeps the record
	// until it is gone. A reply counts as the ack for a caller recipient, and
	// for a session parent only once that parent session is gone.
	if s.CallerSenderUID != "" {
		return false
	}
	return s.ParentNote || s.CallerRecipient
}

// inboxShapeDecided reports the endpoint pairs retention may release or keep.
// Every other pair, including caller to caller, is Unknown.
func inboxShapeDecided(s Summary) bool {
	sessionSender := s.CallerSenderUID == "" && s.Sender != (SessionRef{})
	sessionRecipient := !s.CallerRecipient && s.Recipient != (SessionRef{})
	callerSender := s.CallerSenderUID != "" && s.Sender == (SessionRef{})
	callerRecipient := s.CallerRecipient && s.Recipient.Namespace == "" && s.Recipient.Name == "" && s.Recipient.UID != ""
	switch {
	case sessionSender && sessionRecipient && !callerSender && !callerRecipient:
		return true
	case sessionSender && callerRecipient && !callerSender:
		switch s.MessageKind {
		case inbox.KindQuestion, inbox.KindResult, inbox.KindBlocker:
			return true
		default:
			return false
		}
	case callerSender && sessionRecipient && !callerRecipient:
		return s.MessageKind == inbox.KindReply || s.MessageKind == ""
	default:
		return false
	}
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

func decideInbox(ctx context.Context, c EvalConfig, s Summary, memo map[string]memoSession, replies *replyIndex) (Decision, *MessageRef, []protocol.CoordArchiveObservation, bool, *Summary) {
	if !inboxShapeDecided(s) {
		return Unknown, nil, nil, false, nil
	}
	senderAnswer, senderObs, ok := inboxSender(ctx, c, s, memo)
	if !ok {
		return Unknown, nil, nil, false, nil
	}
	if senderAnswer == sessionPresent {
		return Kept, nil, []protocol.CoordArchiveObservation{senderObs}, false, nil
	}
	// A caller-authored record is released only when its child session is gone.
	// A read ack does not shortcut that.
	if s.CallerSenderUID != "" {
		recipientAnswer, recipientObs, ok := inboxRecipient(ctx, c, s, memo)
		if !ok {
			return Unknown, nil, nil, false, nil
		}
		decision := Released
		if recipientAnswer == sessionPresent {
			decision = Kept
		}
		return decision, nil, []protocol.CoordArchiveObservation{senderObs, recipientObs}, false, nil
	}
	if !s.CallerRecipient {
		decision, observations, done := inboxAck(ctx, c, s, senderObs)
		if done {
			return decision, nil, observations, false, nil
		}
	}
	if noteUsesReplyAck(s) && !s.CallerRecipient {
		// The reply counts as the ack only once the parent session is gone.
		// While that session exists the note is kept, answered or not.
		recipientAnswer, recipientObs, ok := inboxRecipient(ctx, c, s, memo)
		if !ok {
			return Unknown, nil, nil, false, nil
		}
		if recipientAnswer == sessionPresent {
			return Kept, nil, []protocol.CoordArchiveObservation{senderObs, recipientObs}, false, nil
		}
		reply, err := provenReply(ctx, c, s, replies)
		if reply != nil {
			obs, obsOK := replyAckObservation(s, reply.UID)
			if !obsOK {
				return Unknown, nil, nil, false, nil
			}
			return Released, nil, []protocol.CoordArchiveObservation{senderObs, obs}, true, reply
		}
		if err != nil {
			return Unknown, nil, nil, false, nil
		}
		return Released, nil, []protocol.CoordArchiveObservation{senderObs, recipientObs}, false, nil
	}
	if s.CallerRecipient {
		reply, err := provenReply(ctx, c, s, replies)
		if reply != nil {
			obs, obsOK := replyAckObservation(s, reply.UID)
			if !obsOK {
				return Unknown, nil, nil, false, nil
			}
			return Released, nil, []protocol.CoordArchiveObservation{senderObs, obs}, true, reply
		}
		if err != nil {
			return Unknown, nil, nil, false, nil
		}
		obs, obsOK := callerPartyObservation("recipient", s.Recipient.UID, "not-found", "")
		if !obsOK {
			return Unknown, nil, nil, false, nil
		}
		return Released, nil, []protocol.CoordArchiveObservation{senderObs, obs}, false, nil
	}
	recipientAnswer, recipientObs, ok := inboxRecipient(ctx, c, s, memo)
	if !ok {
		return Unknown, nil, nil, false, nil
	}
	decision := Released
	if recipientAnswer == sessionPresent {
		decision = Kept
	}
	return decision, nil, []protocol.CoordArchiveObservation{senderObs, recipientObs}, false, nil
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

func provenReply(ctx context.Context, c EvalConfig, note Summary, replies *replyIndex) (*Summary, error) {
	// A nil index has not seen the pass's list. The delete recheck has one
	// record, so it reads replies itself. An empty index already did.
	if replies == nil {
		return listedReply(ctx, c, note)
	}
	key := noteKey{note.Project, note.Name, note.UID}
	if err := replies.err[key]; err != nil && len(replies.byNote[key]) == 0 {
		return nil, err
	}
	var lastErr error
	for _, candidate := range replies.byNote[key] {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var msg coord.InboxMessage
		objectKey := client.ObjectKey{Namespace: candidate.Namespace, Name: candidate.Name}
		err := c.Reader.Get(ctx, objectKey, &msg)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			lastErr = err
			continue
		}
		// The fresh read uses the same function as the pass index and the recheck list.
		if !matchesObject(&msg, objectKey) || string(msg.UID) != candidate.UID || !validReply(note, &msg, c.OperatorNamespace) {
			continue
		}
		fresh, ok := summarizeInbox(&msg, c.OperatorNamespace)
		if !ok {
			continue
		}
		return &fresh, nil
	}
	return nil, lastErr
}

func listedReply(ctx context.Context, c EvalConfig, note Summary) (*Summary, error) {
	// Kind and project select the page. validReply then applies the same
	// label, message, name and resourceVersion rules as the pass index.
	// A parent-note label on this list would miss a caller reply.
	selector := client.MatchingLabels{
		inbox.KindLabel:        inbox.KindReply,
		taskclaim.ProjectLabel: note.Project,
	}
	cursor, seen := "", map[string]struct{}{}
	var found *Summary
	for page := 0; page < 128; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var list coord.InboxMessageList
		err := c.Reader.List(ctx, &list, client.InNamespace(c.OperatorNamespace), selector, client.Limit(50), client.Continue(cursor))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, err
		}
		if len(list.Items) > 50 {
			return nil, fmt.Errorf("parent reply list page exceeds its bound")
		}
		for i := range list.Items {
			msg := &list.Items[i]
			if !validReply(note, msg, c.OperatorNamespace) {
				continue
			}
			if found == nil {
				fresh, ok := summarizeInbox(msg, c.OperatorNamespace)
				if !ok {
					continue
				}
				found = &fresh
			}
		}
		if list.Continue == "" {
			return found, nil
		}
		if _, ok := seen[list.Continue]; ok || list.Continue == cursor {
			return nil, fmt.Errorf("parent reply list repeated its cursor")
		}
		seen[list.Continue] = struct{}{}
		cursor = list.Continue
	}
	return nil, fmt.Errorf("parent reply list did not end")
}

// validReply is the only acceptance check for a stored reply. The pass index,
// the fresh read of an indexed candidate, and the recheck list all call it:
// the same labels, inbox.ValidMessage, the name derived from the sender and
// the key, and the resourceVersion check inside validSummary.
func validReply(note Summary, msg *coord.InboxMessage, namespace string) bool {
	if msg == nil || msg.Namespace != namespace {
		return false
	}
	labels := msg.GetLabels()
	callerSender := msg.Spec.CallerSender != nil && msg.Spec.Sender == (coord.SessionIdentity{})
	if labels[inbox.KindLabel] != inbox.KindReply || labels[taskclaim.ProjectLabel] != msg.Spec.Project {
		return false
	}
	if labels[inbox.ParentNoteLabel] != "true" && !callerSender {
		return false
	}
	if !inbox.ValidMessage(msg.Spec) || msg.GetName() != inbox.MessageNameFor(msg.Spec) {
		return false
	}
	fresh, ok := summarizeInbox(msg, namespace)
	if !ok || !validSummary(fresh, namespace) {
		return false
	}
	if fresh.MessageKind != inbox.KindReply || fresh.ReplyToName == "" || fresh.ReplyToUID == "" {
		return false
	}
	if !fresh.ParentNote && fresh.CallerSenderUID == "" {
		return false
	}
	return replyBinds(note, *msg)
}

func summarizeInbox(msg *coord.InboxMessage, namespace string) (Summary, bool) {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(msg)
	if err != nil {
		return Summary{}, false
	}
	u := &unstructured.Unstructured{Object: raw}
	u.SetGroupVersionKind(kindResource(InboxMessage).gvk)
	return summarize(kindResource(InboxMessage), namespace, u), true
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
