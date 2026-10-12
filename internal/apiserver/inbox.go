package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func inboxHidden() error { return notFound("inbox record not found") }
func inboxUnknown() error {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "inbox outcome Unknown; retry the same intent and key")
}
func (s *Server) inboxAvailable() error {
	if s.Inbox == nil || s.Live == nil || s.Projects == nil || len(s.InboxProjects) == 0 {
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "durable inbox is not enabled")
	}
	return nil
}
func sessionIdentity(s *v1alpha1.AgentSession) coord.SessionIdentity {
	return coord.SessionIdentity{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)}
}
func inboxActor(c *caller) coord.ClaimActor {
	return coord.ClaimActor{Kind: string(c.kind), ServiceAccount: c.identity.serviceAccountRef(), PodName: c.identity.PodName, PodUID: c.identity.PodUID}
}

// Unlike claimCaller, the recipient proof permits a deleting original Session.
// Its currently controlled executor Pod must still exist and not be deleting.
func (s *Server) inboxRecipientCaller(ctx context.Context, c *caller) (*v1alpha1.AgentSession, error) {
	if c.kind != kindSession || c.session == nil || c.pod == nil || s.Live == nil {
		return nil, inboxHidden()
	}
	var pod corev1.Pod
	var sess v1alpha1.AgentSession
	if s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: c.identity.PodName}, &pod) != nil ||
		string(pod.UID) != c.identity.PodUID || !pod.DeletionTimestamp.IsZero() || pod.Spec.ServiceAccountName != s.Policy.SessionServiceAccount ||
		s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: c.session.Name}, &sess) != nil ||
		sess.UID == "" || sess.UID != c.session.UID || !metav1.IsControlledBy(&pod, &sess) {
		return nil, inboxHidden()
	}
	owner := metav1.GetControllerOf(&pod)
	if owner == nil || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.Kind != "AgentSession" || owner.Name != sess.Name {
		return nil, inboxHidden()
	}
	return &sess, nil
}

func (s *Server) inboxSenderCaller(ctx context.Context, c *caller) (*v1alpha1.AgentSession, error) {
	sess, err := s.inboxRecipientCaller(ctx, c)
	if err != nil {
		return nil, err
	}
	if !sess.DeletionTimestamp.IsZero() {
		return nil, inboxHidden()
	}
	return sess, nil
}

func (s *Server) inboxReadFence(ctx context.Context, c *caller, project string, original *coord.SessionIdentity) error {
	fresh, err := s.inboxReadScope(ctx, c, project)
	if err != nil || ((original == nil) != (fresh == nil)) || (original != nil && *original != *fresh) {
		return inboxHidden()
	}
	return nil
}

func (s *Server) inboxReadScope(ctx context.Context, c *caller, project string) (*coord.SessionIdentity, error) {
	if !projectcatalog.ValidName(project) || !slices.Contains(s.InboxProjects, project) {
		return nil, inboxHidden()
	}
	if c.kind == kindCoordinator {
		if !slices.Contains(s.InboxCoordinatorReadProjects, project) {
			return nil, inboxHidden()
		}
		if _, err := s.claimCaller(ctx, c); err != nil {
			return nil, inboxHidden()
		}
		return nil, nil
	}
	sess, err := s.inboxRecipientCaller(ctx, c)
	if err != nil {
		return nil, err
	}
	// Existing inbox scope comes from the stored original recipient/project,
	// not a replacement catalog or a later change to its Session annotation.
	id := sessionIdentity(sess)
	return &id, nil
}

func boundedInboxJSON(r *http.Request, limit int64, out any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(data)) > limit || !utf8.Valid(data) {
		return badRequest("expected bounded UTF-8 inbox JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return badRequest("expected one inbox JSON object with intent fields only")
	}
	return nil
}

func inboxProvenance(snapshot projectcatalog.Snapshot) coord.ClaimProvenance {
	return coord.ClaimProvenance{CatalogRevision: snapshot.CatalogRevision(), RulesRevision: snapshot.RulesRevision(), GitHub: strings.ToLower(snapshot.Selected().GitHub), DefaultBranch: snapshot.Selected().DefaultBranch, InitialPinState: "Unknown"}
}

func (s *Server) newInboxRecipient(ctx context.Context, id coord.SessionIdentity, project string) (*v1alpha1.AgentSession, error) {
	if id.Namespace != s.Policy.SessionNamespace || !inbox.ValidSession(id) {
		return nil, inboxHidden()
	}
	var sess v1alpha1.AgentSession
	if s.Live.Get(ctx, types.NamespacedName{Namespace: id.Namespace, Name: id.Name}, &sess) != nil || sessionIdentity(&sess) != id {
		return nil, inboxHidden()
	}
	snapshot, err := claimSnapshot(&sess)
	if err != nil || snapshot.Project() != project {
		return nil, inboxHidden()
	}
	return &sess, nil
}

