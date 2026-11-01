package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// portalQuestionAuditEnabled is the route gate. The portal reader still cannot
// call /v1/inbox. An empty project allowlist, a missing inbox, or either
// prerequisite flag leaves this path unregistered.
func (s *Server) portalQuestionAuditEnabled() bool {
	return s != nil && s.PortalQuestionAudit && s.Policy.PortalReaderEnabled && s.InboxQuestions && s.Inbox != nil && s.Live != nil && len(s.PortalQuestionProjects) > 0 && len(s.InboxProjects) > 0
}

// portalQuestionAudit reads one page of metadata for an explicit project and
// the exact live session name and UID. The identity passed to List or ListSent
// is that non-nil session. A catalog snapshot is not consulted, and a nil
// coordinator scope is not used. The session is read again after derivation.
func (s *Server) portalQuestionAudit(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if !s.portalQuestionAuditEnabled() {
		return 0, nil, notFound("question audit metadata was not found")
	}
	if c == nil || c.kind != kindPortalReader {
		return 0, nil, forbidden("only the portal reader may read question audit metadata")
	}
	q, err := parsePortalQuestionQuery(r)
	if err != nil {
		return 0, nil, err
	}
	if !slices.Contains(s.PortalQuestionProjects, q.project) || !slices.Contains(s.InboxProjects, q.project) {
		return 0, nil, notFound("question audit metadata was not found")
	}
	id, err := s.provenQuestionSession(ctx, q.session, q.uid)
	if err != nil {
		return 0, nil, err
	}
	var items []coord.InboxMessage
	var next string
	if q.sent {
		items, next, err = s.Inbox.ListSent(ctx, q.project, *id, q.limit, q.cursor)
	} else {
		items, next, err = s.Inbox.List(ctx, q.project, id, q.limit, q.cursor)
	}
	if err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "question audit metadata is unavailable")
	}
	// One store page. Plain messages on it are omitted and do not widen the
	// metadata scope. The handler does not read another page to refill the limit.
	// Truncated and NextCursor still describe the store page, so a filtered
	// page can be shorter than Limit, including empty while Truncated is set.
	memo := map[coord.SessionIdentity]inboxEnded{}
	lookup := s.pageReplies(q.project, id, q.sent)
	views := make([]apiv1.PortalQuestionAuditItem, 0, len(items))
	for i := range items {
		m := &items[i]
		if m.Spec.Kind != inbox.KindQuestion && m.Spec.Kind != inbox.KindReply {
			continue
		}
		thread := s.inboxThread(ctx, m, false, lookup, memo)
		ack := apiv1.PortalQuestionAuditAck{Status: apiv1.PortalQuestionAckUnavailable}
		if thread != nil && thread.State == apiv1.InboxStateAcknowledged && thread.Reply != nil {
			ack = s.questionAuditAck(ctx, q.project, *thread.Reply)
			if ack.Status != apiv1.PortalQuestionAckRecorded {
				reduced := *thread
				reduced.State = apiv1.InboxStateUnknown
				reduced.WaitingOn = nil
				thread = &reduced
				ack = apiv1.PortalQuestionAuditAck{Status: apiv1.PortalQuestionAckUnavailable}
			}
		}
		views = append(views, questionAuditItem(m, thread, ack))
	}
	fresh, freshErr := s.provenQuestionSession(ctx, q.session, q.uid)
	if freshErr != nil || fresh == nil || *fresh != *id {
		return 0, nil, notFound("question audit metadata was not found")
	}
	for i := range views {
		if !apiv1.PortalQuestionAuditItemOK(views[i]) {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "question audit metadata is unavailable")
		}
	}
	resp := apiv1.PortalQuestionAudit{
		Version: apiv1.PortalQuestionAuditVersion, Project: q.project,
		Session: apiv1.PortalQuestionAuditIdentity{Namespace: id.Namespace, Name: id.Name, UID: id.UID},
		Box:     q.box, Limit: int(q.limit), Truncated: next != "", NextCursor: next,
		Origin: apiv1.PortalQuestionOriginUnknown, Approval: apiv1.PortalQuestionApprovalNotRecorded,
		ProviderConsumption: apiv1.PortalQuestionConsumptionNotRecorded, ServedModel: apiv1.PortalQuestionServedModelUnknown,
		Items: views,
	}
	encoded, mErr := json.Marshal(resp)
	if mErr != nil || len(encoded)+1 > apiv1.PortalQuestionAuditMaxBytes {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "question audit metadata is unavailable")
	}
	return http.StatusOK, resp, nil
}

