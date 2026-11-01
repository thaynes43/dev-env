package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

func TestPortalQuestionAuditStaleConstant(t *testing.T) {
	if apiv1.PortalQuestionAuditStaleAfterSeconds != int64(inbox.StaleAfter/time.Second) {
		t.Fatalf("stale constant %d", apiv1.PortalQuestionAuditStaleAfterSeconds)
	}
}

func TestPortalQuestionAuditDefaultOffDeniesInbox(t *testing.T) {
	f, _, child, _ := askReplyFixture(t)
	enableQuestionAuditPortal(t, f, false)
	q := publish(t, f, child, askParent("q-1", "SECRET_QUESTION_TEXT"), http.StatusCreated)
	for _, raw := range []string{questionAuditURL("child", q.UID, apiv1.PortalQuestionBoxSent), apiv1.InboxPath + "?project=sample", apiv1.InboxPath + "/" + q.Name + "?project=sample", apiv1.InboxPath + "/" + q.Name + "/ack?project=sample"} {
		w := f.do(http.MethodGet, raw, "tok-portal", nil)
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "SECRET_QUESTION_TEXT") {
			t.Fatalf("portal read %s: %d %s", raw, w.Code, w.Body.String())
		}
	}
	if w := f.do(http.MethodGet, questionAuditURL("child", q.UID, apiv1.PortalQuestionBoxSent), "tok-portal", nil); w.Code != http.StatusNotFound {
		t.Fatalf("default-off route %d", w.Code)
	}
	if w := f.do(http.MethodPost, apiv1.InboxPath+"/"+q.Name+"/ack?project=sample", "tok-portal", struct{}{}); w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Fatal("portal acknowledged a message")
	}
}

