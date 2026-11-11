package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

// The routes are always registered. With the flag off an authenticated caller
// gets 503 and nothing is stored (DESIGN-062 section 7.3), as the durable
// inbox answers when it is off; an older operator answers 404.
func (s *Server) registerParentNotes(mux *http.ServeMux) {
	mux.Handle(apiv1.ParentNotesPath, s.serve(route{inbox: true, coordinator: true, methods: map[string]handler{
		http.MethodPost: s.tellParent, http.MethodGet: s.listParentNotes,
	}}))
	mux.Handle(apiv1.ParentNotesPath+"/{message}", s.serve(route{inbox: true, coordinator: true, methods: map[string]handler{
		http.MethodGet: s.readParentNote,
	}}))
	mux.Handle(apiv1.ParentNotesPath+"/{message}/answer", s.serve(route{inbox: true, coordinator: true, methods: map[string]handler{
		http.MethodPost: s.answerParentNote,
	}}))
	mux.Handle(apiv1.ParentNotesPath+"/{message}/wait", s.serve(route{inbox: true, coordinator: true, methods: map[string]handler{
		http.MethodGet: s.waitParentNote,
	}}))
}

func (s *Server) parentNotesAvailable() error {
	if !s.ParentNotes {
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "parent notes are not enabled")
	}
	if s.Inbox == nil || s.Live == nil {
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "durable inbox is not enabled")
	}
	return nil
}

func validParentNoteKind(kind string) bool {
	return kind == inbox.KindQuestion || kind == inbox.KindResult || kind == inbox.KindBlocker
}

func (s *Server) tellParent(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.parentNotesAvailable(); err != nil {
		return 0, nil, err
	}
	var req apiv1.TellParentRequest
	if err := boundedInboxJSON(r, inbox.MaxRequestBytes, &req); err != nil {
		return 0, nil, err
	}
	if c.kind == kindPolicy {
		return s.sendPolicyParentNote(ctx, c, req)
	}
	if req.Session != "" {
		return 0, nil, badRequest("a session tells its recorded parent and names no child")
	}
	sender, err := s.inboxSenderCaller(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	if !inbox.ValidKey(req.Key) || !inbox.ValidText(req.Text) || !validParentNoteKind(req.Kind) {
		return 0, nil, badRequest("a parent note requires a bounded key, kind question, result or blocker, and 1 to 16384 UTF-8 bytes")
	}
	snapshot, err := claimSnapshot(sender)
	if err != nil {
		return 0, nil, newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "a child needs a project snapshot and no workspace to tell its parent")
	}
	if sender.Spec.Parent == meatballAccount {
		return s.tellMeatballParent(ctx, c, sender, snapshot, req)
	}
	if sender.Spec.Parent == "" || strings.Contains(sender.Spec.Parent, "/") || len(validation.IsDNS1123Subdomain(sender.Spec.Parent)) != 0 {
		return 0, nil, newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "this Session has no recorded session parent")
	}
	if !slices.Contains(s.InboxProjects, snapshot.Project()) {
		return 0, nil, inboxHidden()
	}
	spec := coord.InboxMessageSpec{
		Project: snapshot.Project(), Key: req.Key, Kind: req.Kind, Text: req.Text,
		Sender: sessionIdentity(sender), Actor: inboxActor(c), Provenance: inboxProvenance(snapshot),
		Repository: snapshot.Selected().Name, Provider: string(sender.Spec.Agent), RequestedModel: sender.Spec.Model,
	}
	// As with inbox ask, an accepted retry survives a removed parent and a
	// changed catalog. Bind only the original stored parent, never a replacement.
	existing, err := s.Inbox.Get(ctx, inbox.MessageName(spec.Sender, spec.Key))
	if err == nil {
		if !inbox.IsParentNote(existing) {
			return 0, nil, parentNoteKeyConflict()
		}
		if err := s.bindInboxRetry(ctx, &spec, existing.Spec, apiv1.PublishInboxRequest{Kind: inbox.KindQuestion, ToParent: true}, newCoordSender(sender)); err != nil {
			return 0, nil, err
		}
		if err := s.parentNoteSenderFence(ctx, c, sender); err != nil {
			return 0, nil, err
		}
		return s.finishParentNote(existing, false, spec)
	}
	if !errors.Is(err, inbox.ErrNotFound) {
		return 0, nil, inboxUnknown()
	}
	parentRequest := apiv1.PublishInboxRequest{ToParent: true}
	if spec.Recipient, err = s.resolveInboxQuestionRecipient(ctx, parentRequest, newCoordSender(sender)); err != nil {
		return 0, nil, err
	}
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
	freshParent, err := s.resolveInboxQuestionRecipient(ctx, parentRequest, newCoordSender(sender))
	if err != nil || freshParent != spec.Recipient {
		return 0, nil, inboxHidden()
	}
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
	// A concurrent peer create can win the shared sender/key name. It cannot
	// become a parent note or confirm a tell merely because its spec agrees.
	if !inbox.IsParentNote(m) {
		return 0, nil, parentNoteKeyConflict()
	}
	return s.finishParentNote(m, created, spec)
}

