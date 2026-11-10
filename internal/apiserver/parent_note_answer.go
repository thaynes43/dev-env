package apiserver

import (
	"context"
	"errors"
	"net/http"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

func (s *Server) answerParentNote(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.parentNotesAvailable(); err != nil {
		return 0, nil, err
	}
	sender, err := s.inboxSenderCaller(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	party := sessionIdentity(sender)
	note, err := s.parentNoteFor(ctx, r.PathValue("message"), party, true)
	if err != nil {
		return 0, nil, err
	}
	var req apiv1.AnswerParentRequest
	if err := boundedInboxJSON(r, inbox.MaxRequestBytes, &req); err != nil {
		return 0, nil, err
	}
	if !inbox.ValidKey(req.Key) || !inbox.ValidText(req.Text) {
		return 0, nil, badRequest("a parent answer requires a bounded key and 1 to 16384 UTF-8 bytes")
	}
	ref := inbox.Ref(note)
	spec := coord.InboxMessageSpec{
		Project: note.Spec.Project, Key: req.Key, Text: req.Text, Kind: inbox.KindReply,
		Sender: party, Recipient: note.Spec.Sender, InReplyTo: &ref,
		Actor: inboxActor(c), Repository: sender.Spec.Repo, Provider: string(sender.Spec.Agent), RequestedModel: sender.Spec.Model,
		// The parent need not have a snapshot. Project provenance is the
		// child's saved snapshot; writer attribution is the current parent.
		Provenance: note.Spec.Provenance,
	}
	if !inbox.ValidMessage(spec) {
		return 0, nil, badRequest("parent answer intent or saved sender attribution is invalid")
	}
	existing, err := s.Inbox.Get(ctx, inbox.MessageName(party, req.Key))
	if err == nil {
		if !inbox.IsParentNote(existing) {
			return 0, nil, parentNoteKeyConflict()
		}
		if err := s.parentNoteSenderFence(ctx, c, sender); err != nil {
			return 0, nil, err
		}
		return inboxPublishResult(existing, false, spec)
	}
	if !errors.Is(err, inbox.ErrNotFound) {
		return 0, nil, inboxUnknown()
	}
	replies, _, err := s.Inbox.ParentReplies(ctx, note)
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	if len(replies) != 0 {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "the parent note already has an answer; nothing was replaced")
	}
	if err := s.parentNoteSenderFence(ctx, c, sender); err != nil {
		return 0, nil, err
	}
	fresh, err := s.parentNoteFor(ctx, note.Name, party, true)
	if err != nil {
		return 0, nil, err
	}
	if inbox.Ref(fresh) != ref {
		return 0, nil, inboxHidden()
	}
	// Use the peer inbox's check-then-create race rule (DESIGN-062 7.1).
	// Each answer is immutable; concurrent distinct keys can both land.
	m, created, err := s.Inbox.CreateParentNote(ctx, spec)
	if errors.Is(err, inbox.ErrLimit) {
		return 0, nil, newError(http.StatusTooManyRequests, apiv1.CodeLimitExceeded, "inbox message limit reached for this sender Session or project; nothing was sent")
	}
	if errors.Is(err, inbox.ErrSchemaNotUpgraded) {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "inbox schema not upgraded; nothing was sent")
	}
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	if !inbox.IsParentNote(m) {
		return 0, nil, parentNoteKeyConflict()
	}
	return inboxPublishResult(m, created, spec)
}

// Direction and kind are checked before a reply is read or created. Replies,
// plain messages, peer records and the other party all look like absence.
func (s *Server) parentNoteFor(ctx context.Context, name string, party coord.SessionIdentity, answer bool) (*coord.InboxMessage, error) {
	m, err := s.Inbox.GetParentNote(ctx, name, party)
	if errors.Is(err, inbox.ErrNotFound) {
		return nil, inboxHidden()
	}
	if err != nil {
		return nil, inboxUnknown()
	}
	if !validParentNoteKind(m.Spec.Kind) || (answer && m.Spec.Recipient != party) || (!answer && m.Spec.Sender != party) {
		return nil, inboxHidden()
	}
	return m, nil
}