func TestPortalQuestionAuditRefusesTheWrongCallerAndQuery(t *testing.T) {
	f, lead, child, _ := askReplyFixture(t)
	enableQuestionAuditPortal(t, f, true)
	q := publish(t, f, child, askParent("q-1", "SECRET_QUESTION_TEXT"), http.StatusCreated)
	s := f.session("child")
	raw := questionAuditURL(s.Name, string(s.UID), apiv1.PortalQuestionBoxSent)
	for _, tok := range []string{lead, tokHuman, tokClient, tokCoordinator} {
		if w := f.do(http.MethodGet, raw, tok, nil); w.Code != http.StatusForbidden {
			t.Fatalf("%s answered %d", tok, w.Code)
		}
	}
	if w := f.do(http.MethodPost, raw, "tok-portal", nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %d", w.Code)
	}
	bad := []string{
		raw + "&extra=1",
		strings.Replace(raw, "box=sent", "box=plain", 1),
		strings.Replace(raw, "uid="+string(s.UID), "uid=not-the-uid", 1),
		strings.Replace(raw, "project=sample", "project=other", 1),
		strings.Replace(raw, "session=child", "session=lead", 1),
	}
	for _, path := range bad {
		w := f.do(http.MethodGet, path, "tok-portal", nil)
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "SECRET_QUESTION_TEXT") {
			t.Fatalf("bad query accepted %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	page := auditOf(t, f, "child", apiv1.PortalQuestionBoxSent)
	item := auditNamed(t, page, q.Name)
	if item.Kind != apiv1.InboxKindQuestion || item.InReplyTo != nil || item.Thread == nil || item.Thread.State != apiv1.InboxStateUnanswered || item.Thread.Question.Name != q.Name || item.Thread.Question.UID != q.UID {
		t.Fatalf("question item %+v", item)
	}
	if page.Project != "sample" || page.Session.Name != "child" || page.Session.UID != string(s.UID) || page.Origin != apiv1.PortalQuestionOriginUnknown || page.Approval != apiv1.PortalQuestionApprovalNotRecorded {
		t.Fatalf("envelope %+v", page)
	}
	_ = q
}

func TestPortalQuestionAuditUsesInboxThreadStates(t *testing.T) {
	f, lead, child, _ := askReplyFixture(t)
	enableQuestionAuditPortal(t, f, true)
	q := publish(t, f, child, askParent("q-1", "SECRET_QUESTION_TEXT"), http.StatusCreated)
	if got := auditState(t, f, "child", apiv1.PortalQuestionBoxSent, q.Name); got.Thread.State != apiv1.InboxStateUnanswered || got.Thread.WaitingOn == nil || got.Thread.WaitingOn.Name != "lead" {
		t.Fatalf("unanswered %+v", got.Thread)
	}
	f.now = f.now.Add(inbox.StaleAfter + time.Minute)
	if got := auditState(t, f, "child", apiv1.PortalQuestionBoxSent, q.Name); got.Thread.State != apiv1.InboxStateStale {
		t.Fatalf("stale %+v", got.Thread)
	}
	r := publish(t, f, lead, replyTo(q, "r-1", "SECRET_REPLY_TEXT"), http.StatusCreated)
	if got := auditState(t, f, "child", apiv1.PortalQuestionBoxSent, q.Name); got.Thread.State != apiv1.InboxStateAnswered || got.Thread.Reply == nil || got.Thread.Reply.Name != r.Name {
		t.Fatalf("answered %+v", got.Thread)
	}
	reply := auditState(t, f, "child", apiv1.PortalQuestionBoxInbox, r.Name)
	if reply.Kind != apiv1.InboxKindReply || reply.InReplyTo == nil || reply.InReplyTo.Name != q.Name || reply.InReplyTo.UID != q.UID || reply.Thread.Question != *reply.InReplyTo || reply.Thread.Reply == nil || reply.Thread.Reply.Name != r.Name || reply.Thread.Reply.UID != r.UID {
		t.Fatalf("reply correlation %+v", reply)
	}
	if w := f.do(http.MethodPost, ackPath(r), child, struct{}{}); w.Code != http.StatusCreated {
		t.Fatal("ack", w.Code, w.Body.String())
	}
	acked := auditState(t, f, "child", apiv1.PortalQuestionBoxInbox, r.Name)
	if acked.Thread.State != apiv1.InboxStateAcknowledged || acked.Acknowledgement.Status != apiv1.PortalQuestionAckRecorded || acked.Acknowledgement.Actor == nil || acked.Acknowledgement.Actor.PodName != "child" || acked.Acknowledgement.Actor.Kind != "session" || acked.Approval != apiv1.PortalQuestionApprovalNotRecorded || acked.ProviderConsumption != apiv1.PortalQuestionConsumptionNotRecorded || acked.ServedModel != apiv1.PortalQuestionServedModelUnknown {
		t.Fatalf("ack evidence %+v %+v", acked.Thread, acked.Acknowledgement)
	}
	q2 := publish(t, f, child, apiv1.PublishInboxRequest{Project: "sample", Key: "q-2", Text: "SECRET_QUESTION_TEXT", Kind: apiv1.InboxKindQuestion, Recipient: apiv1.TaskClaimOwner{Name: "bystander"}}, http.StatusCreated)
	setQuestionRecipient(t, f, v1alpha1.ModeTask, v1alpha1.PhaseRunning, protocol.AgentExited)
	if got := auditState(t, f, "child", apiv1.PortalQuestionBoxSent, q2.Name); got.Thread.State != apiv1.InboxStateRecipientEnded || got.Thread.WaitingOn != nil {
		t.Fatalf("ended task %+v", got.Thread)
	}
	f.srv.Inbox.Live = failingReplyLookup{f.c}
	if got := auditState(t, f, "child", apiv1.PortalQuestionBoxSent, q.Name); got.Thread.State != apiv1.InboxStateUnknown || got.Acknowledgement.Status != apiv1.PortalQuestionAckUnavailable {
		t.Fatalf("unknown %+v", got.Thread)
	}
}

func TestPortalQuestionAuditEndedTaskVersusResumable(t *testing.T) {
	cases := []struct {
		mode  v1alpha1.SessionMode
		phase v1alpha1.SessionPhase
		agent string
		want  string
	}{
		{v1alpha1.ModeRemote, v1alpha1.PhaseRunning, protocol.AgentExited, apiv1.InboxStateUnanswered},
		{v1alpha1.ModeLocal, v1alpha1.PhaseRunning, protocol.AgentInterrupted, apiv1.InboxStateUnanswered},
		{v1alpha1.ModeTask, v1alpha1.PhaseSuspended, protocol.AgentBusy, apiv1.InboxStateUnanswered},
		{v1alpha1.ModeTask, v1alpha1.PhaseRunning, protocol.AgentExited, apiv1.InboxStateRecipientEnded},
		{v1alpha1.ModeTask, v1alpha1.PhaseRunning, protocol.AgentFailed, apiv1.InboxStateRecipientEnded},
		{v1alpha1.ModeTask, v1alpha1.PhaseRunning, protocol.AgentInterrupted, apiv1.InboxStateRecipientEnded},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode)+"/"+string(tc.phase)+"/"+tc.agent, func(t *testing.T) {
			f, _, child, _ := askReplyFixture(t)
			enableQuestionAuditPortal(t, f, true)
			q := publish(t, f, child, apiv1.PublishInboxRequest{Project: "sample", Key: "q-end", Text: "SECRET_QUESTION_TEXT", Kind: apiv1.InboxKindQuestion, Recipient: apiv1.TaskClaimOwner{Name: "bystander"}}, http.StatusCreated)
			setQuestionRecipient(t, f, tc.mode, tc.phase, tc.agent)
			if got := auditState(t, f, "child", apiv1.PortalQuestionBoxSent, q.Name); got.Thread.State != tc.want {
				t.Fatalf("state %s want %s", got.Thread.State, tc.want)
			}
		})
	}
}

