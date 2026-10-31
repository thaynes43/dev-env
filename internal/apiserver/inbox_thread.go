package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

// Ask and reply on the durable inbox (DESIGN-014, 2026-10-31). A question and
// its reply are ordinary immutable InboxMessages with a kind and, for a reply,
// the exact question it answers. Their state is derived on each read.

// validInboxAddressing checks a request's shape before any lookup. A plain
// message names its recipient exactly, as before. A question names a recipient
// Session by name or asks for the recorded parent. A reply names its question
// only: the server derives the recipient from that question.
func validInboxAddressing(req apiv1.PublishInboxRequest) bool {
	r := req.Recipient
	switch req.Kind {
	case "":
		return !req.ToParent && req.InReplyTo == nil && r.Namespace != "" && r.UID != ""
	case inbox.KindQuestion:
		if req.InReplyTo != nil {
			return false
		}
		if req.ToParent {
			return r == apiv1.TaskClaimOwner{}
		}
		return len(validation.IsDNS1123Subdomain(r.Name)) == 0 &&
			(r.Namespace == "" || len(validation.IsDNS1123Label(r.Namespace)) == 0) &&
			(r.UID == "" || inbox.ValidUID(r.UID))
	case inbox.KindReply:
		return !req.ToParent && r == apiv1.TaskClaimOwner{} && req.InReplyTo != nil && inbox.ValidMessageName(req.InReplyTo.Name) &&
			(req.InReplyTo.UID == "" || inbox.ValidUID(req.InReplyTo.UID))
	}
	return false
}

// bindInboxRetry fills the server-derived fields of a retried question or
// reply from the stored record, but only where the request's own fields agree
// with it. The stored intent then decides 200 or 409 as for any message, even
// if the recipient, parent or question has since gone. A parent-addressed
// retry binds only a stored recipient that passes the parent fences.
func (s *Server) bindInboxRetry(ctx context.Context, spec *coord.InboxMessageSpec, stored coord.InboxMessageSpec, req apiv1.PublishInboxRequest, sender *coordSender) error {
	switch req.Kind {
	case inbox.KindQuestion:
		r := stored.Recipient
		if req.ToParent {
			if r.Namespace != s.Policy.SessionNamespace || r.Name != sender.parent || (sender.usageUID != "" && r.UID != sender.usageUID) {
				return nil
			}
			var sess v1alpha1.AgentSession
			err := s.Live.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: r.Name}, &sess)
			switch {
			case apierrors.IsNotFound(err):
			case err != nil:
				return inboxUnknown()
			case string(sess.UID) == r.UID && sess.CreationTimestamp.After(sender.created):
				return nil
			}
			spec.Recipient = r
			return nil
		}
		ns := req.Recipient.Namespace
		if ns == "" {
			ns = s.Policy.SessionNamespace
		}
		if r.Namespace == ns && r.Name == req.Recipient.Name && (req.Recipient.UID == "" || req.Recipient.UID == r.UID) {
			spec.Recipient = r
		}
	case inbox.KindReply:
		if q := stored.InReplyTo; q != nil && q.Name == req.InReplyTo.Name && (req.InReplyTo.UID == "" || req.InReplyTo.UID == q.UID) {
			ref := *q
			spec.InReplyTo, spec.Recipient = &ref, stored.Recipient
		}
	}
	return nil
}

// coordSender is what resolution needs from the authenticated sender Session.
type coordSender struct {
	parent   string
	created  time.Time
	usageUID string
}

func newCoordSender(s *v1alpha1.AgentSession) *coordSender {
	return &coordSender{parent: s.Spec.Parent, created: s.CreationTimestamp.Time, usageUID: s.Spec.UsageCaptureParentUID}
}

