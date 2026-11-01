package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// questionAuditTimeout bounds both box reads together, on the request context.
// Request cancellation stops the reads. A slow, failed, or cancelled audit
// leaves the session page in place. This bound is separate from sessionReadTimeout.
var questionAuditTimeout = 4 * time.Second

var errQuestionAudit = errors.New("question audit unavailable")

type questionAuditMissingError struct{}

func (questionAuditMissingError) Error() string { return "question audit missing" }

func questionAuditIsMissing(err error) bool {
	return errors.Is(err, questionAuditMissingError{})
}

type questionAuditExpect struct {
	project, name, uid, box string
}

type questionAuditLoc struct {
	group, row int
	name, uid  string
}

// UseQuestionAudit connects selected-session detail to the metadata route for
// one explicit project. An empty project leaves the read off.
func (s *SessionSource) UseQuestionAudit(project string) {
	if s != nil {
		s.questionAuditProject = project
	}
}

// attachQuestionAudit adds one panel to the selected session. No selection, or
// no configured project, performs no request. At most two reads run, inbox
// then sent, and neither follows a cursor. Failure replaces only this panel.
func (s *SessionSource) attachQuestionAudit(ctx context.Context, page portal.ProductPage, selected string) portal.ProductPage {
	if s == nil || s.questionAuditProject == "" || selected == "" {
		return page
	}
	loc, found := questionAuditLocate(page, selected)
	if !found {
		return page
	}
	if loc.name == "" || loc.uid == "" || ctx.Err() != nil {
		return questionAuditApply(page, loc, questionAuditUnavailable())
	}
	// Bound the panel by the request and by questionAuditTimeout. Cancellation
	// of the request cancels this context, so the reads stop with the request.
	panelCtx, cancel := context.WithTimeout(ctx, questionAuditTimeout)
	defer cancel()
	inbox, err := s.readQuestionAudit(panelCtx, loc.name, loc.uid, apiv1.PortalQuestionBoxInbox)
	if err != nil {
		if questionAuditIsMissing(err) {
			return questionAuditApply(page, loc, questionAuditMissingSection())
		}
		return questionAuditApply(page, loc, questionAuditUnavailable())
	}
	sent, err := s.readQuestionAudit(panelCtx, loc.name, loc.uid, apiv1.PortalQuestionBoxSent)
	if err != nil {
		return questionAuditApply(page, loc, questionAuditUnavailable())
	}
	return questionAuditApply(page, loc, questionAuditSection(inbox, sent))
}

func questionAuditLocate(page portal.ProductPage, selected string) (questionAuditLoc, bool) {
	for i := range page.Groups {
		for j := range page.Groups[i].Rows {
			m := page.Groups[i].Rows[j].Messages
			if m != nil && m.SessionUID == selected {
				return questionAuditLoc{group: i, row: j, name: m.Session, uid: m.SessionUID}, true
			}
		}
	}
	return questionAuditLoc{}, false
}

func questionAuditApply(page portal.ProductPage, loc questionAuditLoc, section portal.ProductSection) portal.ProductPage {
	page.Groups = append([]portal.ProductGroup(nil), page.Groups...)
	for i := range page.Groups {
		rows := append([]portal.ProductRow(nil), page.Groups[i].Rows...)
		if i == loc.group && loc.row >= 0 && loc.row < len(rows) {
			row := rows[loc.row]
			row.Sections = append(append([]portal.ProductSection{}, row.Sections...), section)
			rows[loc.row] = row
		}
		page.Groups[i].Rows = rows
	}
	return page
}