func TestPortalQuestionAuditFencesIdentityProjectAndScope(t *testing.T) {
	f, _, child, _ := askReplyFixture(t)
	enableQuestionAuditPortal(t, f, true)
	q := publish(t, f, child, askParent("q-1", "SECRET_QUESTION_TEXT"), http.StatusCreated)
	foreign := publish(t, f, child, apiv1.PublishInboxRequest{Project: "sample", Key: "q-foreign", Text: "SECRET_QUESTION_TEXT", Kind: apiv1.InboxKindQuestion, Recipient: apiv1.TaskClaimOwner{Name: "bystander"}}, http.StatusCreated)
	plain := publish(t, f, child, inboxRequest(f, "lead", "plain-1"), http.StatusCreated)
	leadPage := auditOf(t, f, "lead", apiv1.PortalQuestionBoxInbox)
	if auditHas(leadPage, foreign.Name) || auditHas(leadPage, plain.Name) || !auditHas(leadPage, q.Name) {
		t.Fatalf("nil scope or plain message leaked: %+v", leadPage.Items)
	}
	for _, item := range leadPage.Items {
		if item.Kind != apiv1.InboxKindQuestion && item.Kind != apiv1.InboxKindReply {
			t.Fatal("plain kind", item.Kind)
		}
	}
	f.srv.Projects = nil
	s := f.session("child")
	s.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: `{"project":"other"}`}
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if page := auditOf(t, f, "child", apiv1.PortalQuestionBoxSent); !auditHas(page, q.Name) || page.Project != "sample" {
		t.Fatal("live identity lost after snapshot rewrite", page.Project)
	}
	if w := f.do(http.MethodGet, questionAuditURL("child", string(f.session("child").UID), apiv1.PortalQuestionBoxSent, "other"), "tok-portal", nil); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "SECRET_QUESTION_TEXT") {
		t.Fatalf("foreign project %d %s", w.Code, w.Body.String())
	}
	live := f.srv.Live
	f.srv.Live = &questionFence{Reader: live, name: "child", mode: "delete"}
	if w := f.do(http.MethodGet, questionAuditURL("child", string(f.session("child").UID), apiv1.PortalQuestionBoxSent), "tok-portal", nil); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "portal-question-audit.v1") {
		t.Fatalf("deletion during derivation %d %s", w.Code, w.Body.String())
	}
	f.srv.Live = &questionFence{Reader: live, name: "child", mode: "replace"}
	if w := f.do(http.MethodGet, questionAuditURL("child", string(f.session("child").UID), apiv1.PortalQuestionBoxSent), "tok-portal", nil); w.Code != http.StatusNotFound {
		t.Fatalf("replacement during derivation %d %s", w.Code, w.Body.String())
	}
}

