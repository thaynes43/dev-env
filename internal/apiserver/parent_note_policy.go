package apiserver

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func policyInboxCaller(c *caller) *coord.InboxCaller {
	if c == nil || c.policy == nil {
		return nil
	}
	return &coord.InboxCaller{ServiceAccount: meatballAccount, PolicyUID: string(c.policy.UID)}
}

func (s *Server) policyNoteFence(ctx context.Context, c *caller) error {
	p, err := s.readCallerPolicy(ctx)
	if err != nil {
		return err
	}
	if c == nil || c.policy == nil || p == nil || p.UID != c.policy.UID || p.Spec.ServiceAccount != meatballAccount {
		return inboxHidden()
	}
	return nil
}

func (s *Server) policyOwnedChild(ctx context.Context, c *caller, name string) (*v1alpha1.AgentSession, error) {
	if len(validation.IsDNS1123Subdomain(name)) != 0 {
		return nil, inboxHidden()
	}
	var sess v1alpha1.AgentSession
	if s.Live == nil || s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, &sess) != nil || !policyOwns(c, &sess) {
		return nil, inboxHidden()
	}
	return &sess, nil
}

func (s *Server) policyWriteFence(ctx context.Context, c *caller, child *v1alpha1.AgentSession) error {
	if err := s.policyNoteFence(ctx, c); err != nil {
		return err
	}
	fresh, err := s.policyOwnedChild(ctx, c, child.Name)
	if err != nil || !sameSessionBinding(fresh, child) {
		return inboxHidden()
	}
	return nil
}

func (s *Server) storeParentNote(ctx context.Context, spec coord.InboxMessageSpec) (int, any, error) {
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
	return s.finishParentNote(m, created, spec)
}

func (s *Server) tellMeatballParent(ctx context.Context, c *caller, sender *v1alpha1.AgentSession, snapshot projectcatalog.Snapshot, req apiv1.TellParentRequest) (int, any, error) {
	if !slices.Contains(s.InboxProjects, snapshot.Project()) {
		return 0, nil, inboxHidden()
	}
	spec := coord.InboxMessageSpec{
		Project: snapshot.Project(), Key: req.Key, Kind: req.Kind, Text: req.Text,
		Sender: sessionIdentity(sender), Actor: inboxActor(c), Provenance: inboxProvenance(snapshot),
		Repository: snapshot.Selected().Name, Provider: string(sender.Spec.Agent), RequestedModel: sender.Spec.Model,
	}
	existing, err := s.Inbox.Get(ctx, inbox.MessageName(spec.Sender, spec.Key))
	if err == nil {
		if !inbox.IsParentNote(existing) {
			return 0, nil, parentNoteKeyConflict()
		}
		if existing.Spec.CallerRecipient != nil && existing.Spec.CallerRecipient.ServiceAccount == meatballAccount {
			copied := *existing.Spec.CallerRecipient
			spec.CallerRecipient = &copied
		}
		if err := s.parentNoteSenderFence(ctx, c, sender); err != nil {
			return 0, nil, err
		}
		return s.finishParentNote(existing, false, spec)
	}
	if !errors.Is(err, inbox.ErrNotFound) {
		return 0, nil, inboxUnknown()
	}
	p, err := s.readCallerPolicy(ctx)
	if err != nil {
		return 0, nil, err
	}
	if p == nil || p.Spec.ServiceAccount != meatballAccount {
		return 0, nil, newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "this Session has no recorded session parent")
	}
	spec.CallerRecipient = &coord.InboxCaller{ServiceAccount: meatballAccount, PolicyUID: string(p.UID)}
	if !inbox.ValidMessage(spec) {
		return 0, nil, badRequest("parent note intent or saved sender attribution is invalid")
	}
	if s.Projects == nil {
		return 0, nil, inboxUnknown()
	}
	_, selected, err := s.Projects.snapshot(ctx, s.Live, spec.Project, spec.Repository)
	if err != nil || !strings.EqualFold(selected.GitHub, snapshot.Selected().GitHub) {
		return 0, nil, inboxHidden()
	}
	if err := s.parentNoteSenderFence(ctx, c, sender); err != nil {
		return 0, nil, err
	}
	fresh, err := s.readCallerPolicy(ctx)
	if err != nil {
		return 0, nil, err
	}
	if fresh == nil || string(fresh.UID) != spec.CallerRecipient.PolicyUID {
		return 0, nil, inboxHidden()
	}
	return s.storeParentNote(ctx, spec)
}