// resolveInboxQuestionRecipient finds a new question's recipient Session. The
// parent comes from the sender's immutable spec.parent, which the API set from
// the creating caller's token (D-05), never from message text. A parent
// created after the sender is a replacement, not the parent.
func (s *Server) resolveInboxQuestionRecipient(ctx context.Context, req apiv1.PublishInboxRequest, sender *coordSender) (coord.SessionIdentity, error) {
	name, uid := req.Recipient.Name, req.Recipient.UID
	if req.ToParent {
		if sender.parent == "" || strings.Contains(sender.parent, "/") || len(validation.IsDNS1123Subdomain(sender.parent)) != 0 {
			return coord.SessionIdentity{}, newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "this Session has no recorded parent Session; name the recipient Session instead")
		}
		name, uid = sender.parent, sender.usageUID
	} else if req.Recipient.Namespace != "" && req.Recipient.Namespace != s.Policy.SessionNamespace {
		return coord.SessionIdentity{}, inboxHidden()
	}
	var sess v1alpha1.AgentSession
	if s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, &sess) != nil || sess.UID == "" || (uid != "" && string(sess.UID) != uid) ||
		(req.ToParent && sess.CreationTimestamp.After(sender.created)) {
		return coord.SessionIdentity{}, inboxHidden()
	}
	return sessionIdentity(&sess), nil
}

// inboxQuestionFor checks that a new reply answers one live question in this
// project addressed to the replier, and that it has no other reply yet. Any
// other message reads as not found, so a reply reveals nothing about messages
// the replier did not receive.
func (s *Server) inboxQuestionFor(ctx context.Context, req apiv1.PublishInboxRequest, replier coord.SessionIdentity) (*coord.InboxMessage, error) {
	q, err := s.Inbox.Get(ctx, req.InReplyTo.Name)
	if errors.Is(err, inbox.ErrNotFound) {
		return nil, inboxHidden()
	}
	if err != nil {
		return nil, inboxUnknown()
	}
	if q.Spec.Project != req.Project || q.Spec.Recipient != replier || (req.InReplyTo.UID != "" && req.InReplyTo.UID != string(q.UID)) {
		return nil, inboxHidden()
	}
	if q.Spec.Kind != inbox.KindQuestion {
		return nil, newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "only a question takes a reply; this message is a reply or a plain message")
	}
	if err := s.inboxUnanswered(ctx, q); err != nil {
		return nil, err
	}
	return q, nil
}

// inboxUnanswered refuses a second distinct reply. Two replies racing past it
// can both land; the derived state then names the oldest and counts both.
func (s *Server) inboxUnanswered(ctx context.Context, q *coord.InboxMessage) error {
	replies, err := s.Inbox.Replies(ctx, q)
	if err != nil {
		return inboxUnknown()
	}
	if len(replies) != 0 {
		return newError(http.StatusConflict, apiv1.CodeConflict, "the question already has a reply; nothing was sent")
	}
	return nil
}

// inboxQuery reads the optional thread view and sent box. Each appears at
// most once with its one value.
func inboxQuery(q url.Values) (thread, sent bool, err error) {
	for _, p := range []struct {
		key, want string
		out       *bool
	}{{apiv1.InboxViewQuery, apiv1.InboxViewThread, &thread}, {apiv1.InboxBoxQuery, apiv1.InboxBoxSent, &sent}} {
		values := q[p.key]
		if len(values) > 1 || (len(values) == 1 && values[0] != p.want) {
			return false, false, badRequest("%s must be %s", p.key, p.want)
		}
		*p.out = len(values) == 1
	}
	return thread, sent, nil
}

func inboxRef(r coord.InboxMessageRef) *apiv1.InboxMessageRef {
	return &apiv1.InboxMessageRef{Name: r.Name, UID: r.UID}
}

// withInboxCorrelation adds the kind and the question a reply answers.
func withInboxCorrelation(view apiv1.InboxMetadata, m *coord.InboxMessage) apiv1.InboxMetadata {
	view.Kind = apiv1.InboxKindPlain
	if m.Spec.Kind != "" {
		view.Kind = m.Spec.Kind
	}
	if m.Spec.InReplyTo != nil {
		view.InReplyTo = inboxRef(*m.Spec.InReplyTo)
	}
	return view
}

type inboxEnded int

const (
	inboxEndedUnknown inboxEnded = iota
	inboxRecipientLive
	inboxRecipientEnded
)