func TestPortalQuestionAuditAckMismatchReducesEvidence(t *testing.T) {
	f, lead, child, _ := askReplyFixture(t)
	enableQuestionAuditPortal(t, f, true)
	q := publish(t, f, child, askParent("q-1", "SECRET_QUESTION_TEXT"), http.StatusCreated)
	r := publish(t, f, lead, replyTo(q, "r-1", "SECRET_REPLY_TEXT"), http.StatusCreated)
	answered := auditState(t, f, "child", apiv1.PortalQuestionBoxInbox, r.Name)
	if answered.Thread.State != apiv1.InboxStateAnswered || answered.Acknowledgement.Status != apiv1.PortalQuestionAckUnavailable || answered.Acknowledgement.Actor != nil {
		t.Fatalf("absent ack %+v", answered.Acknowledgement)
	}
	if got := f.srv.questionAuditAck(context.Background(), "sample", apiv1.InboxMessageRef{Name: r.Name, UID: "not-the-uid"}); got.Status != apiv1.PortalQuestionAckUnavailable || got.Actor != nil {
		t.Fatalf("substituted ref %+v", got)
	}
	if w := f.do(http.MethodPost, ackPath(r), child, struct{}{}); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	stored, err := f.srv.Inbox.Get(context.Background(), r.Name)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := f.srv.Inbox.ReadAck(context.Background(), stored)
	if err != nil {
		t.Fatal(err)
	}
	ack.Spec.Actor.PodName = "claimed-origin"
	if err := f.c.Update(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
	reduced := auditState(t, f, "child", apiv1.PortalQuestionBoxInbox, r.Name)
	if reduced.Thread.State != apiv1.InboxStateUnknown || reduced.Acknowledgement.Status != apiv1.PortalQuestionAckUnavailable || reduced.Acknowledgement.Actor != nil {
		t.Fatalf("claimed origin stayed trusted %+v %+v", reduced.Thread, reduced.Acknowledgement)
	}
	var listed coord.InboxReadAck
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ack.Namespace, Name: ack.Name}, &listed); err != nil {
		t.Fatal(err)
	}
	listed.Spec.Actor.PodName = "child"
	listed.Spec.MessageUID = "crossed-uid"
	if err := f.c.Update(context.Background(), &listed); err != nil {
		t.Fatal(err)
	}
	crossed := auditState(t, f, "child", apiv1.PortalQuestionBoxInbox, r.Name)
	if crossed.Thread.State == apiv1.InboxStateAcknowledged || crossed.Acknowledgement.Status == apiv1.PortalQuestionAckRecorded || strings.Contains(crossed.Name, "crossed-uid") {
		t.Fatalf("crossed ack %+v %+v", crossed.Thread, crossed.Acknowledgement)
	}
}

func TestPortalQuestionAuditOnePageOmitsPlain(t *testing.T) {
	if apiv1.PortalQuestionAuditDefaultLimit != 20 {
		t.Fatalf("default one-page budget changed: %d", apiv1.PortalQuestionAuditDefaultLimit)
	}
	f, _, child, _ := askReplyFixture(t)
	enableQuestionAuditPortal(t, f, true)
	first := publish(t, f, child, askParent("q-1", "SECRET_QUESTION_TEXT"), http.StatusCreated)
	second := publish(t, f, child, askParent("q-2", "SECRET_QUESTION_TEXT"), http.StatusCreated)
	plain := publish(t, f, child, inboxRequest(f, "lead", "plain-1"), http.StatusCreated)
	// The fixture client returns every match and ignores Limit and Continue.
	// ListSent refuses a page longer than the requested limit, which is the
	// production bound. Page here the same way the inbox suite's pagedFake does.
	var lists []questionAuditListPage
	f.srv.Inbox.Live = questionAuditPagedLive(t, f.srv.Inbox.Live, &lists)
	s := f.session("child")
	values := url.Values{}
	values.Set("box", apiv1.PortalQuestionBoxSent)
	values.Set("limit", "1")
	values.Set("project", "sample")
	values.Set("session", s.Name)
	values.Set("uid", string(s.UID))
	cursor := ""
	seen := map[string]int{}
	for pageN := 0; ; pageN++ {
		if pageN > 4 {
			t.Fatal("continuation did not end")
		}
		if cursor == "" {
			values.Del("continue")
		} else {
			values.Set("continue", cursor)
		}
		before := len(lists)
		w := f.do(http.MethodGet, apiv1.PortalQuestionAuditPath+"?"+values.Encode(), "tok-portal", nil)
		body := w.Body.String()
		if w.Code != http.StatusOK {
			t.Fatalf("page %d %s", w.Code, body)
		}
		if len(lists) != before+1 {
			t.Fatalf("one-page budget made %d lists", len(lists)-before)
		}
		call := lists[before]
		if call.limit != 1 || call.cursor != cursor || call.count != 1 {
			t.Fatalf("store page %+v", call)
		}
		assertQuestionAuditAllowlist(t, body)
		for _, secret := range []string{"SECRET_QUESTION_TEXT", "PRIVATE_INBOX_EXACT_TEXT", plain.Name, "plain-1", "q-1", "q-2", "claude-opus-5-5", "requestedModel"} {
			if strings.Contains(body, secret) {
				t.Fatalf("page carried %s", secret)
			}
		}
		page := decode[apiv1.PortalQuestionAudit](t, w)
		if page.Limit != 1 || len(page.Items) > 1 || page.Truncated != (page.NextCursor != "") || page.NextCursor != call.next || page.ServedModel != apiv1.PortalQuestionServedModelUnknown {
			t.Fatalf("bounded page %+v store %+v", page, call)
		}
		if call.name == plain.Name {
			if len(page.Items) != 0 {
				t.Fatalf("plain message returned %+v", page.Items)
			}
		} else if len(page.Items) != 1 || page.Items[0].Name != call.name || page.Items[0].ServedModel != apiv1.PortalQuestionServedModelUnknown || page.Items[0].Kind != apiv1.InboxKindQuestion {
			t.Fatalf("question page %+v store %+v", page.Items, call)
		}
		for _, item := range page.Items {
			seen[item.Name]++
		}
		cursor = page.NextCursor
		if !page.Truncated {
			break
		}
	}
	if len(lists) != 3 || lists[0].name != plain.Name || lists[0].next == "" || seen[first.Name] != 1 || seen[second.Name] != 1 || len(seen) != 2 {
		t.Fatalf("pages %d covered %+v lists %+v", len(lists), seen, lists)
	}
}