func (s *Server) finishParentNote(m *coord.InboxMessage, created bool, spec coord.InboxMessageSpec) (int, any, error) {
	status, body, err := inboxPublishResult(m, created, spec)
	if status == http.StatusOK || status == http.StatusCreated {
		s.logParentNote("write", m)
	}
	return status, body, err
}

func (s *Server) logParentNote(verb string, m *coord.InboxMessage) {
	if m == nil {
		return
	}
	kind := m.Spec.Kind
	if kind == "" {
		kind = "plain"
	}
	side := "session"
	if m.Spec.CallerSender != nil || m.Spec.CallerRecipient != nil {
		side = "caller"
	}
	s.Log.Info("parent note", "verb", verb, "name", m.Name, "kind", kind, "bytes", len(m.Spec.Text), "parent", side)
}

func (s *Server) logParentNoteList(box string, n int, caller bool) {
	side := "session"
	if caller {
		side = "caller"
	}
	s.Log.Info("parent note", "verb", "list", "box", box, "count", n, "parent", side)
}

func parentNoteKeyConflict() error {
	return newError(http.StatusConflict, apiv1.CodeConflict, "the key already identifies a peer inbox message; nothing was replaced")
}

func (s *Server) parentNoteSenderFence(ctx context.Context, c *caller, original *v1alpha1.AgentSession) error {
	fresh, err := s.inboxSenderCaller(ctx, c)
	if err != nil || !sameSessionBinding(fresh, original) {
		return inboxHidden()
	}
	return nil
}

func (s *Server) parentNoteReadFence(ctx context.Context, c *caller, original coord.SessionIdentity) error {
	fresh, err := s.inboxRecipientCaller(ctx, c)
	if err != nil || sessionIdentity(fresh) != original {
		return inboxHidden()
	}
	return nil
}

func (s *Server) readParentNote(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.parentNotesAvailable(); err != nil {
		return 0, nil, err
	}
	if c.kind == kindPolicy {
		return s.readPolicyParentNote(ctx, r, c)
	}
	sess, err := s.inboxRecipientCaller(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	party := sessionIdentity(sess)
	m, err := s.Inbox.GetParentNote(ctx, r.PathValue("message"), party)
	if errors.Is(err, inbox.ErrNotFound) {
		return 0, nil, inboxHidden()
	}
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	if err := s.parentNoteReadFence(ctx, c, party); err != nil {
		return 0, nil, err
	}
	s.logParentNote("read", m)
	return http.StatusOK, apiv1.InboxMessage{InboxMetadata: withInboxCorrelation(inboxMetadata(m), m), Text: m.Spec.Text}, nil
}

type parentNoteQuery struct {
	box    string
	limit  int64
	cursor string
	sent   bool
}

func parseParentNoteQuery(r *http.Request) (parentNoteQuery, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return parentNoteQuery{}, badRequest("invalid parent note query")
	}
	for key, values := range q {
		if len(values) != 1 || (key != "box" && key != "limit" && key != "continue") {
			return parentNoteQuery{}, badRequest("invalid parent note query")
		}
	}
	box := q.Get("box")
	if box == "" {
		box = apiv1.ParentNotesInbox
	}
	if box != apiv1.ParentNotesInbox && box != apiv1.ParentNotesSent {
		return parentNoteQuery{}, badRequest("box must be inbox or sent")
	}
	limit := int64(100)
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || limit < 1 || limit > 100 {
			return parentNoteQuery{}, badRequest("limit must be between 1 and 100")
		}
	}
	if !inbox.ValidParentNoteCursor(q.Get("continue")) {
		return parentNoteQuery{}, badRequest("invalid parent note continuation")
	}
	return parentNoteQuery{box: box, limit: limit, cursor: q.Get("continue"), sent: box == apiv1.ParentNotesSent}, nil
}

func parentNoteViews(items []coord.InboxMessage) []apiv1.InboxMetadata {
	views := make([]apiv1.InboxMetadata, 0, len(items))
	for i := range items {
		views = append(views, withInboxCorrelation(inboxMetadata(&items[i]), &items[i]))
	}
	return views
}

func (s *Server) listParentNotes(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.parentNotesAvailable(); err != nil {
		return 0, nil, err
	}
	if c.kind == kindPolicy {
		return s.listPolicyParentNotes(ctx, r, c)
	}
	sess, err := s.inboxRecipientCaller(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	q, err := parseParentNoteQuery(r)
	if err != nil {
		return 0, nil, err
	}
	party := sessionIdentity(sess)
	items, next, err := s.Inbox.ListParentNotes(ctx, party, q.sent, q.limit, q.cursor)
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	if err := s.parentNoteReadFence(ctx, c, party); err != nil {
		return 0, nil, err
	}
	s.logParentNoteList(q.box, len(items), false)
	return http.StatusOK, apiv1.InboxList{Items: parentNoteViews(items), Continue: next, Box: q.box}, nil
}