// inboxRecipientState reads whether a question's recipient can still answer.
// Gone, replaced, deleting, Archived or Failed has ended, as has a task whose
// agent exited, failed or was interrupted: a task runs once. A Suspended
// Session can resume and read its inbox, so it has not ended.
func (s *Server) inboxRecipientState(ctx context.Context, id coord.SessionIdentity, memo map[coord.SessionIdentity]inboxEnded) inboxEnded {
	if got, ok := memo[id]; ok {
		return got
	}
	answer := inboxEndedUnknown
	var sess v1alpha1.AgentSession
	if id.Namespace == s.Policy.SessionNamespace {
		err := s.Live.Get(ctx, types.NamespacedName{Namespace: id.Namespace, Name: id.Name}, &sess)
		switch {
		case apierrors.IsNotFound(err):
			answer = inboxRecipientEnded
		case err != nil:
		case string(sess.UID) != id.UID || !sess.DeletionTimestamp.IsZero() || sess.Status.Phase == v1alpha1.PhaseArchived || sess.Status.Phase == v1alpha1.PhaseFailed:
			answer = inboxRecipientEnded
		case sess.Spec.Mode == v1alpha1.ModeTask && sess.Status.Agent != nil &&
			(sess.Status.Agent.Status == protocol.AgentExited || sess.Status.Agent.Status == protocol.AgentFailed || sess.Status.Agent.Status == protocol.AgentInterrupted):
			answer = inboxRecipientEnded
		default:
			answer = inboxRecipientLive
		}
	}
	memo[id] = answer
	return answer
}

// repliesLookup finds a question's live replies, oldest first.
type repliesLookup func(context.Context, *coord.InboxMessage) ([]coord.InboxMessage, error)

// pageReplies scans the replies a thread list page can hold once, on first
// use: replies sent by the caller for its inbox, addressed to it for its sent
// box, or every reply in the project for a coordinator host. A failed or
// oversized scan leaves each question on the page Unknown.
func (s *Server) pageReplies(project string, caller *coord.SessionIdentity, sent bool) repliesLookup {
	var index []coord.InboxMessage
	var err error
	scanned := false
	return func(ctx context.Context, q *coord.InboxMessage) ([]coord.InboxMessage, error) {
		if !scanned {
			scanned = true
			var from, to *coord.SessionIdentity
			if caller != nil && sent {
				to = caller
			} else if caller != nil {
				from = caller
			}
			index, err = s.Inbox.ReplyIndex(ctx, project, from, to)
		}
		if err != nil {
			return nil, err
		}
		return inbox.RepliesOf(q, index)
	}
}

// inboxThread derives a question's state, or a reply's view of it. Plain
// messages have none. Any lookup it cannot confirm leaves the state Unknown;
// it never guesses Unanswered. WaitingOn names only a Session confirmed live.
func (s *Server) inboxThread(ctx context.Context, m *coord.InboxMessage, archived bool, lookup repliesLookup, memo map[coord.SessionIdentity]inboxEnded) *apiv1.InboxThread {
	now := s.now()
	t := &apiv1.InboxThread{State: apiv1.InboxStateUnknown, StaleAfterSeconds: int64(inbox.StaleAfter / time.Second), ObservedAt: now.UTC()}
	var reply *coord.InboxMessage
	switch m.Spec.Kind {
	case inbox.KindReply:
		t.Question, t.Reply = *inboxRef(*m.Spec.InReplyTo), inboxRef(inbox.Ref(m))
		reply = m
	case inbox.KindQuestion:
		t.Question = *inboxRef(inbox.Ref(m))
		if archived {
			return t
		}
		replies, err := lookup(ctx, m)
		if err != nil {
			return t
		}
		if len(replies) == 0 {
			switch s.inboxRecipientState(ctx, m.Spec.Recipient, memo) {
			case inboxRecipientEnded:
				t.State = apiv1.InboxStateRecipientEnded
			case inboxRecipientLive:
				t.State = apiv1.InboxStateUnanswered
				if now.Sub(m.CreationTimestamp.Time) > inbox.StaleAfter {
					t.State = apiv1.InboxStateStale
				}
				owner := inboxOwner(m.Spec.Recipient)
				t.WaitingOn = &owner
			}
			return t
		}
		reply = &replies[0]
		t.Reply = inboxRef(inbox.Ref(reply))
		if len(replies) > 1 {
			t.Replies = len(replies)
		}
	default:
		return nil
	}
	if archived {
		return t
	}
	_, err := s.Inbox.ReadAck(ctx, reply)
	switch {
	case err == nil:
		t.State = apiv1.InboxStateAcknowledged
	case errors.Is(err, inbox.ErrNotFound):
		t.State = apiv1.InboxStateAnswered
		if s.inboxRecipientState(ctx, reply.Spec.Recipient, memo) == inboxRecipientLive {
			owner := inboxOwner(reply.Spec.Recipient)
			t.WaitingOn = &owner
		}
	}
	return t
}
