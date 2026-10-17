package coordretention

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/contextpublication"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/taskclaim"
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

// TestEnvtestReplacedOwnerReleases is DESIGN-024 E6. The live Session keeps
// the owner's name and has a new UID. The claim that still names the old UID
// is released. A claim that names the live UID stays kept. The delete after
// grace stays in the fake-client tests.
func TestEnvtestReplacedOwnerReleases(t *testing.T) {
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
	for _, name := range []string{retentionNS, sender.Namespace} {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	session := &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sender.Namespace, Name: sender.Name}, Spec: devv1.AgentSessionSpec{
		Repo: "haynes-ops", Agent: devv1.AgentClaude, Mode: devv1.ModeTask, Model: "claude-haiku-4-5", Prompt: "read the replaced owner",
	}}
	if err := admin.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	if string(session.UID) == sender.UID {
		t.Fatal("the API server reused the original owner UID")
	}
	replaced := claimSpec()
	if err := admin.Create(ctx, &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: taskclaim.Name(replaced.Identity)}, Spec: replaced}); err != nil {
		t.Fatal(err)
	}
	current := claimSpec()
	current.Identity.TaskKey = "issue:302"
	current.Owner.UID = string(session.UID)
	if err := admin.Create(ctx, &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: taskclaim.Name(current.Identity)}, Spec: current}); err != nil {
		t.Fatal(err)
	}
	cfg := EvalConfig{Reader: admin, OperatorNamespace: retentionNS, ReconciledNamespaces: map[string]bool{sender.Namespace: true}}
	if got := decideClaim(t, ctx, cfg, taskclaim.Name(replaced.Identity)); got != Released {
		t.Fatalf("same name, new UID: %s, want Released", got)
	}
	if got := decideClaim(t, ctx, cfg, taskclaim.Name(current.Identity)); got != Kept {
		t.Fatalf("live UID: %s, want Kept", got)
	}
}

func decideClaim(t *testing.T, ctx context.Context, cfg EvalConfig, name string) Decision {
	t.Helper()
	sums, err := Summaries(ctx, ListConfig{Reader: cfg.Reader, Namespace: retentionNS, Enabled: map[Kind]bool{TaskClaim: true}})
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
		t.Fatalf("claim %s was not summarized", name)
	}
	got := Evaluate(ctx, cfg, []Summary{summary})
	if len(got) != 1 {
		t.Fatalf("results %#v", got)
	}
	return got[0].Decision
}