func (s *SessionSource) readQuestionAudit(ctx context.Context, name, uid, box string) (apiv1.PortalQuestionAudit, error) {
	var none apiv1.PortalQuestionAudit
	if s == nil || s.client == nil || s.now == nil || s.path == "" || s.questionAuditProject == "" {
		return none, errQuestionAudit
	}
	token, err := operatorCredential(s.path, s.now())
	if err != nil {
		return none, errQuestionAudit
	}
	query := url.Values{}
	query.Set("box", box)
	query.Set("limit", strconv.Itoa(apiv1.PortalQuestionAuditDefaultLimit))
	query.Set("project", s.questionAuditProject)
	query.Set("session", name)
	query.Set("uid", uid)
	body, status, err := questionAuditGet(ctx, s.client, operatorPortalOrigin+apiv1.PortalQuestionAuditPath+"?"+query.Encode(), token)
	if err != nil {
		return none, errQuestionAudit
	}
	if status == http.StatusNotFound {
		return none, questionAuditMissingError{}
	}
	if status != http.StatusOK {
		return none, errQuestionAudit
	}
	return decodePortalQuestionAudit(body, s.now(), questionAuditExpect{project: s.questionAuditProject, name: name, uid: uid, box: box})
}

func questionAuditGet(ctx context.Context, client *http.Client, rawURL, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, errQuestionAudit
	}
	req.Header.Set("Accept", mediaJSON)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil || response == nil {
		return nil, 0, errQuestionAudit
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, apiv1.PortalQuestionAuditMaxBytes+1))
	if err != nil || len(body) > apiv1.PortalQuestionAuditMaxBytes {
		return nil, response.StatusCode, errQuestionAudit
	}
	if response.StatusCode != http.StatusOK {
		return body, response.StatusCode, nil
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != mediaJSON {
		return nil, response.StatusCode, errQuestionAudit
	}
	return body, response.StatusCode, nil
}

func decodePortalQuestionAudit(body []byte, now time.Time, expect questionAuditExpect) (apiv1.PortalQuestionAudit, error) {
	var none apiv1.PortalQuestionAudit
	if len(body) == 0 || len(body) > apiv1.PortalQuestionAuditMaxBytes {
		return none, errQuestionAudit
	}
	obj, err := jsonObject(body)
	if err != nil || !exactKeys(obj, []string{"version", "project", "session", "box", "limit", "truncated", "origin", "approval", "providerConsumption", "servedModel", "items"}, "nextCursor") {
		return none, errQuestionAudit
	}
	if !questionAuditShape(obj) {
		return none, errQuestionAudit
	}
	var page apiv1.PortalQuestionAudit
	if json.Unmarshal(body, &page) != nil {
		return none, errQuestionAudit
	}
	if page.Version != apiv1.PortalQuestionAuditVersion || page.Project != expect.project || page.Session.Name != expect.name || page.Session.UID != expect.uid || page.Box != expect.box || page.Limit != apiv1.PortalQuestionAuditDefaultLimit {
		return none, errQuestionAudit
	}
	if !apiv1.PortalQuestionProjectOK(page.Project) || !questionAuditDNSLabel(page.Session.Namespace) {
		return none, errQuestionAudit
	}
	if page.Origin != apiv1.PortalQuestionOriginUnknown || page.Approval != apiv1.PortalQuestionApprovalNotRecorded || page.ProviderConsumption != apiv1.PortalQuestionConsumptionNotRecorded || page.ServedModel != apiv1.PortalQuestionServedModelUnknown {
		return none, errQuestionAudit
	}
	if page.Truncated != (page.NextCursor != "") || (page.Truncated && !apiv1.PortalQuestionCursorOK(page.NextCursor)) {
		return none, errQuestionAudit
	}
	items, ok := jsonArray(obj["items"])
	if !ok || page.Items == nil || len(page.Items) != len(items) || len(page.Items) > page.Limit {
		return none, errQuestionAudit
	}
	for _, item := range page.Items {
		if !apiv1.PortalQuestionAuditItemOK(item) || questionAuditAhead(item.CreatedAt, now) || item.Thread == nil || questionAuditAhead(item.Thread.ObservedAt, now) {
			return none, errQuestionAudit
		}
		if item.Acknowledgement.CreatedAt != nil && questionAuditAhead(*item.Acknowledgement.CreatedAt, now) {
			return none, errQuestionAudit
		}
	}
	return page, nil
}

func questionAuditAhead(at, now time.Time) bool {
	return at.IsZero() || at.After(now.Add(2*time.Second))
}

func questionAuditDNSLabel(name string) bool {
	return apiv1.PortalQuestionProjectOK(name)
}