func (s *Server) publishInbox(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.inboxAvailable(); err != nil {
		return 0, nil, err
	}
	var req apiv1.PublishInboxRequest
	if err := boundedInboxJSON(r, inbox.MaxRequestBytes, &req); err != nil {
		return 0, nil, err
	}
	if !projectcatalog.ValidName(req.Project) || !inbox.ValidKey(req.Key) || !inbox.ValidText(req.Text) {
		return 0, nil, badRequest("inbox intent requires explicit project, bounded key and 1 to 16384 UTF-8 bytes")
	}
	if c.kind != kindSession || !slices.Contains(s.InboxProjects, req.Project) {
		return 0, nil, inboxHidden()
	}
	sender, err := s.inboxSenderCaller(ctx, c)
	if err != nil {
		return 0, nil, inboxHidden()
	}
	snapshot, err := claimSnapshot(sender)
	if err != nil || snapshot.Project() != req.Project {
		return 0, nil, inboxHidden()
	}
	spec := coord.InboxMessageSpec{Project: req.Project, Key: req.Key, Sender: sessionIdentity(sender), Recipient: coord.SessionIdentity{Namespace: req.Recipient.Namespace, Name: req.Recipient.Name, UID: req.Recipient.UID}, Actor: inboxActor(c), Provenance: inboxProvenance(snapshot), Repository: snapshot.Selected().Name, Provider: string(sender.Spec.Agent), RequestedModel: sender.Spec.Model, Text: req.Text}
	if !inbox.ValidMessage(spec) {
		return 0, nil, badRequest("inbox intent or saved sender attribution is invalid")
	}
	// An accepted retry must survive recipient deletion and catalog updates.
	existing, err := s.Inbox.Get(ctx, inbox.MessageName(spec.Sender, spec.Key))
	if err == nil {
		fresh, fenceErr := s.inboxSenderCaller(ctx, c)
		if fenceErr != nil || !sameSessionBinding(fresh, sender) {
			return 0, nil, inboxHidden()
		}
		return inboxPublishResult(existing, false, spec)
	}
	if !errors.Is(err, inbox.ErrNotFound) {
		return 0, nil, inboxUnknown()
	}
	recipient, err := s.newInboxRecipient(ctx, spec.Recipient, spec.Project)
	if err != nil {
		return 0, nil, err
	}
	_, selected, err := s.Projects.snapshot(ctx, s.Live, spec.Project, spec.Repository)
	if err != nil || !strings.EqualFold(selected.GitHub, snapshot.Selected().GitHub) {
		return 0, nil, inboxHidden()
	}
	// Fence both original identities after catalog I/O, before the only mutation.
	fresh, err := s.inboxSenderCaller(ctx, c)
	if err != nil || !sameSessionBinding(fresh, sender) {
		return 0, nil, inboxHidden()
	}
	freshRecipient, err := s.newInboxRecipient(ctx, spec.Recipient, spec.Project)
	if err != nil || !sameSessionBinding(freshRecipient, recipient) {
		return 0, nil, inboxHidden()
	}
	m, created, err := s.Inbox.Create(ctx, spec)
	if errors.Is(err, inbox.ErrLimit) {
		return 0, nil, newError(http.StatusTooManyRequests, apiv1.CodeLimitExceeded, "inbox message limit reached for this sender Session or project; nothing was sent")
	}
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	return inboxPublishResult(m, created, spec)
}

