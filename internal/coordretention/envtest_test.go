package coordretention

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/testenv"
)

// TestEnvtestInboxRule proves the inbox keep decisions against a real API
// server. Delete order stays in the fake-client tests.
func TestEnvtestInboxRule(t *testing.T) {
	env, err := testenv.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Log("envtest stop:", err)
		}
	})
	ctx := context.Background()
	scheme := retentionScheme(t)
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	admin, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{retentionNS, recipient.Namespace} {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	session := &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: recipient.Namespace, Name: recipient.Name}, Spec: devv1.AgentSessionSpec{
		Repo: "haynes-ops", Agent: devv1.AgentClaude, Mode: devv1.ModeTask, Model: "claude-haiku-4-5", Prompt: "read the inbox rule",
	}}
	if err := admin.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	spec := messageSpec()
	spec.Recipient.Namespace, spec.Recipient.Name, spec.Recipient.UID = session.Namespace, session.Name, string(session.UID)
	msg := &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: inbox.MessageName(spec.Sender, spec.Key)}, Spec: spec}
	if err := admin.Create(ctx, msg); err != nil {
		t.Fatal(err)
	}
	cfg := EvalConfig{Reader: admin, OperatorNamespace: retentionNS, ReconciledNamespaces: map[string]bool{recipient.Namespace: true}}
	if got := decideMessage(t, ctx, cfg, msg.Name); got != Kept {
		t.Fatalf("sender absent and recipient present without an ack: %s, want Kept", got)
	}
	ack := &coord.InboxReadAck{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: inbox.AckName(string(msg.UID), spec.Recipient.UID)}, Spec: coord.InboxReadAckSpec{
		Project: spec.Project, MessageName: msg.Name, MessageUID: string(msg.UID), Recipient: spec.Recipient, Actor: actor(),
	}}
	if err := admin.Create(ctx, ack); err != nil {
		t.Fatal(err)
	}
	if got := decideMessage(t, ctx, cfg, msg.Name); got != Released {
		t.Fatalf("recipient ack: %s, want Released", got)
	}
	orphanKey := "retention-2"
	orphanName := inbox.MessageName(spec.Sender, orphanKey)
	orphanUID := "44444444-4444-4444-8444-444444444444"
	orphan := &coord.InboxReadAck{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: inbox.AckName(orphanUID, spec.Recipient.UID)}, Spec: coord.InboxReadAckSpec{
		Project: spec.Project, MessageName: orphanName, MessageUID: orphanUID, Recipient: spec.Recipient, Actor: actor(),
	}}
	if err := admin.Create(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	sums, err := Summaries(ctx, ListConfig{Reader: admin, Namespace: retentionNS, Enabled: map[Kind]bool{InboxReadAck: true}})
	if err != nil {
		t.Fatal(err)
	}
	var orphanSummary Summary
	for _, s := range sums {
		if s.Name == orphan.Name {
			orphanSummary = s
		}
	}
	if !orphanSummary.Valid {
		t.Fatal("orphan ack was not summarized")
	}
	cfg.Archive = map[MessageRef]ArchiveEvidence{orphanSummary.Message: {Known: true, Holds: false}}
	got := Evaluate(ctx, cfg, []Summary{orphanSummary})
	if len(got) != 1 || got[0].Decision != Kept {
		t.Fatalf("ack whose message is gone and not archived: %#v", got)
	}
}

func decideMessage(t *testing.T, ctx context.Context, cfg EvalConfig, name string) Decision {
	t.Helper()
	sums, err := Summaries(ctx, ListConfig{Reader: cfg.Reader, Namespace: retentionNS, Enabled: map[Kind]bool{InboxMessage: true}})
	if err != nil {
		t.Fatal(err)
	}
	var summary Summary
	for _, s := range sums {
		if s.Name == name {
			summary = s
		}
	}
	if !summary.Valid {
		t.Fatal("message was not summarized")
	}
	got := Evaluate(ctx, cfg, []Summary{summary})
	if len(got) != 1 {
		t.Fatalf("results %#v", got)
	}
	return got[0].Decision
}
