package testutil

import (
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/templates"
)

// PilotFixture shares only synthetic nonsecret source authority across tests.
type PilotFixture struct {
	Policy                                  *projectcanary.PilotPolicy
	Now                                     time.Time
	Catalog, Template, Seal, Common, Header *corev1.ConfigMap
	Templates                               *templates.Templates
	Snapshot                                []byte
	Rules                                   commonrules.Snapshot
}

func NewPilot(t *testing.T) PilotFixture {
	t.Helper()
	old := New(t, "codex", projectcanary.PilotCaller)
	raw := `{"version":1,"repositories":{"dev-env":{"github":"thaynes43/dev-env","defaultBranch":"main"}},"projects":{"dev-env":{"repositories":[{"name":"dev-env"}],"rules":"Follow synthetic pilot project rules."}}}`
	catalog, err := projectcatalog.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("dev-env", "dev-env")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := old.Templates
	for i, e := range tmpl.Env {
		if e.Name == "AGENTD_PROJECT_CLONE_OWNER" {
			tmpl.Env[i].Value = "thaynes43"
		}
	}
	tmpl.Profiles[projectcanary.PilotProfile] = tmpl.Profiles["canary"]
	delete(tmpl.Profiles, "canary")
	tmpl.DefaultProfile = projectcanary.PilotProfile
	doc, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err = templates.Parse(map[string]string{templates.Key: string(doc)})
	if err != nil {
		t.Fatal(err)
	}
	a, h := commonfixture.Sources("Synthetic common pilot instructions.", "Synthetic Codex header.")
	rules := commonfixture.Snapshot(t, saved, "Synthetic common pilot instructions.", "Synthetic Codex header.")
	now := time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC)
	p := &projectcanary.PilotPolicy{NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(20 * time.Minute), SealUID: "11111111-1111-4111-8111-111111111111", CatalogRevision: catalog.Revision(), RulesRevision: snapshot.RulesRevision(), CommonRevision: rules.CommonRevision, TemplateRevision: tmpl.Revision(), Image: tmpl.Image, Prompts: []string{"Perform bounded integration actor A without source edits.", "Perform bounded integration actor B without source edits."}}
	yes := true
	f := PilotFixture{Policy: p, Now: now, Catalog: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: projectcanary.CatalogNamespace, Name: projectcanary.CatalogName, UID: "catalog-original", ResourceVersion: "1"}, Data: map[string]string{"catalog.json": raw}}, Template: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: templates.DefaultName, UID: "template-original", ResourceVersion: "1"}, Data: map[string]string{templates.Key: string(doc)}}, Seal: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: projectcanary.PilotSealNamespace, Name: projectcanary.PilotSealName, UID: types.UID(p.SealUID), ResourceVersion: "1"}, Immutable: &yes, Data: map[string]string{"policySHA256": p.Digest()}}, Common: a, Header: h, Templates: tmpl, Snapshot: saved, Rules: rules}
	if p.Validate() != nil || p.ValidateTemplate(tmpl) != nil {
		t.Fatal("invalid synthetic pilot fixture")
	}
	return f
}
func (f PilotFixture) Objects() []client.Object {
	return []client.Object{f.Catalog, f.Template, f.Seal, f.Common, f.Header}
}
func (f PilotFixture) Session(t *testing.T, slot int) *v1alpha1.AgentSession {
	t.Helper()
	req := f.Policy.Request(slot)
	binding, err := f.Rules.Binding()
	if err != nil {
		t.Fatal(err)
	}
	return &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: projectcanary.PilotSessionName(slot), UID: types.UID([]string{"22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[slot]), Generation: 1, CreationTimestamp: metav1.NewTime(f.Now), Finalizers: []string{projectcanary.PilotFinalizer}, Labels: map[string]string{v1alpha1.LabelIdempotencyKey: projectcanary.PilotKey(slot)}, Annotations: map[string]string{projectcanary.PilotAnnotation: f.Policy.Binding(slot), projectcanary.PilotPolicyAnnotation: f.Policy.Record(projectcanary.PilotEvidence{CatalogUID: string(f.Catalog.UID), CatalogResourceVersion: f.Catalog.ResourceVersion}), v1alpha1.AnnotationProjectSnapshot: string(f.Snapshot)}}, Spec: v1alpha1.AgentSessionSpec{PrivatePilot: f.Policy.SpecBinding(slot), Repo: "dev-env", Base: "main", Agent: v1alpha1.AgentCodex, Mode: v1alpha1.ModeTask, Model: req.Model, Effort: req.Effort, Prompt: req.Prompt, Parent: projectcanary.PilotCaller, Profile: projectcanary.PilotProfile, Size: v1alpha1.SizeS, OperatingMode: v1alpha1.OperatingModeRunning, Limits: &v1alpha1.SessionLimits{Timeout: &metav1.Duration{Duration: 15 * time.Minute}}, CommonRules: binding, SessionDirectory: &v1alpha1.SessionDirectoryEnrollment{Version: 1, Project: "dev-env"}}}
}
