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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/inbox"
)

// Real agentrun.Run over TLS into the existing inbox handlers and fake inbox
// store. Redirect and token-file rules stay in the agentrun client tests.
func TestInboxCLIRunRetainsSenderAttribution(t *testing.T) {
	const (
		claudeText = "SYNTHETIC_INBOX_E2E_TEXT\n"
		codexText  = "SYNTHETIC_CODEX_INBOX_TEXT\n"
		claudeKey  = "logical-1"
		codexKey   = "logical-codex"
		unboundTok = "synthetic-unbound-legacy"
	)
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	executor := &fakeExec{}
	f.srv.Exec = executor
	store := f.srv.Inbox
	f.auth[unboundTok] = saIdentity(sessionNS + "/" + controller.ServiceAccountName)
	claudeTok := claimSession(t, f, "sender", humanSA)
	codexTok := claimSession(t, f, "sender-codex", clientSA)
	recipientTok := claimSession(t, f, "recipient", clientSA)
	legacyTok := f.sessionPod("legacy", "dev", 0)
	codexSess := f.session("sender-codex")
	codexSess.Spec.Agent = v1alpha1.AgentCodex
	codexSess.Spec.Model = "gpt-6.1-sol"
	if err := f.c.Update(context.Background(), codexSess); err != nil {
		t.Fatal(err)
	}
	recipient := f.session("recipient")
	recipientUID := string(recipient.UID)

	var handler atomic.Value
	handler.Store(f.h)
	var requests, publishPosts, ackPosts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if r.Method == http.MethodPost && r.URL.Path == apiv1.InboxPath {
			publishPosts.Add(1)
			var fields map[string]json.RawMessage
			if json.Unmarshal(body, &fields) != nil {
				t.Errorf("publish body was not one JSON object")
			} else {
				for key := range fields {
					switch key {
					case "project", "recipient", "key", "text":
					default:
						t.Errorf("publish body included %s", key)
					}
				}
			}
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack") {
			ackPosts.Add(1)
		}
		handler.Load().(http.Handler).ServeHTTP(w, r)
	}))
	defer server.Close()

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(token, stdin string, want int, args ...string) (string, string) {
		t.Helper()
		if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		env := agentrun.DefaultEnv()
		env.Stdout, env.Stderr = &stdout, &stderr
		env.Stdin = strings.NewReader(stdin)
		env.Getenv = func(string) string { return "" }
		env.SessionTokenFile = filepath.Join(dir, "absent-session")
		env.ServiceAccountDir = filepath.Join(dir, "absent-sa")
		env.LookPath = func(string) (string, error) { return "", errors.New("synthetic: no host command") }
		env.Run = func(context.Context, []string, io.Reader) (int, error) {
			return 0, errors.New("synthetic host process refused")
		}
		env.Output = func(context.Context, []string) ([]byte, error) {
			return nil, errors.New("synthetic host process refused")
		}
		env.ExecOutput = func(context.Context, []string) (int, string, string, error) {
			return 0, "", "", errors.New("synthetic host process refused")
		}
		env.PortForward = func(context.Context, []string) (string, func(), error) {
			return "", func() {}, errors.New("synthetic port-forward refused")
		}
		env.Sleep = func(context.Context, time.Duration) error {
			t.Errorf("inbox client retried")
			return errors.New("synthetic retry refused")
		}
		env.NewKey = func() string {
			t.Errorf("inbox client generated a key")
			return "generated-key-must-not-be-used"
		}
		args = append(append([]string{}, args...), "--api-url", server.URL, "--token-file", tokenPath, "--ca-file", caPath)
		if code := agentrun.Run(context.Background(), args, env); code != want {
			t.Fatalf("%v: exit %d want %d\nstdout %s\nstderr %s", args[:len(args)-6], code, want, stdout.String(), stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	send := func(token, key, text string, want int) (string, string) {
		t.Helper()
		return run(token, text, want, "inbox", "send", "--project", "sample", "--recipient-namespace", sessionNS, "--recipient-name", "recipient", "--recipient-uid", recipientUID, "--key", key)
	}

	stdout, stderr := send(claudeTok, claudeKey, claudeText, agentrun.ExitOK)
	if strings.Contains(stdout+stderr, claudeText) || strings.Contains(stdout+stderr, claudeTok) {
		t.Fatal("send disclosed text or token")
	}
	created := decodeInboxResult(t, stdout)
	if created.Outcome != "Created" {
		t.Fatalf("outcome %s", created.Outcome)
	}
	msgs, acks := listStoredInbox(t, f)
	if len(msgs) != 1 || len(acks) != 0 || publishPosts.Load() != 1 {
		t.Fatalf("create store messages=%d acks=%d posts=%d", len(msgs), len(acks), publishPosts.Load())
	}
	assertInboxAttribution(t, msgs[0], f.session("sender"), sessionPodObject(t, f, "sender"), recipient, claudeKey, claudeText)
	if created.Message.UID != string(msgs[0].UID) || created.Message.Name != msgs[0].Name || created.Message.Sender.UID != string(f.session("sender").UID) || created.Message.Provider != string(v1alpha1.AgentClaude) {
		t.Fatalf("CLI receipt drifted from store: %+v", created.Message)
	}
	if _, err := store.ReadAck(context.Background(), &msgs[0]); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatal("send created an acknowledgement", err)
	}

	second := &Server{
		Client: f.c, Live: f.c, Auth: f.auth, Policy: f.srv.Policy, Projects: f.srv.Projects,
		Inbox: store, InboxProjects: f.srv.InboxProjects, Exec: executor, Now: f.srv.Now, Log: f.srv.Log,
	}
	handler.Store(second.Handler())
	f.srv.Inbox = nil

	stdout, stderr = send(claudeTok, claudeKey, claudeText, agentrun.ExitOK)
	if strings.Contains(stdout+stderr, claudeText) {
		t.Fatal("retry disclosed text")
	}
	confirmed := decodeInboxResult(t, stdout)
	msgs, acks = listStoredInbox(t, f)
	if confirmed.Outcome != "Confirmed" || confirmed.Message.UID != created.Message.UID || len(msgs) != 1 || len(acks) != 0 || publishPosts.Load() != 2 {
		t.Fatalf("retry changed the stored message: outcome %s uid %s messages %d posts %d", confirmed.Outcome, confirmed.Message.UID, len(msgs), publishPosts.Load())
	}
	beforeConflict := publishPosts.Load()
	stdout, stderr = send(claudeTok, claudeKey, claudeText+"CHANGED", agentrun.ExitFailed)
	if !strings.Contains(stderr, "inbox intent conflicts with the existing key; no replacement key was generated") || strings.Contains(stdout+stderr, claudeText) || strings.Contains(stdout+stderr, claudeTok) || publishPosts.Load() != beforeConflict+1 {
		t.Fatalf("conflict replayed or disclosed content: posts %d\n%s%s", publishPosts.Load(), stdout, stderr)
	}
	msgs, _ = listStoredInbox(t, f)
	if len(msgs) != 1 || msgs[0].Spec.Text != claudeText || string(msgs[0].UID) != created.Message.UID {
		t.Fatal("conflict mutated the stored message")
	}

	stdout, stderr = run(recipientTok, "", agentrun.ExitOK, "inbox", "list", "--project", "sample", "--limit", "20")
	var listed apiv1.InboxList
	if json.Unmarshal([]byte(stdout), &listed) != nil || len(listed.Items) != 1 || listed.Items[0].Name != created.Message.Name || listed.Items[0].UID != created.Message.UID || strings.Contains(stdout+stderr, claudeText) || strings.Contains(stdout+stderr, recipientTok) {
		t.Fatalf("list was not metadata: %s%s", stdout, stderr)
	}
	if _, err := store.ReadAck(context.Background(), &msgs[0]); !errors.Is(err, inbox.ErrNotFound) || ackPosts.Load() != 0 {
		t.Fatal("list acknowledged the message")
	}
	stdout, stderr = run(recipientTok, "", agentrun.ExitOK, "inbox", "read", created.Message.Name, "--project", "sample")
	if stdout != claudeText || strings.Contains(stderr, claudeText) || ackPosts.Load() != 0 {
		t.Fatalf("read stdout %q stderr %q acks %d", stdout, stderr, ackPosts.Load())
	}
	if _, err := store.ReadAck(context.Background(), &msgs[0]); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatal("read created an acknowledgement", err)
	}
	stdout, _ = run(recipientTok, "", agentrun.ExitOK, "inbox", "ack", created.Message.Name, "--project", "sample")
	acked := decodeInboxAck(t, stdout)
	if acked.Outcome != "Created" || ackPosts.Load() != 1 {
		t.Fatalf("ack %s posts %d", acked.Outcome, ackPosts.Load())
	}
	_, acks = listStoredInbox(t, f)
	if len(acks) != 1 || acks[0].Spec.MessageUID != created.Message.UID || acks[0].Spec.Recipient.UID != recipientUID || len(executor.cmds) != 0 {
		t.Fatalf("ack was not one recipient record: %+v exec %v", acks, executor.cmds)
	}
	stdout, _ = run(recipientTok, "", agentrun.ExitOK, "inbox", "ack", created.Message.Name, "--project", "sample")
	again := decodeInboxAck(t, stdout)
	_, acks = listStoredInbox(t, f)
	if again.Outcome != "Confirmed" || again.Ack.UID != acked.Ack.UID || len(acks) != 1 || ackPosts.Load() != 2 {
		t.Fatal("ack retry replaced the acknowledgement")
	}

	stdout, stderr = run(codexTok, codexText, agentrun.ExitOK, "inbox", "send", "--project", "sample", "--recipient-namespace", sessionNS, "--recipient-name", "recipient", "--recipient-uid", recipientUID, "--key", codexKey)
	if strings.Contains(stdout+stderr, codexText) || strings.Contains(stdout+stderr, codexTok) {
		t.Fatal("codex send disclosed text or token")
	}
	codexResult := decodeInboxResult(t, stdout)
	msgs, acks = listStoredInbox(t, f)
	codexMsg := storedByKey(t, msgs, codexKey)
	assertInboxAttribution(t, codexMsg, f.session("sender-codex"), sessionPodObject(t, f, "sender-codex"), f.session("recipient"), codexKey, codexText)
	if codexResult.Message.Provider != string(v1alpha1.AgentCodex) || codexMsg.Spec.Provider != string(v1alpha1.AgentCodex) || len(msgs) != 2 || len(acks) != 1 || len(executor.cmds) != 0 {
		t.Fatalf("provider changed the inbox: provider %s messages %d exec %v", codexResult.Message.Provider, len(msgs), executor.cmds)
	}
	stdout, _ = run(recipientTok, "", agentrun.ExitOK, "inbox", "list", "--project", "sample")
	if strings.Contains(stdout, claudeText) || strings.Contains(stdout, codexText) || !strings.Contains(stdout, created.Message.Name) || !strings.Contains(stdout, codexResult.Message.Name) {
		t.Fatal("mixed-provider list was not metadata for both messages")
	}
	stdout, _ = run(recipientTok, "", agentrun.ExitOK, "inbox", "read", codexResult.Message.Name, "--project", "sample")
	_, acks = listStoredInbox(t, f)
	if stdout != codexText || len(acks) != 1 {
		t.Fatal("reading the second provider message consumed or acknowledged it")
	}
	original, err := store.Get(context.Background(), created.Message.Name)
	if err != nil || original.Spec.Text != claudeText || string(original.UID) != created.Message.UID {
		t.Fatal("reconstructed store did not return the original once", err)
	}

	kept := inboxDigest(t, f)
	refuse := func(name, token, stdin string, args ...string) {
		t.Helper()
		beforeReq, beforePublish, beforeAck := requests.Load(), publishPosts.Load(), ackPosts.Load()
		out, errOut := run(token, stdin, agentrun.ExitNotFound, args...)
		combined := out + errOut
		wantPublish, wantAck := beforePublish, beforeAck
		if len(args) > 1 && args[1] == "send" {
			wantPublish++
		}
		if len(args) > 1 && args[1] == "ack" {
			wantAck++
		}
		if !strings.Contains(errOut, "inbox record or authorized scope was not found (404)") || strings.Contains(combined, "not an API error") || inboxDigest(t, f) != kept || requests.Load() != beforeReq+1 || publishPosts.Load() != wantPublish || ackPosts.Load() != wantAck {
			t.Fatalf("%s classification or store: requests %d publish %d ack %d\n%s", name, requests.Load()-beforeReq, publishPosts.Load(), ackPosts.Load(), combined)
		}
		for _, secret := range []string{claudeText, codexText, token, claudeTok, codexTok, recipientTok, legacyTok, unboundTok} {
			if secret != "" && strings.Contains(combined, secret) {
				t.Fatalf("%s disclosed a fixture secret", name)
			}
		}
	}
	refuse("wrong uid", claudeTok, claudeText, "inbox", "send", "--project", "sample", "--recipient-namespace", sessionNS, "--recipient-name", "recipient", "--recipient-uid", "wrong-uid-value", "--key", "wrong-uid")
	refuse("unauthorized project", claudeTok, claudeText, "inbox", "send", "--project", "other", "--recipient-namespace", sessionNS, "--recipient-name", "recipient", "--recipient-uid", recipientUID, "--key", "other-project")
	refuse("legacy unbound", unboundTok, claudeText, "inbox", "send", "--project", "sample", "--recipient-namespace", sessionNS, "--recipient-name", "recipient", "--recipient-uid", recipientUID, "--key", "legacy-unbound")
	refuse("legacy snapshot", legacyTok, claudeText, "inbox", "send", "--project", "sample", "--recipient-namespace", sessionNS, "--recipient-name", "recipient", "--recipient-uid", recipientUID, "--key", "legacy-nosnapshot")

	removeInboxSession(t, f, "recipient")
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "recipient"}, &pod); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	replacementTok := claimSession(t, f, "recipient", clientSA)
	if replacementTok == "" || string(f.session("recipient").UID) == recipientUID {
		t.Fatal("replacement did not receive a new uid")
	}
	kept = inboxDigest(t, f)
	refuse("replaced read", replacementTok, "", "inbox", "read", created.Message.Name, "--project", "sample")
	refuse("replaced ack", replacementTok, "", "inbox", "ack", created.Message.Name, "--project", "sample")
	refuse("replaced uid", claudeTok, claudeText, "inbox", "send", "--project", "sample", "--recipient-namespace", sessionNS, "--recipient-name", "recipient", "--recipient-uid", recipientUID, "--key", "replaced-uid")
	stdout, stderr = run(replacementTok, "", agentrun.ExitOK, "inbox", "list", "--project", "sample")
	if strings.Contains(stdout+stderr, claudeText) || strings.Contains(stdout+stderr, codexText) || strings.Contains(stdout, created.Message.Name) || strings.Contains(stdout, codexResult.Message.Name) || inboxDigest(t, f) != kept {
		t.Fatalf("replacement adopted the old inbox: %s", stdout)
	}
	if len(executor.cmds) != 0 {
		t.Fatal("inbox CLI invoked a provider executor", executor.cmds)
	}
}