// provenQuestionSession is a live Get of the exact name and UID. It does not
// read spec.project, a snapshot annotation, or a replacement catalog.
func (s *Server) provenQuestionSession(ctx context.Context, name, uid string) (*coord.SessionIdentity, error) {
	if s == nil || s.Live == nil || len(validation.IsDNS1123Label(s.Policy.SessionNamespace)) != 0 {
		return nil, notFound("question audit metadata was not found")
	}
	var sess v1alpha1.AgentSession
	if s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, &sess) != nil || sess.Name != name || sess.Namespace != s.Policy.SessionNamespace || string(sess.UID) != uid {
		return nil, notFound("question audit metadata was not found")
	}
	id := sessionIdentity(&sess)
	if !inbox.ValidSession(id) {
		return nil, notFound("question audit metadata was not found")
	}
	return &id, nil
}

// questionAuditAck copies actor and time only from the acknowledgement of the
// exact linked reply. The actor must be the recorded recipient session, not
// the message's sender and not a claimed origin. A missing record, a crossed
// name or UID, a project or recipient mismatch, an actor that is not that
// session, or a time before the reply stays unavailable. The thread state
// word is not a source for either field, and Acknowledged is not approval.
func (s *Server) questionAuditAck(ctx context.Context, project string, ref apiv1.InboxMessageRef) apiv1.PortalQuestionAuditAck {
	unavailable := apiv1.PortalQuestionAuditAck{Status: apiv1.PortalQuestionAckUnavailable}
	if s == nil || s.Inbox == nil || !inbox.ValidMessageName(ref.Name) || !questionAuditUID(ref.UID) {
		return unavailable
	}
	m, err := s.Inbox.Get(ctx, ref.Name)
	if err != nil || m == nil || string(m.UID) != ref.UID || m.Spec.Project != project || m.Spec.Kind != inbox.KindReply || !inbox.ValidSession(m.Spec.Recipient) {
		return unavailable
	}
	a, err := s.Inbox.ReadAck(ctx, m)
	if err != nil || a == nil || a.Spec.MessageName != m.Name || a.Spec.MessageUID != string(m.UID) || a.Spec.Project != project || a.Spec.Recipient != m.Spec.Recipient || !inbox.ValidAck(a.Spec) {
		return unavailable
	}
	actor := inboxActorView(a.Spec.Actor)
	if !apiv1.PortalQuestionAckActorMatches(actor, inboxOwner(m.Spec.Recipient)) {
		return unavailable
	}
	if a.CreationTimestamp.IsZero() || a.CreationTimestamp.Time.Before(m.CreationTimestamp.Time) {
		return unavailable
	}
	at := a.CreationTimestamp.UTC()
	return apiv1.PortalQuestionAuditAck{Status: apiv1.PortalQuestionAckRecorded, Actor: &actor, CreatedAt: &at}
}