func (s *Server) sendPolicyParentNote(ctx context.Context, c *caller, req apiv1.TellParentRequest) (int, any, error) {
	if req.Kind != "" {
		return 0, nil, badRequest("a parent message has no kind")
	}
	if req.Session == "" {
		return 0, nil, badRequest("a parent message names the child session")
	}
	if !inbox.ValidKey(req.Key) || !inbox.ValidText(req.Text) {
		return 0, nil, badRequest("a parent message requires a bounded key and 1 to 16384 UTF-8 bytes")
	}
	if err := s.policyNoteFence(ctx, c); err != nil {
		return 0, nil, err
	}
	caller := policyInboxCaller(c)
	spec := coord.InboxMessageSpec{Key: req.Key, Text: req.Text, CallerSender: caller, Actor: inboxActor(c)}
	existing, err := s.Inbox.Get(ctx, inbox.MessageNameFor(spec))
	if err == nil {
		if !inbox.IsParentNote(existing) {
			return 0, nil, parentNoteKeyConflict()
		}
		if existing.Spec.Recipient.Name == req.Session && callersMatch(existing.Spec.CallerSender, caller) {
			spec.Project = existing.Spec.Project
			spec.Recipient = existing.Spec.Recipient
			spec.Provenance = existing.Spec.Provenance
			spec.Repository = existing.Spec.Repository
			spec.Provider = existing.Spec.Provider
			spec.RequestedModel = existing.Spec.RequestedModel
			copied := *existing.Spec.CallerSender
			spec.CallerSender = &copied
		}
		if err := s.policyNoteFence(ctx, c); err != nil {
			return 0, nil, err
		}
		return s.finishParentNote(existing, false, spec)
	}
	if !errors.Is(err, inbox.ErrNotFound) {
		return 0, nil, inboxUnknown()
	}
	child, err := s.policyOwnedChild(ctx, c, req.Session)
	if err != nil {
		return 0, nil, err
	}
	return s.createPolicyParentMessage(ctx, c, child, spec)
}

func (s *Server) createPolicyParentMessage(ctx context.Context, c *caller, child *v1alpha1.AgentSession, spec coord.InboxMessageSpec) (int, any, error) {
	snapshot, err := claimSnapshot(child)
	if err != nil {
		return 0, nil, newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "a child needs a project snapshot and no workspace")
	}
	if !slices.Contains(s.InboxProjects, snapshot.Project()) {
		return 0, nil, inboxHidden()
	}
	spec.Project = snapshot.Project()
	spec.Recipient = sessionIdentity(child)
	spec.Provenance = inboxProvenance(snapshot)
	spec.Repository = snapshot.Selected().Name
	spec.Provider = string(child.Spec.Agent)
	spec.RequestedModel = child.Spec.Model
	if !inbox.ValidMessage(spec) {
		return 0, nil, badRequest("parent message intent or saved sender attribution is invalid")
	}
	if s.Projects == nil {
		return 0, nil, inboxUnknown()
	}
	_, selected, err := s.Projects.snapshot(ctx, s.Live, spec.Project, spec.Repository)
	if err != nil || !strings.EqualFold(selected.GitHub, snapshot.Selected().GitHub) {
		return 0, nil, inboxHidden()
	}
	if err := s.policyWriteFence(ctx, c, child); err != nil {
		return 0, nil, err
	}
	fresh, err := s.policyOwnedChild(ctx, c, child.Name)
	if err != nil || sessionIdentity(fresh) != spec.Recipient {
		return 0, nil, inboxHidden()
	}
	return s.storeParentNote(ctx, spec)
}

func callersMatch(stored, live *coord.InboxCaller) bool {
	return stored != nil && live != nil && stored.ServiceAccount == live.ServiceAccount && stored.PolicyUID == live.PolicyUID
}

