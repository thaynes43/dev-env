package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Synthetic question and reply text. The audit document must not carry either
// line, the idempotency keys, the session prompt, or a provider error.
const (
	cliAuditQuestion = "Which base branch should I use?\n"
	cliAuditReply    = "Use main.\n"
)

// TestInboxCLIQuestionAuditSameStore is the missing combined boundary.
// agentrun.Run asks, reads, replies, reads and acknowledges over TLS on the
// fixture handler. After each transition the existing portal question-audit
// GET on that same handler must show the CLI message's name, UID, actor and
// timestamp, and the derived Unanswered, Answered, then Acknowledged state.
// Reading the reply is not an acknowledgement. A wrong session UID and a
// portal POST do not authorize a mutation or return the text.
func TestInboxCLIQuestionAuditSameStore(t *testing.T) {
	f, lead, child, cli, handler := questionAuditCLI(t, true)
	leaks := cliAuditLeaks()

	out, _ := cli.run(child, cliAuditQuestion, agentrun.ExitOK, "inbox", "ask", "--project", "sample", "--key", "q-1")
	asked := decodeCLI[apiv1.InboxResult](t, out)
	q := asked.Message
	if asked.Outcome != "Created" || q.Kind != apiv1.InboxKindQuestion || q.Recipient != owner(f, "lead") || q.Sender != owner(f, "child") || q.Name == "" || q.UID == "" || q.CreatedAt.IsZero() || q.Actor.PodName != "child" {
		t.Fatalf("ask receipt %+v", asked)
	}
	question := cliAuditPage(t, f, "child", apiv1.PortalQuestionBoxSent, leaks)
	qItem := auditNamed(t, question, q.Name)
	requireSameAuditMessage(t, qItem, q)
	requireNoAPIAck(t, qItem, apiv1.InboxStateUnanswered)
	if qItem.Kind != apiv1.InboxKindQuestion || qItem.InReplyTo != nil || qItem.Thread.Question != (apiv1.InboxMessageRef{Name: q.Name, UID: q.UID}) || qItem.Thread.WaitingOn == nil || *qItem.Thread.WaitingOn != owner(f, "lead") {
		t.Fatalf("unanswered question %+v", qItem)
	}
	requireSameThread(t, qItem.Thread, cli.thread(lead, q.Name))
	leadQuestion := auditNamed(t, cliAuditPage(t, f, "lead", apiv1.PortalQuestionBoxInbox, leaks), q.Name)
	requireSameAuditMessage(t, leadQuestion, q)
	requireNoAPIAck(t, leadQuestion, apiv1.InboxStateUnanswered)

	childSession := f.session("child")
	wrong := strings.Replace(questionAuditURL(childSession.Name, string(childSession.UID), apiv1.PortalQuestionBoxSent), "uid="+string(childSession.UID), "uid=not-the-uid", 1)
	if w := f.do(http.MethodGet, wrong, "tok-portal", nil); w.Code == http.StatusOK {
		t.Fatalf("wrong uid authorized %s", w.Body.String())
	} else {
		refuseLeaks(t, w.Body.String(), append(leaks, q.Name, q.UID))
	}

	text, stderr := cli.run(lead, "", agentrun.ExitOK, "inbox", "read", q.Name, "--project", "sample")
	if text != cliAuditQuestion || !strings.Contains(stderr, "state="+apiv1.InboxStateUnanswered) || strings.Contains(stderr, "state="+apiv1.InboxStateAcknowledged) {
		t.Fatalf("coordinator read %q %s", text, stderr)
	}
	afterRead := auditNamed(t, cliAuditPage(t, f, "child", apiv1.PortalQuestionBoxSent, leaks), q.Name)
	requireSameAuditMessage(t, afterRead, q)
	requireNoAPIAck(t, afterRead, apiv1.InboxStateUnanswered)

	out, _ = cli.run(lead, cliAuditReply, agentrun.ExitOK, "inbox", "reply", q.Name, "--project", "sample", "--key", "r-1")
	replied := decodeCLI[apiv1.InboxResult](t, out)
	r := replied.Message
	if replied.Outcome != "Created" || r.Kind != apiv1.InboxKindReply || r.Recipient != q.Sender || r.Sender != q.Recipient || r.InReplyTo == nil || *r.InReplyTo != (apiv1.InboxMessageRef{Name: q.Name, UID: q.UID}) || r.Name == "" || r.UID == "" || r.CreatedAt.IsZero() || r.Actor.PodName != "lead" {
		t.Fatalf("reply receipt %+v", replied)
	}
	replyItem := auditNamed(t, cliAuditPage(t, f, "child", apiv1.PortalQuestionBoxInbox, leaks), r.Name)
	requireSameAuditMessage(t, replyItem, r)
	requireNoAPIAck(t, replyItem, apiv1.InboxStateAnswered)
	if replyItem.InReplyTo == nil || *replyItem.InReplyTo != (apiv1.InboxMessageRef{Name: q.Name, UID: q.UID}) || replyItem.Thread.Reply == nil || *replyItem.Thread.Reply != (apiv1.InboxMessageRef{Name: r.Name, UID: r.UID}) || replyItem.Thread.Question != (apiv1.InboxMessageRef{Name: q.Name, UID: q.UID}) || replyItem.Thread.WaitingOn == nil || *replyItem.Thread.WaitingOn != owner(f, "child") {
		t.Fatalf("answered reply %+v", replyItem)
	}
	requireSameThread(t, replyItem.Thread, cli.thread(child, r.Name))
	answered := auditNamed(t, cliAuditPage(t, f, "child", apiv1.PortalQuestionBoxSent, leaks), q.Name)
	requireSameAuditMessage(t, answered, q)
	requireNoAPIAck(t, answered, apiv1.InboxStateAnswered)
	if answered.Thread.Reply == nil || *answered.Thread.Reply != (apiv1.InboxMessageRef{Name: r.Name, UID: r.UID}) {
		t.Fatalf("answered question lost the reply %+v", answered.Thread)
	}

	text, stderr = cli.run(child, "", agentrun.ExitOK, "inbox", "read", r.Name, "--project", "sample")
	if text != cliAuditReply || !strings.Contains(stderr, "state="+apiv1.InboxStateAnswered) || strings.Contains(stderr, "state="+apiv1.InboxStateAcknowledged) {
		t.Fatalf("reply read %q %s", text, stderr)
	}
	readReply := auditNamed(t, cliAuditPage(t, f, "child", apiv1.PortalQuestionBoxInbox, leaks), r.Name)
	requireSameAuditMessage(t, readReply, r)
	requireNoAPIAck(t, readReply, apiv1.InboxStateAnswered)

	portalAck := f.do(http.MethodPost, ackPath(r), "tok-portal", struct{}{})
	if portalAck.Code == http.StatusOK || portalAck.Code == http.StatusCreated {
		t.Fatalf("portal POST ack authorized %d %s", portalAck.Code, portalAck.Body.String())
	}
	refuseLeaks(t, portalAck.Body.String(), leaks)
	portalAudit := f.do(http.MethodPost, questionAuditURL(childSession.Name, string(childSession.UID), apiv1.PortalQuestionBoxInbox), "tok-portal", struct{}{})
	if portalAudit.Code != http.StatusMethodNotAllowed {
		t.Fatalf("portal POST audit %d %s", portalAudit.Code, portalAudit.Body.String())
	}
	refuseLeaks(t, portalAudit.Body.String(), append(leaks, r.Name, r.UID))
	stillAnswered := auditNamed(t, cliAuditPage(t, f, "child", apiv1.PortalQuestionBoxInbox, leaks), r.Name)
	requireSameAuditMessage(t, stillAnswered, r)
	requireNoAPIAck(t, stillAnswered, apiv1.InboxStateAnswered)
	if n := countMessages(t, f); n != 2 {
		t.Fatalf("messages %d", n)
	}

	out, _ = cli.run(child, "", agentrun.ExitOK, "inbox", "ack", r.Name, "--project", "sample")
	ack := decodeCLI[apiv1.InboxAckResult](t, out)
	if ack.Outcome != "Created" || ack.Ack.Project != "sample" || ack.Ack.MessageName != r.Name || ack.Ack.MessageUID != r.UID || ack.Ack.Actor.PodName != "child" || ack.Ack.CreatedAt.IsZero() || ack.Ack.Recipient != r.Recipient {
		t.Fatalf("ack receipt %+v", ack)
	}
	ackedReply := auditNamed(t, cliAuditPage(t, f, "child", apiv1.PortalQuestionBoxInbox, leaks), r.Name)
	requireSameAuditMessage(t, ackedReply, r)
	requireAPIAck(t, ackedReply, ack.Ack)
	ackedQuestion := auditNamed(t, cliAuditPage(t, f, "child", apiv1.PortalQuestionBoxSent, leaks), q.Name)
	requireSameAuditMessage(t, ackedQuestion, q)
	requireAPIAck(t, ackedQuestion, ack.Ack)
	requireSameThread(t, ackedQuestion.Thread, cli.thread(child, q.Name, "--sent"))
	leadAcked := auditNamed(t, cliAuditPage(t, f, "lead", apiv1.PortalQuestionBoxInbox, leaks), q.Name)
	requireSameAuditMessage(t, leadAcked, q)
	requireAPIAck(t, leadAcked, ack.Ack)
	if f.h != handler {
		t.Fatal("portal GET and CLI TLS diverged from the fixture handler")
	}
}

