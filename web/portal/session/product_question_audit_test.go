package session

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

func TestQuestionAuditSelectedReadsTwoBoxes(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	inbox := auditQuestionItem(apiv1.InboxStateUnanswered)
	sent := auditReplyItem()
	cursor := "cursor-secret-value"
	script := &questionAuditScript{
		inbox: auditPageBytes(t, apiv1.PortalQuestionBoxInbox, []apiv1.PortalQuestionAuditItem{inbox}, false, ""),
		sent:  auditPageBytes(t, apiv1.PortalQuestionBoxSent, []apiv1.PortalQuestionAuditItem{sent}, true, cursor),
	}
	src := questionAuditSource(t, now, script)
	src.UseQuestionAudit("sample")
	page, err := src.Read(t.Context(), portal.ProductFilter{Session: "uid-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(script.queries) != 2 {
		t.Fatalf("queries %v", script.queries)
	}
	seen := map[string]bool{}
	for _, raw := range script.queries {
		q, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		if q.Get("continue") != "" || q.Get("project") != "sample" || q.Get("session") != "agent" || q.Get("uid") != "uid-agent" || q.Get("limit") != "20" || len(q) != 5 {
			t.Fatalf("query %s", raw)
		}
		seen[q.Get("box")] = true
	}
	if !seen[apiv1.PortalQuestionBoxInbox] || !seen[apiv1.PortalQuestionBoxSent] {
		t.Fatalf("boxes %v", script.queries)
	}
	section := questionAuditSectionOf(t, page, "agent")
	if questionAuditSectionCount(page, "other") != 0 {
		t.Fatal("unselected row gained a question audit")
	}
	fields := map[string]string{}
	for _, f := range section.Fields {
		fields[f.Label] = f.Value
	}
	if fields["Origin"] != apiv1.PortalQuestionOriginUnknown || fields["Approval"] != apiv1.PortalQuestionApprovalNotRecorded || fields["Provider consumption"] != apiv1.PortalQuestionConsumptionNotRecorded || fields["Served model"] != apiv1.PortalQuestionServedModelUnknown {
		t.Fatalf("fixed fields %+v", fields)
	}
	if !strings.Contains(fields["Inbox 1"], apiv1.InboxStateUnanswered) || !strings.Contains(fields["Inbox 1"], inbox.Name) || !strings.Contains(fields["Inbox 1"], "actor child") {
		t.Fatalf("inbox line %q", fields["Inbox 1"])
	}
	if !strings.Contains(fields["Sent 1"], apiv1.InboxStateAnswered) || !strings.Contains(fields["Sent 1"], "reply to "+sent.InReplyTo.Name) {
		t.Fatalf("sent line %q", fields["Sent 1"])
	}
	body, err := portal.RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(body)
	// Session detail keeps the requested model. The audit section is a separate
	// surface: it serves Unknown and carries no requested or served inference.
	if !strings.Contains(rendered, "claude-opus-5-5") || !strings.Contains(rendered, "Requested model") {
		t.Fatal("session detail dropped the requested model")
	}
	for _, secret := range []string{cursor, "SECRET_QUESTION_TEXT", sent.Provenance.GitHub} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("render included %s", secret)
		}
	}
	panel := questionAuditPanelHTML(t, rendered)
	for _, secret := range []string{cursor, "SECRET_QUESTION_TEXT", sent.Provenance.GitHub, "claude-opus-5-5", "Requested model", "servedModels"} {
		if strings.Contains(panel, secret) {
			t.Fatalf("audit section included %s", secret)
		}
	}
	for _, allowed := range []string{"<dt>Origin</dt><dd>Unknown</dd>", "<dt>Approval</dt><dd>not recorded</dd>", "<dt>Provider consumption</dt><dd>not recorded</dd>", "<dt>Served model</dt><dd>Unknown</dd>"} {
		if !strings.Contains(panel, allowed) {
			t.Fatalf("audit section lost %s", allowed)
		}
	}
	base := questionAuditSource(t, now, &questionAuditScript{
		inbox: script.inbox,
		sent:  script.sent,
	})
	before, err := base.Read(t.Context(), portal.ProductFilter{Session: "uid-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if questionAuditSignature(before) != questionAuditSignature(page) {
		t.Fatalf("inventory changed\nbefore %s\nafter %s", questionAuditSignature(before), questionAuditSignature(page))
	}
}