func questionAuditShape(obj map[string]json.RawMessage) bool {
	session, err := jsonObject(obj["session"])
	if err != nil || !exactKeys(session, []string{"namespace", "name", "uid"}) {
		return false
	}
	if raw, ok := obj["nextCursor"]; ok {
		if _, ok := jsonString(raw); !ok {
			return false
		}
	}
	items, ok := jsonArray(obj["items"])
	if !ok {
		return false
	}
	for _, raw := range items {
		if !questionItemShape(raw) {
			return false
		}
	}
	return true
}

func questionItemShape(raw json.RawMessage) bool {
	obj, err := jsonObject(raw)
	if err != nil || !exactKeys(obj, []string{"name", "uid", "kind", "sender", "recipient", "actor", "provenance", "createdAt", "origin", "approval", "providerConsumption", "servedModel", "thread", "acknowledgement"}, "inReplyTo") {
		return false
	}
	if !questionOwnerShape(obj["sender"]) || !questionOwnerShape(obj["recipient"]) || !questionActorShape(obj["actor"]) || !questionProvenanceShape(obj["provenance"]) || !questionThreadShape(obj["thread"]) || !questionAckShape(obj["acknowledgement"]) {
		return false
	}
	if replyTo, ok := obj["inReplyTo"]; ok {
		ref, err := jsonObject(replyTo)
		if err != nil || !exactKeys(ref, []string{"name", "uid"}) {
			return false
		}
	}
	return true
}

func questionOwnerShape(raw json.RawMessage) bool {
	obj, err := jsonObject(raw)
	return err == nil && exactKeys(obj, []string{"namespace", "name", "uid"})
}

func questionActorShape(raw json.RawMessage) bool {
	obj, err := jsonObject(raw)
	return err == nil && exactKeys(obj, []string{"kind", "serviceAccount", "podName", "podUID"})
}

func questionProvenanceShape(raw json.RawMessage) bool {
	obj, err := jsonObject(raw)
	if err != nil || !exactKeys(obj, []string{"catalogRevision", "rulesRevision", "github", "defaultBranch", "initialPinState"}, "reportedHead", "reportedAt") {
		return false
	}
	_, head := obj["reportedHead"]
	_, at := obj["reportedAt"]
	return head == at
}

func questionThreadShape(raw json.RawMessage) bool {
	obj, err := jsonObject(raw)
	if err != nil || !exactKeys(obj, []string{"state", "question", "staleAfterSeconds", "observedAt"}, "reply", "replies", "waitingOn") {
		return false
	}
	if !questionRefShape(obj["question"]) {
		return false
	}
	if reply, ok := obj["reply"]; ok && !questionRefShape(reply) {
		return false
	}
	if waiting, ok := obj["waitingOn"]; ok && !questionOwnerShape(waiting) {
		return false
	}
	return true
}

func questionRefShape(raw json.RawMessage) bool {
	obj, err := jsonObject(raw)
	return err == nil && exactKeys(obj, []string{"name", "uid"})
}

func questionAckShape(raw json.RawMessage) bool {
	obj, err := jsonObject(raw)
	if err != nil {
		return false
	}
	status, ok := jsonString(obj["status"])
	if !ok {
		return false
	}
	switch status {
	case apiv1.PortalQuestionAckUnavailable:
		return exactKeys(obj, []string{"status"})
	case apiv1.PortalQuestionAckRecorded:
		if !exactKeys(obj, []string{"status", "actor", "createdAt"}) || !questionActorShape(obj["actor"]) {
			return false
		}
		_, ok := jsonString(obj["createdAt"])
		return ok
	default:
		return false
	}
}

func questionAuditUnavailable() portal.ProductSection {
	return portal.ProductSection{
		Title:  "Question audit",
		States: []portal.ProductState{{Status: portal.ProductStatus{Text: "Unavailable", Tone: "bad"}, Text: "The question audit could not be checked. No record is shown."}},
		Note:   "Session inventory is unchanged. Nothing here is approval, provider consumption, or a served model.",
	}
}