func sessionPodObject(t *testing.T, f *fixture, name string) *corev1.Pod {
	t.Helper()
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	return &pod
}

func listStoredInbox(t *testing.T, f *fixture) ([]coord.InboxMessage, []coord.InboxReadAck) {
	t.Helper()
	var messages coord.InboxMessageList
	var acks coord.InboxReadAckList
	if err := f.c.List(context.Background(), &messages, client.InNamespace("dev-env-system")); err != nil {
		t.Fatal(err)
	}
	if err := f.c.List(context.Background(), &acks, client.InNamespace("dev-env-system")); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(messages.Items, func(a, b coord.InboxMessage) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(acks.Items, func(a, b coord.InboxReadAck) int { return strings.Compare(a.Name, b.Name) })
	return messages.Items, acks.Items
}

func storedByKey(t *testing.T, messages []coord.InboxMessage, key string) coord.InboxMessage {
	t.Helper()
	var found *coord.InboxMessage
	for i := range messages {
		if messages[i].Spec.Key != key {
			continue
		}
		if found != nil {
			t.Fatalf("duplicate inbox key %s", key)
		}
		found = &messages[i]
	}
	if found == nil {
		t.Fatalf("missing inbox key %s", key)
	}
	return *found
}

func assertInboxAttribution(t *testing.T, m coord.InboxMessage, sender *v1alpha1.AgentSession, pod *corev1.Pod, recipient *v1alpha1.AgentSession, key, text string) {
	t.Helper()
	if m.Namespace != "dev-env-system" || m.UID == "" || m.Name != inbox.MessageName(sessionIdentity(sender), key) {
		t.Fatalf("message identity %s/%s", m.Namespace, m.Name)
	}
	if m.Spec.Project != "sample" || m.Spec.Key != key || m.Spec.Text != text || m.Spec.Repository != "alias" || m.Spec.Provider != string(sender.Spec.Agent) || m.Spec.RequestedModel != sender.Spec.Model {
		t.Fatalf("stored intent %+v", m.Spec)
	}
	if m.Spec.Sender != sessionIdentity(sender) || m.Spec.Recipient != sessionIdentity(recipient) {
		t.Fatalf("stored sender %+v recipient %+v", m.Spec.Sender, m.Spec.Recipient)
	}
	want := coord.ClaimActor{Kind: "session", ServiceAccount: sessionNS + "/" + controller.ServiceAccountName, PodName: pod.Name, PodUID: string(pod.UID)}
	if m.Spec.Actor != want || m.Spec.Provenance.InitialPinState != "Unknown" || m.Spec.Provenance.GitHub != "thaynes43/actual-source" || m.Spec.Provenance.DefaultBranch != "stable" {
		t.Fatalf("actor %+v provenance %+v", m.Spec.Actor, m.Spec.Provenance)
	}
}

func inboxDigest(t *testing.T, f *fixture) string {
	t.Helper()
	msgs, acks := listStoredInbox(t, f)
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Name)
		b.WriteString(string(m.UID))
		b.WriteString(m.Spec.Key)
		b.WriteString(m.Spec.Text)
		b.WriteString(m.Spec.Sender.UID)
		b.WriteString(m.Spec.Recipient.UID)
		b.WriteString(m.Spec.Provider)
	}
	for _, a := range acks {
		b.WriteString(a.Name)
		b.WriteString(string(a.UID))
		b.WriteString(a.Spec.MessageUID)
		b.WriteString(a.Spec.Recipient.UID)
	}
	return b.String()
}

func decodeInboxResult(t *testing.T, raw string) apiv1.InboxResult {
	t.Helper()
	var result apiv1.InboxResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode result %q: %v", raw, err)
	}
	return result
}

func decodeInboxAck(t *testing.T, raw string) apiv1.InboxAckResult {
	t.Helper()
	var result apiv1.InboxAckResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode ack %q: %v", raw, err)
	}
	return result
}