func enableQuestionAuditPortal(t *testing.T, f *fixture, on bool) {
	t.Helper()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: types.UID("portal-pod-uid")}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	f.srv.Policy.PortalReaderEnabled = true
	f.srv.PortalQuestionAudit = on
	if on {
		f.srv.PortalQuestionProjects = []string{"sample"}
	}
	f.h = f.srv.Handler()
}

func questionAuditURL(name, uid, box string, project ...string) string {
	chosen := "sample"
	if len(project) == 1 {
		chosen = project[0]
	}
	values := url.Values{}
	values.Set("box", box)
	values.Set("limit", "20")
	values.Set("project", chosen)
	values.Set("session", name)
	values.Set("uid", uid)
	return apiv1.PortalQuestionAuditPath + "?" + values.Encode()
}

func auditOf(t *testing.T, f *fixture, name, box string) apiv1.PortalQuestionAudit {
	t.Helper()
	s := f.session(name)
	w := f.do(http.MethodGet, questionAuditURL(s.Name, string(s.UID), box), "tok-portal", nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "SECRET_QUESTION_TEXT") || strings.Contains(w.Body.String(), "SECRET_REPLY_TEXT") || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") || strings.Contains(w.Body.String(), "PRIVATE_PROMPT") || strings.Contains(w.Body.String(), "claude-opus-5-5") {
		t.Fatalf("audit %s %s: %d %s", name, box, w.Code, w.Body.String())
	}
	page := decode[apiv1.PortalQuestionAudit](t, w)
	if page.Version != apiv1.PortalQuestionAuditVersion || page.Box != box || page.Session.Name != name || page.Session.UID != string(s.UID) {
		t.Fatalf("echo %+v", page)
	}
	return page
}

func auditNamed(t *testing.T, page apiv1.PortalQuestionAudit, name string) apiv1.PortalQuestionAuditItem {
	t.Helper()
	for _, item := range page.Items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("%s missing from %d items", name, len(page.Items))
	return apiv1.PortalQuestionAuditItem{}
}

func auditState(t *testing.T, f *fixture, session, box, name string) apiv1.PortalQuestionAuditItem {
	t.Helper()
	return auditNamed(t, auditOf(t, f, session, box), name)
}

func auditHas(page apiv1.PortalQuestionAudit, name string) bool {
	for _, item := range page.Items {
		if item.Name == name {
			return true
		}
	}
	return false
}

type questionAuditListPage struct {
	limit, count int64
	cursor, next string
	name         string
}