func (s *Server) listPolicyParentNotes(ctx context.Context, r *http.Request, c *caller) (int, any, error) {
	if err := s.policyNoteFence(ctx, c); err != nil {
		return 0, nil, err
	}
	q, err := parseParentNoteQuery(r)
	if err != nil {
		return 0, nil, err
	}
	items, next, err := s.Inbox.ListCallerParentNotes(ctx, policyInboxCaller(c), q.sent, q.limit, q.cursor)
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	if err := s.policyNoteFence(ctx, c); err != nil {
		return 0, nil, err
	}
	s.logParentNoteList(q.box, len(items), true)
	return http.StatusOK, apiv1.InboxList{Items: parentNoteViews(items), Continue: next, Box: q.box}, nil
}

func (s *Server) readPolicyParentNote(ctx context.Context, r *http.Request, c *caller) (int, any, error) {
	if err := s.policyNoteFence(ctx, c); err != nil {
		return 0, nil, err
	}
	m, err := s.Inbox.GetCallerParentNote(ctx, r.PathValue("message"), policyInboxCaller(c))
	if errors.Is(err, inbox.ErrNotFound) {
		return 0, nil, inboxHidden()
	}
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	if err := s.policyNoteFence(ctx, c); err != nil {
		return 0, nil, err
	}
	s.logParentNote("read", m)
	return http.StatusOK, apiv1.InboxMessage{InboxMetadata: withInboxCorrelation(inboxMetadata(m), m), Text: m.Spec.Text}, nil
}

func (s *Server) answerPolicyParentNote(ctx context.Context, r *http.Request, c *caller) (int, any, error) {
	if err := s.policyNoteFence(ctx, c); err != nil {
		return 0, nil, err
	}
	caller := policyInboxCaller(c)
	note, err := s.callerNoteFor(ctx, r.PathValue("message"), caller, true)
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
	child, err := s.policyOwnedChild(ctx, c, note.Spec.Sender.Name)
	if err != nil || sessionIdentity(child) != note.Spec.Sender {
		return 0, nil, inboxHidden()
	}
	ref := inbox.Ref(note)
	spec := coord.InboxMessageSpec{
		Project: note.Spec.Project, Key: req.Key, Text: req.Text, Kind: inbox.KindReply,
		Recipient: note.Spec.Sender, InReplyTo: &ref, CallerSender: caller,
		Actor: inboxActor(c), Repository: child.Spec.Repo, Provider: string(child.Spec.Agent), RequestedModel: child.Spec.Model,
		Provenance: note.Spec.Provenance,
	}
	if !inbox.ValidMessage(spec) {
		return 0, nil, badRequest("parent answer intent or saved sender attribution is invalid")
	}
	existing, err := s.Inbox.Get(ctx, inbox.MessageNameFor(spec))
	if err == nil {
		if !inbox.IsParentNote(existing) {
			return 0, nil, parentNoteKeyConflict()
		}
		if err := s.policyWriteFence(ctx, c, child); err != nil {
			return 0, nil, err
		}
		return s.finishParentNote(existing, false, spec)
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
	if err := s.policyWriteFence(ctx, c, child); err != nil {
		return 0, nil, err
	}
	fresh, err := s.callerNoteFor(ctx, note.Name, caller, true)
	if err != nil {
		return 0, nil, err
	}
	if inbox.Ref(fresh) != ref {
		return 0, nil, inboxHidden()
	}
	return s.storeParentNote(ctx, spec)
}

// Direction is checked before a reply is stored. A note for another caller,
// a reply, and a plain message all look like absence.
func (s *Server) callerNoteFor(ctx context.Context, name string, caller *coord.InboxCaller, answer bool) (*coord.InboxMessage, error) {
	m, err := s.Inbox.GetCallerParentNote(ctx, name, caller)
	if errors.Is(err, inbox.ErrNotFound) {
		return nil, inboxHidden()
	}
	if err != nil {
		return nil, inboxUnknown()
	}
	if answer && (!validParentNoteKind(m.Spec.Kind) || !callersMatch(m.Spec.CallerRecipient, caller)) {
		return nil, inboxHidden()
	}
	return m, nil
}