func inboxPublishResult(m *coord.InboxMessage, created bool, spec coord.InboxMessageSpec) (int, any, error) {
	status, outcome := http.StatusOK, "Confirmed"
	if !inbox.SameIntent(m.Spec, spec) {
		status, outcome = http.StatusConflict, "Conflict"
	} else if created {
		status, outcome = http.StatusCreated, "Created"
	}
	return status, apiv1.InboxResult{Outcome: outcome, Message: inboxMetadata(m)}, nil
}
func (s *Server) authorizedInbox(ctx context.Context, r *http.Request, c *caller) (*coord.InboxMessage, error) {
	if err := s.inboxAvailable(); err != nil {
		return nil, err
	}
	project := r.URL.Query().Get("project")
	recipient, err := s.inboxReadScope(ctx, c, project)
	if err != nil {
		return nil, err
	}
	if !inbox.ValidMessageName(r.PathValue("message")) {
		return nil, inboxHidden()
	}
	m, err := s.Inbox.Get(ctx, r.PathValue("message"))
	if errors.Is(err, inbox.ErrNotFound) {
		return nil, inboxHidden()
	}
	if err != nil {
		return nil, inboxUnknown()
	}
	if m.Spec.Project != project || (recipient != nil && m.Spec.Recipient != *recipient) {
		return nil, inboxHidden()
	}
	if err := s.inboxReadFence(ctx, c, project, recipient); err != nil {
		return nil, err
	}
	return m, nil
}
func (s *Server) readInbox(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	m, err := s.authorizedInbox(ctx, r, c)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, apiv1.InboxMessage{InboxMetadata: inboxMetadata(m), Text: m.Spec.Text}, nil
}
func (s *Server) listInbox(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.inboxAvailable(); err != nil {
		return 0, nil, err
	}
	project := r.URL.Query().Get("project")
	recipient, err := s.inboxReadScope(ctx, c, project)
	if err != nil {
		return 0, nil, err
	}
	limit := int64(100)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || limit < 1 || limit > 100 {
			return 0, nil, badRequest("limit must be between 1 and 100")
		}
	}
	items, next, err := s.Inbox.List(ctx, project, recipient, limit, r.URL.Query().Get("continue"))
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	if err := s.inboxReadFence(ctx, c, project, recipient); err != nil {
		return 0, nil, err
	}
	views := make([]apiv1.InboxMetadata, 0, len(items))
	for i := range items {
		views = append(views, inboxMetadata(&items[i]))
	}
	return http.StatusOK, apiv1.InboxList{Items: views, Continue: next}, nil
}
func (s *Server) ackInbox(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindSession {
		return 0, nil, inboxHidden()
	}
	var empty struct{}
	if err := boundedInboxJSON(r, 128, &empty); err != nil {
		return 0, nil, err
	}
	m, err := s.authorizedInbox(ctx, r, c)
	if err != nil {
		return 0, nil, err
	}
	fresh, err := s.inboxReadScope(ctx, c, m.Spec.Project)
	if err != nil || fresh == nil || *fresh != m.Spec.Recipient {
		return 0, nil, inboxHidden()
	}
	a, created, err := s.Inbox.Ack(ctx, m, inboxActor(c))
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	status, outcome := http.StatusOK, "Confirmed"
	if created {
		status, outcome = http.StatusCreated, "Created"
	}
	return status, apiv1.InboxAckResult{Outcome: outcome, Ack: inboxAckView(a)}, nil
}
func (s *Server) readInboxAck(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	m, err := s.authorizedInbox(ctx, r, c)
	if err != nil {
		return 0, nil, err
	}
	a, err := s.Inbox.ReadAck(ctx, m)
	if errors.Is(err, inbox.ErrNotFound) {
		return 0, nil, inboxHidden()
	}
	if err != nil {
		return 0, nil, inboxUnknown()
	}
	var recipient *coord.SessionIdentity
	if c.kind == kindSession {
		recipient = &m.Spec.Recipient
	}
	if err := s.inboxReadFence(ctx, c, m.Spec.Project, recipient); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, inboxAckView(a), nil
}
func inboxOwner(s coord.SessionIdentity) apiv1.TaskClaimOwner {
	return apiv1.TaskClaimOwner{Namespace: s.Namespace, Name: s.Name, UID: s.UID}
}
func inboxActorView(a coord.ClaimActor) apiv1.TaskClaimActor {
	return apiv1.TaskClaimActor{Kind: a.Kind, ServiceAccount: a.ServiceAccount, PodName: a.PodName, PodUID: a.PodUID}
}
func inboxMetadata(m *coord.InboxMessage) apiv1.InboxMetadata {
	p := m.Spec.Provenance
	provenance := apiv1.TaskClaimProvenance{CatalogRevision: p.CatalogRevision, RulesRevision: p.RulesRevision, GitHub: p.GitHub, DefaultBranch: p.DefaultBranch, InitialPinState: p.InitialPinState, ReportedHead: p.ReportedHead}
	if p.ReportedAt != nil {
		at := p.ReportedAt.Time
		provenance.ReportedAt = &at
	}
	return apiv1.InboxMetadata{Name: m.Name, UID: string(m.UID), Project: m.Spec.Project, Key: m.Spec.Key, Sender: inboxOwner(m.Spec.Sender), Recipient: inboxOwner(m.Spec.Recipient), Actor: inboxActorView(m.Spec.Actor), Provenance: provenance, Repository: m.Spec.Repository, Provider: m.Spec.Provider, RequestedModel: m.Spec.RequestedModel, CreatedAt: m.CreationTimestamp.Time}
}
func inboxAckView(a *coord.InboxReadAck) apiv1.InboxReadAck {
	return apiv1.InboxReadAck{Name: a.Name, UID: string(a.UID), Project: a.Spec.Project, MessageName: a.Spec.MessageName, MessageUID: a.Spec.MessageUID, Recipient: inboxOwner(a.Spec.Recipient), Actor: inboxActorView(a.Spec.Actor), CreatedAt: a.CreationTimestamp.Time}
}