// questionAuditPagedLive gives the fixture client the Limit and Continue it
// ignores, matching inbox pagedFake. Name order keeps each continuation stable.
func questionAuditPagedLive(t *testing.T, live client.Reader, pages *[]questionAuditListPage) client.Reader {
	t.Helper()
	base, ok := live.(client.WithWatch)
	if !ok {
		t.Fatal("question audit pager needs the fixture client")
	}
	return interceptor.NewClient(base, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		var o client.ListOptions
		o.ApplyOptions(opts)
		if err := c.List(ctx, list, &client.ListOptions{Namespace: o.Namespace, LabelSelector: o.LabelSelector}); err != nil {
			return err
		}
		var cut func(start, end int)
		var total int
		var contp *string
		record := false
		switch l := list.(type) {
		case *coord.InboxMessageList:
			// Plain objects sort first so a limit of one always yields an
			// empty truncated page before the questions. Name order is the tie break.
			sort.Slice(l.Items, func(i, j int) bool {
				plainI := l.Items[i].Spec.Kind != inbox.KindQuestion && l.Items[i].Spec.Kind != inbox.KindReply
				plainJ := l.Items[j].Spec.Kind != inbox.KindQuestion && l.Items[j].Spec.Kind != inbox.KindReply
				if plainI != plainJ {
					return plainI
				}
				return l.Items[i].Name < l.Items[j].Name
			})
			total, contp = len(l.Items), &l.Continue
			cut = func(start, end int) { l.Items = l.Items[start:end] }
			record = true
		case *metav1.PartialObjectMetadataList:
			total, contp = len(l.Items), &l.Continue
			cut = func(start, end int) { l.Items = l.Items[start:end] }
		default:
			return nil
		}
		if o.Limit == 0 {
			return nil
		}
		start, _ := strconv.Atoi(o.Continue)
		if start < 0 || start > total {
			start = total
		}
		end := min(start+int(o.Limit), total)
		*contp = ""
		if end < total {
			*contp = strconv.Itoa(end)
		}
		cut(start, end)
		if record && pages != nil {
			name := ""
			if messages, ok := list.(*coord.InboxMessageList); ok && len(messages.Items) == 1 {
				name = messages.Items[0].Name
			}
			*pages = append(*pages, questionAuditListPage{limit: o.Limit, count: int64(end - start), cursor: o.Continue, next: *contp, name: name})
		}
		return nil
	}})
}

func assertQuestionAuditAllowlist(t *testing.T, body string) {
	t.Helper()
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &obj) != nil {
		t.Fatal("audit json")
	}
	allowed := map[string]struct{}{"version": {}, "project": {}, "session": {}, "box": {}, "limit": {}, "truncated": {}, "nextCursor": {}, "origin": {}, "approval": {}, "providerConsumption": {}, "servedModel": {}, "items": {}}
	for key := range obj {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("audit envelope carried %s", key)
		}
	}
	for _, key := range []string{"version", "project", "session", "box", "limit", "truncated", "origin", "approval", "providerConsumption", "servedModel", "items"} {
		if _, ok := obj[key]; !ok {
			t.Fatalf("audit envelope lost %s", key)
		}
	}
	var items []json.RawMessage
	if json.Unmarshal(obj["items"], &items) != nil {
		t.Fatal("audit items")
	}
	itemKeys := map[string]struct{}{"name": {}, "uid": {}, "kind": {}, "inReplyTo": {}, "sender": {}, "recipient": {}, "actor": {}, "provenance": {}, "createdAt": {}, "origin": {}, "approval": {}, "providerConsumption": {}, "servedModel": {}, "thread": {}, "acknowledgement": {}}
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			t.Fatal("audit item")
		}
		for key := range item {
			if _, ok := itemKeys[key]; !ok {
				t.Fatalf("audit item carried %s", key)
			}
		}
	}
	var walked any
	if json.Unmarshal([]byte(body), &walked) != nil {
		t.Fatal("audit json")
	}
	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			for _, key := range []string{"text", "key", "prompt", "history", "requestedModel", "servedModels", "cursor"} {
				if _, ok := typed[key]; ok {
					t.Fatalf("audit carried %s", key)
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(walked)
}

func setQuestionRecipient(t *testing.T, f *fixture, mode v1alpha1.SessionMode, phase v1alpha1.SessionPhase, agent string) {
	t.Helper()
	b := f.session("bystander")
	b.Spec.Mode = mode
	if mode == v1alpha1.ModeTask {
		b.Spec.Prompt = "p"
	} else {
		b.Spec.Prompt = ""
	}
	if err := f.c.Update(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	b = f.session("bystander")
	if b.Status.Agent == nil {
		b.Status.Agent = &v1alpha1.AgentStatus{}
	}
	b.Status.Phase = phase
	b.Status.Agent.Status = agent
	if err := f.c.Status().Update(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

type questionFence struct {
	client.Reader
	name string
	mode string
	n    int
}

func (g *questionFence) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*v1alpha1.AgentSession); ok && key.Name == g.name {
		g.n++
		if g.n > 1 && g.mode == "delete" {
			return errors.New("deleted during question audit")
		}
	}
	err := g.Reader.Get(ctx, key, obj, opts...)
	if err == nil && g.n > 1 && g.mode == "replace" && key.Name == g.name {
		if sess, ok := obj.(*v1alpha1.AgentSession); ok {
			sess.UID = types.UID("replaced-uid")
		}
	}
	return err
}