// TestEnvtestRetentionDeleteRight is DESIGN-024 E4's delete right. The Role is
// the design's R6 copy. It is not installed in haynes-ops. The identity-guard
// exec refusal stays out of this test.
func TestEnvtestRetentionDeleteRight(t *testing.T) {
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
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, rbacv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	const otherNS, operatorSA, sessionSA = "dev-agents", "dev-env-operator", "dev-env-agent"
	for _, obj := range []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: retentionNS}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNS}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: operatorSA}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: otherNS, Name: sessionSA}},
	} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	role, binding := retentionDeleteRole(t)
	for _, obj := range []client.Object{role, binding} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	as := func(ns, name string) client.Client {
		t.Helper()
		cfg := rest.CopyConfig(env.Config)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + ns + ":" + name}
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	operator, session := as(retentionNS, operatorSA), as(otherNS, sessionSA)

	claim := claimSpec()
	message := messageSpec()
	published := contextSpec()
	live := []client.Object{
		&coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: taskclaim.Name(claim.Identity)}, Spec: claim},
		&coord.PublishedContext{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: contextpublication.Name(published.Identity)}, Spec: published},
		&coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: inbox.MessageName(message.Sender, message.Key)}, Spec: message},
	}
	for _, obj := range live {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	msg := live[2].(*coord.InboxMessage)
	ackSpec := coord.InboxReadAckSpec{Project: message.Project, MessageName: msg.Name, MessageUID: string(msg.UID), Recipient: message.Recipient, Actor: actor()}
	ack := &coord.InboxReadAck{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: inbox.AckName(ackSpec.MessageUID, ackSpec.Recipient.UID)}, Spec: ackSpec}
	if err := admin.Create(ctx, ack); err != nil {
		t.Fatal(err)
	}
	live = append(live, ack)
	foreign := claimSpec()
	foreign.Identity.TaskKey = "issue:399"
	foreignClaim := &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: otherNS, Name: taskclaim.Name(foreign.Identity)}, Spec: foreign}
	if err := admin.Create(ctx, foreignClaim); err != nil {
		t.Fatal(err)
	}
	otherKind := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: "retention-rights-other"}}
	if err := admin.Create(ctx, otherKind); err != nil {
		t.Fatal(err)
	}

	if err := session.Get(ctx, client.ObjectKeyFromObject(live[0]), &coord.TaskClaim{}); !apierrors.IsForbidden(err) {
		t.Fatalf("session get: %v", err)
	}
	if err := session.Delete(ctx, live[0]); !apierrors.IsForbidden(err) {
		t.Fatalf("session delete: %v", err)
	}
	if err := operator.DeleteAllOf(ctx, &coord.TaskClaim{}, client.InNamespace(retentionNS)); !apierrors.IsForbidden(err) {
		t.Fatalf("deletecollection: %v", err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(live[0]), &coord.TaskClaim{}); err != nil {
		t.Fatalf("claim after the refused collection delete: %v", err)
	}
	for _, obj := range live {
		if err := operator.Delete(ctx, obj); err != nil {
			t.Fatalf("operator delete %T: %v", obj, err)
		}
		if err := admin.Get(ctx, client.ObjectKeyFromObject(obj), obj); !apierrors.IsNotFound(err) {
			t.Fatalf("operator delete %T left %v", obj, err)
		}
	}
	if err := operator.Delete(ctx, foreignClaim); !apierrors.IsForbidden(err) {
		t.Fatalf("delete outside the operator namespace: %v", err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(foreignClaim), &coord.TaskClaim{}); err != nil {
		t.Fatalf("foreign claim: %v", err)
	}
	if err := operator.Delete(ctx, otherKind); !apierrors.IsForbidden(err) {
		t.Fatalf("delete of another kind: %v", err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(otherKind), &corev1.ConfigMap{}); err != nil {
		t.Fatalf("configmap: %v", err)
	}
}

func retentionDeleteRole(t *testing.T) (*rbacv1.Role, *rbacv1.RoleBinding) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "haynes-ops", "coordination-retention-operator.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var role *rbacv1.Role
	var binding *rbacv1.RoleBinding
	for {
		var raw unstructured.Unstructured
		if err := dec.Decode(&raw.Object); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if len(raw.Object) == 0 {
			continue
		}
		switch raw.GetKind() {
		case "Role":
			role = &rbacv1.Role{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, role); err != nil {
				t.Fatal(err)
			}
		case "RoleBinding":
			binding = &rbacv1.RoleBinding{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, binding); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("retention copy has %s %s", raw.GetKind(), raw.GetName())
		}
	}
	if role == nil || binding == nil || role.Namespace != retentionNS || role.Name != "dev-env-coordination-retention" {
		t.Fatalf("role %#v binding %#v", role, binding)
	}
	if len(role.Rules) != 1 || len(role.Rules[0].APIGroups) != 1 || role.Rules[0].APIGroups[0] != coord.GroupVersion.Group {
		t.Fatalf("rule %#v", role.Rules)
	}
	if len(role.Rules[0].Verbs) != 1 || role.Rules[0].Verbs[0] != "delete" {
		t.Fatalf("verbs %#v", role.Rules[0].Verbs)
	}
	want := map[string]bool{"taskclaims": true, "publishedcontexts": true, "inboxmessages": true, "inboxreadacks": true}
	if len(role.Rules[0].Resources) != len(want) {
		t.Fatalf("resources %#v", role.Rules[0].Resources)
	}
	for _, resource := range role.Rules[0].Resources {
		if !want[resource] {
			t.Fatalf("resource %s", resource)
		}
		delete(want, resource)
	}
	if binding.Namespace != retentionNS || binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != role.Name || len(binding.Subjects) != 1 {
		t.Fatalf("binding %#v", binding)
	}
	subject := binding.Subjects[0]
	if subject.Kind != rbacv1.ServiceAccountKind || subject.Namespace != retentionNS || subject.Name != "dev-env-operator" {
		t.Fatalf("subject %#v", subject)
	}
	return role, binding
}