func questionAuditItem(m *coord.InboxMessage, thread *apiv1.InboxThread, ack apiv1.PortalQuestionAuditAck) apiv1.PortalQuestionAuditItem {
	kind := apiv1.InboxKindPlain
	if m.Spec.Kind != "" {
		kind = m.Spec.Kind
	}
	item := apiv1.PortalQuestionAuditItem{
		Name: m.Name, UID: string(m.UID), Kind: kind,
		Sender: inboxOwner(m.Spec.Sender), Recipient: inboxOwner(m.Spec.Recipient),
		Actor: inboxActorView(m.Spec.Actor), Provenance: questionAuditProvenance(m.Spec.Provenance),
		CreatedAt: m.CreationTimestamp.UTC(),
		Origin:    apiv1.PortalQuestionOriginUnknown, Approval: apiv1.PortalQuestionApprovalNotRecorded,
		ProviderConsumption: apiv1.PortalQuestionConsumptionNotRecorded, ServedModel: apiv1.PortalQuestionServedModelUnknown,
		Acknowledgement: ack,
	}
	if m.Spec.InReplyTo != nil {
		ref := apiv1.InboxMessageRef{Name: m.Spec.InReplyTo.Name, UID: m.Spec.InReplyTo.UID}
		item.InReplyTo = &ref
	}
	if thread != nil {
		copied := *thread
		if copied.WaitingOn != nil {
			owner := *copied.WaitingOn
			copied.WaitingOn = &owner
		}
		if copied.Reply != nil {
			reply := *copied.Reply
			copied.Reply = &reply
		}
		item.Thread = &copied
	}
	return item
}

func questionAuditProvenance(p coord.ClaimProvenance) apiv1.TaskClaimProvenance {
	out := apiv1.TaskClaimProvenance{CatalogRevision: p.CatalogRevision, RulesRevision: p.RulesRevision, GitHub: p.GitHub, DefaultBranch: p.DefaultBranch, InitialPinState: p.InitialPinState, ReportedHead: p.ReportedHead}
	if p.ReportedAt != nil {
		at := p.ReportedAt.Time
		out.ReportedAt = &at
	}
	return out
}

type portalQuestionQuery struct {
	project, session, uid, box, cursor string
	limit                              int64
	sent                               bool
}

func parsePortalQuestionQuery(r *http.Request) (portalQuestionQuery, error) {
	var q portalQuestionQuery
	if r == nil || r.URL == nil || r.URL.Path != apiv1.PortalQuestionAuditPath || (r.URL.RawPath != "" && r.URL.RawPath != apiv1.PortalQuestionAuditPath) || strings.Contains(r.URL.RawQuery, ";") {
		return q, badRequest("invalid question audit query")
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || values.Encode() != r.URL.RawQuery {
		return q, badRequest("invalid question audit query")
	}
	allowed := map[string]struct{}{"project": {}, "session": {}, "uid": {}, "box": {}, "limit": {}, "continue": {}}
	for key, got := range values {
		if _, ok := allowed[key]; !ok || len(got) != 1 || got[0] == "" {
			return q, badRequest("invalid question audit query")
		}
	}
	for _, key := range []string{"project", "session", "uid", "box"} {
		if _, ok := values[key]; !ok {
			return q, badRequest("invalid question audit query")
		}
	}
	q.project, q.session, q.uid, q.box = values.Get("project"), values.Get("session"), values.Get("uid"), values.Get("box")
	if !projectcatalog.ValidName(q.project) || len(validation.IsDNS1123Subdomain(q.session)) != 0 || !questionAuditUID(q.uid) {
		return q, badRequest("invalid question audit query")
	}
	switch q.box {
	case apiv1.PortalQuestionBoxInbox:
	case apiv1.PortalQuestionBoxSent:
		q.sent = true
	default:
		return q, badRequest("invalid question audit query")
	}
	q.limit = apiv1.PortalQuestionAuditDefaultLimit
	if raw, ok := values["limit"]; ok {
		n, nErr := strconv.ParseInt(raw[0], 10, 64)
		if nErr != nil || n < 1 || n > apiv1.PortalQuestionAuditMaxLimit || strconv.FormatInt(n, 10) != raw[0] {
			return q, badRequest("invalid question audit query")
		}
		q.limit = n
	}
	if raw, ok := values["continue"]; ok {
		if !apiv1.PortalQuestionCursorOK(raw[0]) {
			return q, badRequest("invalid question audit query")
		}
		q.cursor = raw[0]
	}
	return q, nil
}

func questionAuditUID(uid string) bool {
	if len(uid) < 1 || len(uid) > 128 {
		return false
	}
	for _, r := range uid {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