// TestInboxCLIQuestionAuditDefaultOff keeps the portal audit flag off on the
// same handler the CLI uses. The portal GET stays not found, and a portal
// POST to the inbox ack route neither acknowledges the CLI reply nor returns
// its text. The asker's own ack is still the first acknowledgement.
func TestInboxCLIQuestionAuditDefaultOff(t *testing.T) {
	f, lead, child, cli, handler := questionAuditCLI(t, false)
	leaks := cliAuditLeaks()
	out, _ := cli.run(child, cliAuditQuestion, agentrun.ExitOK, "inbox", "ask", "--project", "sample", "--key", "q-1")
	q := decodeCLI[apiv1.InboxResult](t, out).Message
	if q.Name == "" || q.UID == "" {
		t.Fatal("ask", out)
	}
	session := f.session("child")
	raw := questionAuditURL(session.Name, string(session.UID), apiv1.PortalQuestionBoxSent)
	if w := f.do(http.MethodGet, raw, "tok-portal", nil); w.Code != http.StatusNotFound {
		t.Fatalf("default-off audit %d %s", w.Code, w.Body.String())
	} else {
		refuseLeaks(t, w.Body.String(), append(leaks, q.Name, q.UID))
	}
	if w := f.do(http.MethodPost, ackPath(q), "tok-portal", struct{}{}); w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Fatalf("portal POST ack authorized %d %s", w.Code, w.Body.String())
	} else {
		refuseLeaks(t, w.Body.String(), leaks)
	}
	if got := cli.thread(lead, q.Name); got.State != apiv1.InboxStateUnanswered {
		t.Fatalf("portal POST changed the question %+v", got)
	}

	out, _ = cli.run(lead, cliAuditReply, agentrun.ExitOK, "inbox", "reply", q.Name, "--project", "sample", "--key", "r-1")
	r := decodeCLI[apiv1.InboxResult](t, out).Message
	if w := f.do(http.MethodPost, ackPath(r), "tok-portal", struct{}{}); w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Fatalf("portal POST ack authorized %d %s", w.Code, w.Body.String())
	} else {
		refuseLeaks(t, w.Body.String(), leaks)
	}
	if w := f.do(http.MethodGet, questionAuditURL(session.Name, string(session.UID), apiv1.PortalQuestionBoxInbox), "tok-portal", nil); w.Code != http.StatusNotFound {
		t.Fatalf("default-off audit %d %s", w.Code, w.Body.String())
	} else {
		refuseLeaks(t, w.Body.String(), append(leaks, r.Name, r.UID))
	}
	if got := cli.thread(child, r.Name); got.State != apiv1.InboxStateAnswered || got.Reply == nil || got.Reply.Name != r.Name || got.Reply.UID != r.UID {
		t.Fatalf("portal POST changed the reply %+v", got)
	}
	out, _ = cli.run(child, "", agentrun.ExitOK, "inbox", "ack", r.Name, "--project", "sample")
	ack := decodeCLI[apiv1.InboxAckResult](t, out)
	if ack.Outcome != "Created" || ack.Ack.MessageName != r.Name || ack.Ack.MessageUID != r.UID {
		t.Fatalf("asker ack %+v", ack)
	}
	if f.h != handler {
		t.Fatal("portal GET and CLI TLS diverged from the fixture handler")
	}
}