func TestQuestionAuditCatalogProjectIsNotRepoAlias(t *testing.T) {
	const catalogProject = "sample"
	const repoAlias = "alias"
	if catalogProject == repoAlias {
		t.Fatal("catalog project collapsed into the repository alias")
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	script := &questionAuditScript{
		inventoryRepo: repoAlias,
		inbox:         auditPageBytes(t, apiv1.PortalQuestionBoxInbox, []apiv1.PortalQuestionAuditItem{auditQuestionItem(apiv1.InboxStateUnanswered)}, false, ""),
		sent:          auditPageBytes(t, apiv1.PortalQuestionBoxSent, nil, false, ""),
	}
	src := questionAuditSource(t, now, script)
	src.UseQuestionAudit(catalogProject)
	page, err := src.Read(t.Context(), portal.ProductFilter{Session: "uid-agent", Project: repoAlias})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(t, page, "agent")
	if row.Kicker != repoAlias {
		t.Fatalf("selected row repo %q", row.Kicker)
	}
	projectField := ""
	for _, field := range row.Context {
		if field.Label == "Project" {
			projectField = field.Value
		}
	}
	if projectField != repoAlias || page.Filter.Project != repoAlias {
		t.Fatalf("inventory repo %q filter %q", projectField, page.Filter.Project)
	}
	if len(script.queries) != 2 {
		t.Fatalf("queries %v", script.queries)
	}
	for _, raw := range script.queries {
		q, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		if q.Get("project") != catalogProject || strings.Contains(raw, repoAlias) {
			t.Fatalf("audit request used the repo alias %s", raw)
		}
	}
	section := questionAuditSectionOf(t, page, "agent")
	fields := map[string]string{}
	for _, f := range section.Fields {
		fields[f.Label] = f.Value
	}
	if fields["Served model"] != apiv1.PortalQuestionServedModelUnknown {
		t.Fatalf("served model %+v", fields)
	}
	if questionAuditSectionCount(page, "other") != 0 {
		t.Fatal("unselected row gained a question audit")
	}
}

func TestQuestionAuditNoSelectionOrDisabledMakesNoRequest(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	script := &questionAuditScript{}
	disabled := questionAuditSource(t, now, script)
	if _, err := disabled.Read(t.Context(), portal.ProductFilter{Session: "uid-agent"}); err != nil {
		t.Fatal(err)
	}
	enabled := questionAuditSource(t, now, script)
	enabled.UseQuestionAudit("sample")
	if _, err := enabled.Read(t.Context(), portal.ProductFilter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := enabled.Read(t.Context(), portal.ProductFilter{View: "usage"}); err != nil {
		t.Fatal(err)
	}
	if len(script.queries) != 0 {
		t.Fatalf("audit calls %v", script.queries)
	}
}

func TestQuestionAuditPanelFailuresPreserveInventory(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	good := auditPageBytes(t, apiv1.PortalQuestionBoxInbox, []apiv1.PortalQuestionAuditItem{auditQuestionItem(apiv1.InboxStateUnanswered)}, false, "")
	substituted := auditQuestionItem(apiv1.InboxStateUnanswered)
	substituted.Thread = copyAuditThread(substituted.Thread)
	substituted.Thread.Question.Name = auditMessageName('e', 'f')
	echo := auditPageBytes(t, apiv1.PortalQuestionBoxInbox, nil, false, "")
	var echoed apiv1.PortalQuestionAudit
	if json.Unmarshal(echo, &echoed) != nil {
		t.Fatal("echo fixture")
	}
	echoed.Project = "other"
	echoBody, err := json.Marshal(echoed)
	if err != nil {
		t.Fatal(err)
	}
	wrongLimit := auditPageBytes(t, apiv1.PortalQuestionBoxInbox, nil, false, "")
	var limited apiv1.PortalQuestionAudit
	if json.Unmarshal(wrongLimit, &limited) != nil {
		t.Fatal("limit fixture")
	}
	limited.Limit = 50
	limitBody, err := json.Marshal(limited)
	if err != nil {
		t.Fatal(err)
	}
	textBody := []byte(strings.Replace(string(good), `"kind":"question"`, `"text":"SECRET_QUESTION_TEXT","kind":"question"`, 1))
	acked := auditQuestionItem(apiv1.InboxStateAcknowledged)
	at := acked.CreatedAt.Add(time.Second)
	acked.Thread = copyAuditThread(acked.Thread)
	acked.Thread.State = apiv1.InboxStateAcknowledged
	acked.Thread.WaitingOn = nil
	acked.Acknowledgement = apiv1.PortalQuestionAuditAck{Status: apiv1.PortalQuestionAckRecorded, Actor: &apiv1.TaskClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env", PodName: "claimed-origin", PodUID: "pod-claimed"}, CreatedAt: &at}
	cases := []struct {
		name         string
		script       questionAuditScript
		status       string
		secret       string
		calls        int
		missing      bool
		cancelParent bool
	}{
		{name: "substituted ref", script: questionAuditScript{inbox: auditPageBytes(t, apiv1.PortalQuestionBoxInbox, []apiv1.PortalQuestionAuditItem{substituted}, false, "")}, status: "Unavailable", secret: auditMessageName('e', 'f'), calls: 1},
		{name: "echo", script: questionAuditScript{inbox: echoBody}, status: "Unavailable", secret: "other", calls: 1},
		{name: "wrong limit", script: questionAuditScript{inbox: limitBody}, status: "Unavailable", calls: 1},
		{name: "text field", script: questionAuditScript{inbox: textBody}, status: "Unavailable", secret: "SECRET_QUESTION_TEXT", calls: 1},
		{name: "ack actor", script: questionAuditScript{inbox: auditPageBytes(t, apiv1.PortalQuestionBoxInbox, []apiv1.PortalQuestionAuditItem{acked}, false, "")}, status: "Unavailable", secret: "claimed-origin", calls: 1},
		{name: "oversize", script: questionAuditScript{inboxStatus: http.StatusOK, inbox: []byte(strings.Repeat("x", apiv1.PortalQuestionAuditMaxBytes+1))}, status: "Unavailable", calls: 1},
		{name: "malformed", script: questionAuditScript{inboxStatus: http.StatusOK, inbox: []byte("{")}, status: "Unavailable", calls: 1},
		{name: "forbidden", script: questionAuditScript{inboxStatus: http.StatusForbidden}, status: "Unavailable", calls: 1},
		{name: "sent missing", script: questionAuditScript{inbox: good, sentStatus: http.StatusNotFound}, status: "Unavailable", calls: 2},
		{name: "timeout", script: questionAuditScript{hang: true}, status: "Unavailable", calls: 1},
		{name: "parent deadline during panel", script: questionAuditScript{hang: true}, status: "Unavailable", calls: 1, cancelParent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.script.hang && !tc.cancelParent {
				previous := questionAuditTimeout
				questionAuditTimeout = 30 * time.Millisecond
				t.Cleanup(func() { questionAuditTimeout = previous })
			}
			if tc.cancelParent {
				if sessionReadTimeout != 12*time.Second {
					t.Fatal("inventory timeout contract changed")
				}
				previous := questionAuditTimeout
				questionAuditTimeout = 5 * time.Second
				t.Cleanup(func() { questionAuditTimeout = previous })
			}
			baseScript := &questionAuditScript{inbox: good, sent: auditPageBytes(t, apiv1.PortalQuestionBoxSent, nil, false, "")}
			base := questionAuditSource(t, now, baseScript)
			before, err := base.Read(t.Context(), portal.ProductFilter{Session: "uid-agent"})
			if err != nil {
				t.Fatal(err)
			}
			script := tc.script
			src := questionAuditSource(t, now, &script)
			src.UseQuestionAudit("sample")
			readCtx := t.Context()
			var page portal.ProductPage
			if tc.cancelParent {
				entered := make(chan struct{})
				stopped := make(chan struct{})
				script.entered = entered
				script.stopped = stopped
				var cancel context.CancelFunc
				readCtx, cancel = context.WithCancel(t.Context())
				t.Cleanup(cancel)
				go func() {
					<-entered
					cancel()
				}()
				done := make(chan struct{})
				go func() {
					page, err = src.Read(readCtx, portal.ProductFilter{Session: "uid-agent"})
					close(done)
				}()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					cancel()
					t.Fatal("optional panel kept working after the request was cancelled")
				}
				select {
				case <-stopped:
				case <-time.After(time.Second):
					t.Fatal("panel request kept running after the client returned")
				}
			} else {
				page, err = src.Read(readCtx, portal.ProductFilter{Session: "uid-agent"})
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.cancelParent {
				if page.ListCount == "" || len(page.Groups) == 0 || findRow(t, page, "agent").Title != "agent" {
					t.Fatal("inventory, group, count, or card dropped")
				}
			}
			if len(script.queries) != tc.calls {
				t.Fatalf("calls %d %v", len(script.queries), script.queries)
			}
			if questionAuditSignature(before) != questionAuditSignature(page) {
				t.Fatalf("inventory changed\n%s\n%s", questionAuditSignature(before), questionAuditSignature(page))
			}
			section := questionAuditSectionOf(t, page, "agent")
			if len(section.States) != 1 || section.States[0].Status.Text != tc.status || len(section.Fields) != 0 {
				t.Fatalf("panel %+v", section)
			}
			if tc.secret != "" && (strings.Contains(section.States[0].Text, tc.secret) || strings.Contains(section.Note, tc.secret)) {
				t.Fatalf("panel showed %s", tc.secret)
			}
			if questionAuditSectionCount(page, "other") != 0 {
				t.Fatal("other row changed")
			}
		})
	}
}

func TestQuestionAuditEmptyMissingUnknownAndOlderOperator(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	emptyInbox := auditPageBytes(t, apiv1.PortalQuestionBoxInbox, nil, false, "")
	emptySent := auditPageBytes(t, apiv1.PortalQuestionBoxSent, nil, false, "")
	empty := &questionAuditScript{inbox: emptyInbox, sent: emptySent}
	src := questionAuditSource(t, now, empty)
	src.UseQuestionAudit("sample")
	page, err := src.Read(t.Context(), portal.ProductFilter{Session: "uid-agent"})
	if err != nil {
		t.Fatal(err)
	}
	section := questionAuditSectionOf(t, page, "agent")
	if !questionAuditHasState(section, "Empty", "not a missing route") || questionAuditHasState(section, "Missing", "") {
		t.Fatalf("empty %+v", section.States)
	}
	unknownBody := auditPageBytes(t, apiv1.PortalQuestionBoxInbox, []apiv1.PortalQuestionAuditItem{auditQuestionItem(apiv1.InboxStateUnknown)}, false, "")
	unknown := &questionAuditScript{inbox: unknownBody, sent: emptySent}
	src = questionAuditSource(t, now, unknown)
	src.UseQuestionAudit("sample")
	page, err = src.Read(t.Context(), portal.ProductFilter{Session: "uid-agent"})
	if err != nil {
		t.Fatal(err)
	}
	section = questionAuditSectionOf(t, page, "agent")
	if !questionAuditHasState(section, "Unknown", "not unanswered") || questionAuditHasState(section, "Empty", "Inbox") {
		t.Fatalf("unknown %+v", section.States)
	}
	truncated := &questionAuditScript{inbox: auditPageBytes(t, apiv1.PortalQuestionBoxInbox, nil, true, "plain-page-cursor"), sent: emptySent}
	src = questionAuditSource(t, now, truncated)
	src.UseQuestionAudit("sample")
	page, err = src.Read(t.Context(), portal.ProductFilter{Session: "uid-agent"})
	if err != nil {
		t.Fatal(err)
	}
	section = questionAuditSectionOf(t, page, "agent")
	if !questionAuditHasState(section, "Truncated", "plain messages were omitted") || questionAuditHasState(section, "Empty", "Inbox") {
		t.Fatalf("truncated %+v", section.States)
	}
	body, err := portal.RenderProduct(page)
	if err != nil || strings.Contains(string(body), "plain-page-cursor") {
		t.Fatalf("cursor rendered: %v", err)
	}
	for _, raw := range truncated.queries {
		if strings.Contains(raw, "continue") {
			t.Fatalf("consumer followed a cursor %s", raw)
		}
	}
	missing := &questionAuditScript{inboxStatus: http.StatusNotFound}
	src = questionAuditSource(t, now, missing)
	src.UseQuestionAudit("sample")
	page, err = src.Read(t.Context(), portal.ProductFilter{Session: "uid-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.queries) != 1 {
		t.Fatalf("older operator caused %d calls", len(missing.queries))
	}
	section = questionAuditSectionOf(t, page, "agent")
	if !questionAuditHasState(section, "Missing", "not an empty audit") || questionAuditHasState(section, "Empty", "") || questionAuditHasState(section, "Unavailable", "") {
		t.Fatalf("missing %+v", section.States)
	}
}

func TestQuestionAuditLeavesInventoryDecoderUnchanged(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	body := activityPage(activityRow("agent", unknownActivity))
	if _, err := decodeSessionPage([]byte(body), now); err != nil {
		t.Fatal(err)
	}
	extra := strings.Replace(body, `"sessions"`, `"questionAudit":[],"sessions"`, 1)
	if _, err := decodeSessionPage([]byte(extra), now); err == nil {
		t.Fatal("inventory accepted a question audit field")
	}
}

type questionAuditScript struct {
	mu            sync.Mutex
	queries       []string
	inbox         []byte
	sent          []byte
	inboxStatus   int
	sentStatus    int
	hang          bool
	inventoryRepo string
	entered       chan struct{}
	stopped       chan struct{}
}

func (s *questionAuditScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/sessions" {
		w.Header().Set("Content-Type", "application/json")
		agent := activityRow("agent", unknownActivity)
		other := activityRow("other", unknownActivity)
		if s.inventoryRepo != "" {
			agent = strings.Replace(agent, `"repo":"dev-env"`, `"repo":"`+s.inventoryRepo+`"`, 1)
			other = strings.Replace(other, `"repo":"dev-env"`, `"repo":"`+s.inventoryRepo+`"`, 1)
		}
		_, _ = io.WriteString(w, activityPage(agent, other))
		return
	}
	if r.URL.Path != apiv1.PortalQuestionAuditPath {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.queries = append(s.queries, r.URL.RawQuery)
	s.mu.Unlock()
	if s.entered != nil {
		close(s.entered)
		s.entered = nil
	}
	if s.hang {
		<-r.Context().Done()
		if s.stopped != nil {
			close(s.stopped)
		}
		return
	}
	status, body := http.StatusOK, s.inbox
	if r.URL.Query().Get("box") == apiv1.PortalQuestionBoxSent {
		status, body = http.StatusOK, s.sent
		if s.sentStatus != 0 {
			status = s.sentStatus
		}
	} else if s.inboxStatus != 0 {
		status = s.inboxStatus
	}
	if status == http.StatusNotFound {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func questionAuditSource(t *testing.T, now time.Time, handler http.Handler) *SessionSource {
	t.Helper()
	cert, ca := mintNamed(t, operatorPortalHost)
	srv := serveTLS(t, cert, handler)
	token := filepath.Join(t.TempDir(), "operator")
	if err := os.WriteFile(token, []byte(fixtureOperatorToken(apiv1.TokenAudience, now.Add(time.Hour))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := newSessionSource(token, ca, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: srv}))
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func auditMessageName(a, b byte) string {
	return "message." + strings.Repeat(string(a), 32) + "." + strings.Repeat(string(b), 32)
}

func auditQuestionItem(state string) apiv1.PortalQuestionAuditItem {
	name := auditMessageName('a', 'b')
	item := apiv1.PortalQuestionAuditItem{
		Name: name, UID: "uid-q1", Kind: apiv1.InboxKindQuestion,
		Sender: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "child", UID: "uid-child"}, Recipient: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "lead", UID: "uid-lead"},
		Actor:      apiv1.TaskClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env", PodName: "child", PodUID: "pod-child"},
		Provenance: auditProvenance(), CreatedAt: time.Date(2026, 10, 25, 11, 0, 0, 0, time.UTC),
		Origin: apiv1.PortalQuestionOriginUnknown, Approval: apiv1.PortalQuestionApprovalNotRecorded,
		ProviderConsumption: apiv1.PortalQuestionConsumptionNotRecorded, ServedModel: apiv1.PortalQuestionServedModelUnknown,
		Thread: &apiv1.InboxThread{State: state, Question: apiv1.InboxMessageRef{Name: name, UID: "uid-q1"}, StaleAfterSeconds: apiv1.PortalQuestionAuditStaleAfterSeconds, ObservedAt: time.Date(2026, 10, 25, 11, 30, 0, 0, time.UTC),
			WaitingOn: &apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "lead", UID: "uid-lead"}},
		Acknowledgement: apiv1.PortalQuestionAuditAck{Status: apiv1.PortalQuestionAckUnavailable},
	}
	if state != apiv1.InboxStateUnanswered && state != apiv1.InboxStateStale {
		item.Thread.WaitingOn = nil
	}
	return item
}

func auditReplyItem() apiv1.PortalQuestionAuditItem {
	question, reply := auditMessageName('a', 'b'), auditMessageName('c', 'd')
	return apiv1.PortalQuestionAuditItem{
		Name: reply, UID: "uid-r1", Kind: apiv1.InboxKindReply,
		InReplyTo: &apiv1.InboxMessageRef{Name: question, UID: "uid-q1"},
		Sender:    apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "lead", UID: "uid-lead"}, Recipient: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "child", UID: "uid-child"},
		Actor:      apiv1.TaskClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env", PodName: "lead", PodUID: "pod-lead"},
		Provenance: auditProvenance(), CreatedAt: time.Date(2026, 10, 25, 11, 5, 0, 0, time.UTC),
		Origin: apiv1.PortalQuestionOriginUnknown, Approval: apiv1.PortalQuestionApprovalNotRecorded,
		ProviderConsumption: apiv1.PortalQuestionConsumptionNotRecorded, ServedModel: apiv1.PortalQuestionServedModelUnknown,
		Thread:          &apiv1.InboxThread{State: apiv1.InboxStateAnswered, Question: apiv1.InboxMessageRef{Name: question, UID: "uid-q1"}, Reply: &apiv1.InboxMessageRef{Name: reply, UID: "uid-r1"}, StaleAfterSeconds: apiv1.PortalQuestionAuditStaleAfterSeconds, ObservedAt: time.Date(2026, 10, 25, 11, 30, 0, 0, time.UTC)},
		Acknowledgement: apiv1.PortalQuestionAuditAck{Status: apiv1.PortalQuestionAckUnavailable},
	}
}

func auditProvenance() apiv1.TaskClaimProvenance {
	return apiv1.TaskClaimProvenance{CatalogRevision: strings.Repeat("e", 64), RulesRevision: strings.Repeat("f", 64), GitHub: "thaynes43/actual-source", DefaultBranch: "stable", InitialPinState: "Unknown"}
}

func auditPageBytes(t *testing.T, box string, items []apiv1.PortalQuestionAuditItem, truncated bool, cursor string) []byte {
	t.Helper()
	if items == nil {
		items = []apiv1.PortalQuestionAuditItem{}
	}
	page := apiv1.PortalQuestionAudit{
		Version: apiv1.PortalQuestionAuditVersion, Project: "sample",
		Session: apiv1.PortalQuestionAuditIdentity{Namespace: "dev-agents", Name: "agent", UID: "uid-agent"},
		Box:     box, Limit: apiv1.PortalQuestionAuditDefaultLimit, Truncated: truncated, NextCursor: cursor,
		Origin: apiv1.PortalQuestionOriginUnknown, Approval: apiv1.PortalQuestionApprovalNotRecorded,
		ProviderConsumption: apiv1.PortalQuestionConsumptionNotRecorded, ServedModel: apiv1.PortalQuestionServedModelUnknown,
		Items: items,
	}
	body, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func copyAuditThread(in *apiv1.InboxThread) *apiv1.InboxThread {
	if in == nil {
		return nil
	}
	out := *in
	if in.Reply != nil {
		reply := *in.Reply
		out.Reply = &reply
	}
	if in.WaitingOn != nil {
		owner := *in.WaitingOn
		out.WaitingOn = &owner
	}
	return &out
}

func questionAuditPanelHTML(t *testing.T, html string) string {
	t.Helper()
	const marker = "<h3>Question audit</h3>"
	start := strings.Index(html, marker)
	if start < 0 {
		t.Fatal("rendered page has no question audit section")
	}
	rest := html[start:]
	end := strings.Index(rest, "</section>")
	if end < 0 {
		t.Fatal("question audit section did not close")
	}
	return rest[:end]
}

func questionAuditSectionOf(t *testing.T, page portal.ProductPage, title string) portal.ProductSection {
	t.Helper()
	row := findRow(t, page, title)
	var found *portal.ProductSection
	for i := range row.Sections {
		if row.Sections[i].Title != "Question audit" {
			continue
		}
		if found != nil {
			t.Fatal("two question audit panels")
		}
		found = &row.Sections[i]
	}
	if found == nil {
		t.Fatal("no question audit panel")
	}
	return *found
}

func questionAuditSectionCount(page portal.ProductPage, title string) int {
	n := 0
	for _, g := range page.Groups {
		for _, row := range g.Rows {
			if row.Title != title {
				continue
			}
			for _, section := range row.Sections {
				if section.Title == "Question audit" {
					n++
				}
			}
		}
	}
	return n
}

func questionAuditHasState(section portal.ProductSection, status, text string) bool {
	for _, state := range section.States {
		if state.Status.Text == status && (text == "" || strings.Contains(state.Text, text)) {
			return true
		}
	}
	return false
}

func questionAuditSignature(page portal.ProductPage) string {
	var b strings.Builder
	b.WriteString(page.Rail.All + "/" + page.Rail.Attention + " " + page.ListTitle + " " + page.ListCount + "\n")
	for _, project := range page.Rail.Projects {
		b.WriteString(project.Name + "=" + project.Count + "\n")
	}
	if page.Usage != nil {
		for _, stat := range page.Usage.Stats {
			b.WriteString(stat.Label + "=" + stat.Value + "\n")
		}
		for _, row := range page.Usage.Rows {
			b.WriteString(row.Name + ":" + row.SessionUID + "\n")
		}
	}
	for _, g := range page.Groups {
		b.WriteString(g.Title + "\n")
		for _, row := range g.Rows {
			n := 0
			for _, section := range row.Sections {
				if section.Title != "Question audit" {
					n++
				}
			}
			b.WriteString(row.Title + " " + row.Status.Text + " " + strings.Join(row.Badges, "|") + " " + strings.Join(sectionTitles(row, false), ",") + "\n")
			_ = n
		}
	}
	return b.String()
}

func sectionTitles(row portal.ProductRow, includeAudit bool) []string {
	var titles []string
	for _, section := range row.Sections {
		if !includeAudit && section.Title == "Question audit" {
			continue
		}
		titles = append(titles, section.Title)
	}
	return titles
}