func questionAuditMissingSection() portal.ProductSection {
	return portal.ProductSection{
		Title:  "Question audit",
		States: []portal.ProductState{{Status: portal.ProductStatus{Text: "Missing", Tone: "muted"}, Text: "This operator did not provide question audit metadata. That is not an empty audit."}},
		Note:   "An older operator answers 404. No question or reply is shown.",
	}
}

func questionAuditSection(inbox, sent apiv1.PortalQuestionAudit) portal.ProductSection {
	section := portal.ProductSection{
		Title: "Question audit",
		Fields: []portal.ProductField{
			{Label: "Origin", Value: apiv1.PortalQuestionOriginUnknown},
			{Label: "Approval", Value: apiv1.PortalQuestionApprovalNotRecorded},
			{Label: "Provider consumption", Value: apiv1.PortalQuestionConsumptionNotRecorded},
			{Label: "Served model", Value: apiv1.PortalQuestionServedModelUnknown},
		},
		Note: "Origin, approval, provider consumption and served model are fixed because this schema does not record them. An API acknowledgement is not approval or provider consumption. Message text is not shown. A later store page is not read.",
	}
	section.States = append(section.States, questionBoxStates("Inbox", inbox)...)
	section.States = append(section.States, questionBoxStates("Sent", sent)...)
	section.Fields = append(section.Fields, questionAuditFields("Inbox", inbox)...)
	section.Fields = append(section.Fields, questionAuditFields("Sent", sent)...)
	return section
}

func questionBoxStates(box string, page apiv1.PortalQuestionAudit) []portal.ProductState {
	var states []portal.ProductState
	unknown := false
	for _, item := range page.Items {
		if item.Thread != nil && item.Thread.State == apiv1.InboxStateUnknown {
			unknown = true
		}
	}
	switch {
	case len(page.Items) == 0 && !page.Truncated:
		states = append(states, portal.ProductState{Status: portal.ProductStatus{Text: "Empty", Tone: "muted"}, Text: box + " has no question or reply records. This is not a missing route."})
	case len(page.Items) == 0 && page.Truncated:
		states = append(states, portal.ProductState{Status: portal.ProductStatus{Text: "Truncated", Tone: "wait"}, Text: box + " store page had no question or reply after plain messages were omitted. The next page was not read."})
	default:
		states = append(states, portal.ProductState{Status: portal.ProductStatus{Text: "Recorded", Tone: "info"}, Text: box + " shows " + strconv.Itoa(len(page.Items)) + " question or reply records from one store page."})
	}
	if page.Truncated && len(page.Items) > 0 {
		states = append(states, portal.ProductState{Status: portal.ProductStatus{Text: "Truncated", Tone: "wait"}, Text: box + " stopped at the store page. The portal does not follow the cursor."})
	}
	if unknown {
		states = append(states, portal.ProductState{Status: portal.ProductStatus{Text: "Unknown", Tone: "wait"}, Text: box + " has a derived state that could not be confirmed. Unknown is not unanswered."})
	}
	return states
}

func questionAuditFields(box string, page apiv1.PortalQuestionAudit) []portal.ProductField {
	out := make([]portal.ProductField, 0, len(page.Items))
	for i, item := range page.Items {
		if item.Thread == nil {
			continue
		}
		ack := "ack unavailable"
		if item.Acknowledgement.Status == apiv1.PortalQuestionAckRecorded && item.Acknowledgement.Actor != nil && item.Acknowledgement.CreatedAt != nil {
			ack = "ack recorded " + item.Acknowledgement.Actor.PodName + " " + item.Acknowledgement.CreatedAt.UTC().Format(time.RFC3339)
		}
		value := item.Kind + " · " + item.Thread.State + " · " + item.Name + " · from " + item.Sender.Name + " · to " + item.Recipient.Name + " · actor " + item.Actor.PodName + " · " + item.CreatedAt.UTC().Format(time.RFC3339) + " · " + ack
		if item.InReplyTo != nil {
			value += " · reply to " + item.InReplyTo.Name
		}
		out = append(out, portal.ProductField{Label: box + " " + strconv.Itoa(i+1), Value: value, Mono: true})
	}
	return out
}