func questionAuditCLI(t *testing.T, on bool) (f *fixture, lead, child string, cli *inboxCLI, handler http.Handler) {
	t.Helper()
	f, lead, child, _ = askReplyFixture(t)
	enableQuestionAuditPortal(t, f, on)
	handler = f.h
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	cli = newInboxCLI(t, server)
	return f, lead, child, cli, handler
}

type inboxCLI struct {
	t         *testing.T
	dir       string
	tokenPath string
	extra     []string
}

func newInboxCLI(t *testing.T, server *httptest.Server) *inboxCLI {
	t.Helper()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return &inboxCLI{t: t, dir: dir, tokenPath: tokenPath, extra: []string{"--api-url", server.URL, "--token-file", tokenPath, "--ca-file", caPath}}
}

func (c *inboxCLI) run(token, stdin string, want int, args ...string) (string, string) {
	c.t.Helper()
	if err := os.WriteFile(c.tokenPath, []byte(token), 0o600); err != nil {
		c.t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	env := agentrun.DefaultEnv()
	env.Stdout, env.Stderr, env.Stdin = &stdout, &stderr, strings.NewReader(stdin)
	env.Getenv = func(string) string { return "" }
	env.SessionTokenFile, env.ServiceAccountDir = filepath.Join(c.dir, "absent-session"), filepath.Join(c.dir, "absent-sa")
	env.LookPath = func(string) (string, error) { return "", errors.New("synthetic: no host command") }
	env.Run = func(context.Context, []string, io.Reader) (int, error) {
		return 0, errors.New("synthetic host process refused")
	}
	env.Sleep = func(context.Context, time.Duration) error {
		c.t.Errorf("inbox client retried")
		return errors.New("synthetic retry refused")
	}
	if code := agentrun.Run(context.Background(), append(append([]string{}, args...), c.extra...), env); code != want {
		c.t.Fatalf("%v: exit %d want %d\nstdout %s\nstderr %s", args, code, want, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func (c *inboxCLI) thread(token, name string, args ...string) apiv1.InboxThread {
	c.t.Helper()
	out, _ := c.run(token, "", agentrun.ExitOK, append([]string{"inbox", "list", "--project", "sample"}, args...)...)
	item := itemNamed(c.t, decodeCLI[apiv1.InboxList](c.t, out), name)
	if item.Thread == nil {
		c.t.Fatal("no thread state", out)
	}
	return *item.Thread
}

func cliAuditLeaks() []string {
	return []string{
		"Which base branch should I use?", "Use main.", "q-1", "r-1",
		"providerError", "provider_error", "requestedModel", "claude-opus-5-5",
		"PRIVATE_PROMPT", "PRIVATE_INBOX",
	}
}

func cliAuditPage(t *testing.T, f *fixture, session, box string, leaks []string) apiv1.PortalQuestionAudit {
	t.Helper()
	s := f.session(session)
	w := f.do(http.MethodGet, questionAuditURL(s.Name, string(s.UID), box), "tok-portal", nil)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("audit %s %s: %d %s", session, box, w.Code, body)
	}
	assertQuestionAuditAllowlist(t, body)
	refuseLeaks(t, body, leaks)
	page := decode[apiv1.PortalQuestionAudit](t, w)
	if page.Version != apiv1.PortalQuestionAuditVersion || page.Box != box || page.Project != "sample" || page.Session.Name != session || page.Session.UID != string(s.UID) {
		t.Fatalf("echo %+v", page)
	}
	if page.Origin != apiv1.PortalQuestionOriginUnknown || page.Approval != apiv1.PortalQuestionApprovalNotRecorded || page.ProviderConsumption != apiv1.PortalQuestionConsumptionNotRecorded || page.ServedModel != apiv1.PortalQuestionServedModelUnknown {
		t.Fatalf("envelope %+v", page)
	}
	for _, item := range page.Items {
		if item.Origin != page.Origin || item.Approval != page.Approval || item.ProviderConsumption != page.ProviderConsumption || item.ServedModel != page.ServedModel || !apiv1.PortalQuestionAuditItemOK(item) {
			t.Fatalf("item %+v", item)
		}
	}
	return page
}

func requireSameAuditMessage(t *testing.T, item apiv1.PortalQuestionAuditItem, msg apiv1.InboxMetadata) {
	t.Helper()
	if item.Name != msg.Name || item.UID != msg.UID || !item.CreatedAt.Equal(msg.CreatedAt) || item.Actor != msg.Actor || item.Sender != msg.Sender || item.Recipient != msg.Recipient {
		t.Fatalf("audit identity name %s uid %s at %s actor %+v sender %+v recipient %+v; cli name %s uid %s at %s actor %+v sender %+v recipient %+v", item.Name, item.UID, item.CreatedAt, item.Actor, item.Sender, item.Recipient, msg.Name, msg.UID, msg.CreatedAt, msg.Actor, msg.Sender, msg.Recipient)
	}
}

func requireSameThread(t *testing.T, audit *apiv1.InboxThread, cli apiv1.InboxThread) {
	t.Helper()
	if audit == nil || audit.State != cli.State || audit.Question != cli.Question || !sameAuditRef(audit.Reply, cli.Reply) || !sameAuditOwner(audit.WaitingOn, cli.WaitingOn) || !audit.ObservedAt.Equal(cli.ObservedAt) || audit.StaleAfterSeconds != cli.StaleAfterSeconds {
		t.Fatalf("thread audit %+v cli %+v", audit, cli)
	}
}

func requireNoAPIAck(t *testing.T, item apiv1.PortalQuestionAuditItem, state string) {
	t.Helper()
	if item.Thread == nil || item.Thread.State != state || item.Acknowledgement.Status != apiv1.PortalQuestionAckUnavailable || item.Acknowledgement.Actor != nil || item.Acknowledgement.CreatedAt != nil {
		t.Fatalf("state %s ack %+v thread %+v", state, item.Acknowledgement, item.Thread)
	}
}

func requireAPIAck(t *testing.T, item apiv1.PortalQuestionAuditItem, ack apiv1.InboxReadAck) {
	t.Helper()
	if item.Thread == nil || item.Thread.State != apiv1.InboxStateAcknowledged || item.Thread.Reply == nil || item.Acknowledgement.Status != apiv1.PortalQuestionAckRecorded || item.Acknowledgement.Actor == nil || item.Acknowledgement.CreatedAt == nil {
		t.Fatalf("ack evidence %+v thread %+v", item.Acknowledgement, item.Thread)
	}
	if *item.Acknowledgement.Actor != ack.Actor || !item.Acknowledgement.CreatedAt.Equal(ack.CreatedAt) || item.Thread.Reply.Name != ack.MessageName || item.Thread.Reply.UID != ack.MessageUID {
		t.Fatalf("audit ack %+v at %s; cli ack %+v at %s message %s %s", item.Acknowledgement.Actor, item.Acknowledgement.CreatedAt, ack.Actor, ack.CreatedAt, ack.MessageName, ack.MessageUID)
	}
}

func sameAuditRef(a, b *apiv1.InboxMessageRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameAuditOwner(a, b *apiv1.TaskClaimOwner) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func refuseLeaks(t *testing.T, body string, leaks []string) {
	t.Helper()
	for _, leak := range leaks {
		if leak != "" && strings.Contains(body, leak) {
			t.Fatalf("response carried %q", leak)
		}
	}
}

func decodeCLI[T any](t *testing.T, out string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return v
}
