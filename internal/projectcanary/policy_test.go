package projectcanary_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestPolicyStrictFiniteRecords(t *testing.T) {
	claude := testutil.New(t, "claude", "dev/dev-env").Record
	codex := testutil.New(t, "codex", "dev/dev-env").Record
	doc, _ := json.Marshal([]projectcanary.Record{claude, codex})
	p, err := projectcanary.Parse(string(doc))
	if err != nil || p.ValidateClients([]string{claude.Client}, true) != nil {
		t.Fatalf("valid two-provider policy: %v", err)
	}
	if p.ValidateClients([]string{claude.Client}, false) == nil || p.ValidateClients([]string{"other/client"}, true) == nil {
		t.Fatal("missing provider/client prerequisite accepted")
	}
	if p.ValidateClients([]string{claude.Client}, true, claude.Client) == nil {
		t.Fatal("reserved session/human identity accepted as canary client")
	}
	if p, err := projectcanary.Parse(""); err != nil || p != nil {
		t.Fatal("policy enabled by default")
	}
	for _, raw := range []string{"[]", "null", string(doc) + "{}", strings.Replace(string(doc), `"id":`, `"unknown":0,"id":`, 1), strings.Replace(string(doc), `"id":`, `"ID":`, 1), strings.Replace(string(doc), `"id":`, `"id":"duplicate","id":`, 1), strings.Repeat(" ", projectcanary.MaxPolicyBytes+1)} {
		if _, err := projectcanary.Parse(raw); err == nil {
			t.Fatal("ambiguous/unbounded policy accepted")
		}
	}
	for _, change := range []func(*projectcanary.Record){func(r *projectcanary.Record) { r.ID = "*" }, func(r *projectcanary.Record) { r.Client = "*/client" }, func(r *projectcanary.Record) { r.Timeout = "181s" }, func(r *projectcanary.Record) { r.Timeout = "0s" }, func(r *projectcanary.Record) { r.MaxTurns = 4 }, func(r *projectcanary.Record) { r.Profile = "full" }, func(r *projectcanary.Record) { r.Image = "ghcr.io/thaynes43/dev-env:latest" }, func(r *projectcanary.Record) { r.Prompt = strings.Repeat("p", projectcanary.MaxPromptBytes+1) }} {
		r := claude
		change(&r)
		bad, _ := json.Marshal([]projectcanary.Record{r})
		if _, err := projectcanary.Parse(string(bad)); err == nil {
			t.Fatalf("invalid record accepted: %+v", r)
		}
	}
	for _, records := range [][]projectcanary.Record{{claude, claude}, {claude, codex, claude}} {
		bad, _ := json.Marshal(records)
		if _, err := projectcanary.Parse(string(bad)); err == nil {
			t.Fatal("duplicate/unbounded attempts accepted")
		}
	}
	r := claude
	r.Prompt += " Changed reviewed instruction."
	if r.SessionName() != claude.SessionName() || r.Digest() == claude.Digest() {
		t.Fatal("policy revision rearmed deterministic identity or lost binding")
	}
	r = claude
	r.Timeout = "3m0s"
	if r.Digest() != claude.Digest() {
		t.Fatal("equivalent timeout changed immutable policy digest")
	}
}

func TestTemplateAndCatalogAuthorityRefuseDrift(t *testing.T) {
	f := testutil.New(t, "codex", "dev/dev-env")
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.Catalog).Build()
	if _, err := projectcanary.ResolveCatalog(context.Background(), c, testutil.Owner, f.Record); err != nil {
		t.Fatal(err)
	}
	if _, err := projectcanary.ResolveCatalog(context.Background(), c, "foreign", f.Record); err == nil {
		t.Fatal("foreign clone authority accepted")
	}
	changed := f.Record
	changed.RulesRevision = strings.Repeat("0", 64)
	if _, err := projectcanary.ResolveCatalog(context.Background(), c, testutil.Owner, changed); err == nil {
		t.Fatal("rule drift accepted")
	}
	for _, mutate := range []func(*templates.Templates){func(t *templates.Templates) { t.Env = append(t.Env, corev1.EnvVar{Name: "GIT_DIR", Value: "/outside"}) }, func(t *templates.Templates) {
		p := t.Profiles["canary"]
		p.EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "arbitrary"}}}}
		t.Profiles["canary"] = p
	}, func(t *templates.Templates) {
		p := t.Profiles["canary"]
		p.Mounts = p.Mounts[:1]
		t.Profiles["canary"] = p
	}, func(t *templates.Templates) {
		s := t.Sizes[v1alpha1.SizeS]
		delete(s.Limits, corev1.ResourceCPU)
		t.Sizes[v1alpha1.SizeS] = s
	}, func(t *templates.Templates) {
		s := t.Sizes[v1alpha1.SizeS]
		s.Limits[corev1.ResourceCPU] = resource.MustParse("3")
		t.Sizes[v1alpha1.SizeS] = s
	}, func(t *templates.Templates) {
		t.Workspace = &templates.Workspace{Enabled: true, Claim: "shared", ID: "shared"}
	}} {
		fresh := testutil.New(t, "codex", "dev/dev-env")
		mutate(fresh.Templates)
		if fresh.Record.ValidateTemplate(fresh.Templates, testutil.Owner) == nil {
			t.Fatal("unsafe private profile accepted")
		}
	}
}

// TestRetainedBindingRefusesAlteredOriginal proves the binding half: the
// original canary session matches its record, and a caller-changed prompt or
// any other altered field does not. A reviewed prompt that repeats the bound
// rules is refused by ResolveCatalog and CheckCommon
// (TestPromptRepeatingBoundRulesIsRefused), and the rendering adds none
// (controller TestProjectCanaryRenderedPromptAndArgsCarryNoRuleLine, agentd
// TestProjectRulesReachTheAgentOnlyAsRuleInputs).
func TestRetainedBindingRefusesAlteredOriginal(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	if f.Record.MatchSession(f.Session, testutil.Owner) != nil {
		t.Fatal("original retained binding refused")
	}
	if _, err := projectcanary.BoundRecord(f.Session); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*v1alpha1.AgentSession){func(s *v1alpha1.AgentSession) { s.Spec.Prompt += "caller override" }, func(s *v1alpha1.AgentSession) {
		s.Spec.ProjectCanary.CatalogUID = "replacement"
		s.Annotations[v1alpha1.AnnotationProjectSnapshot] += " "
	}, func(s *v1alpha1.AgentSession) { s.Finalizers = nil }, func(s *v1alpha1.AgentSession) { s.Spec.Limits.MaxTurns = 4 }, func(s *v1alpha1.AgentSession) { s.Spec.Restore = "archive" }, func(s *v1alpha1.AgentSession) { s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "shared"} }} {
		s := f.Session.DeepCopy()
		mutate(s)
		if f.Record.MatchSession(s, testutil.Owner) == nil {
			t.Fatal("altered original canary accepted")
		}
	}
}

// TestPromptRepeatingBoundRulesIsRefused proves DESIGN-011's "the fixed prompt
// does not contain validation marker values" on reviewed records: a prompt that
// repeats a bound project rule line, or a marker of the rules even
// mid-sentence, is refused by ResolveCatalog, which the API, the controller
// and the result reader all call, and a prompt that repeats the retained common
// text or Codex header is refused by CheckCommon.
func TestPromptRepeatingBoundRulesIsRefused(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.Catalog).Build()
	if _, err := projectcanary.ResolveCatalog(context.Background(), c, testutil.Owner, f.Record); err != nil {
		t.Fatal("the fixture's prompt was refused:", err)
	}
	for _, prompt := range []string{"Reply with COMMON_RULE_MARKER", "Do this: Follow the accepted rules. Then stop."} {
		r := f.Record
		r.Prompt = prompt
		if _, err := projectcanary.ResolveCatalog(context.Background(), c, testutil.Owner, r); !errors.Is(err, projectcanary.ErrRefused) || !errors.Is(err, projectcanary.ErrPromptRepeatsRules) {
			t.Errorf("prompt %q repeating the bound rules: %v, want ErrPromptRepeatsRules", prompt, err)
		}
	}

	// The live repository marker sits mid-sentence in its rule line, so a bare
	// copy of it repeats no whole line; it is refused as a marker. Wrapped prose
	// tails and ordinary hyphenated words are not markers.
	rules := "## Repository instruction acceptance\n\nFor an explicit instruction acceptance probe, report the repository marker\n`dev-env-repository-rules-20261011-6d807bf2`. Keep the work self-contained in this\nsession.\n"
	live := "Perform the instruction acceptance probe described in your loaded instructions. Reply only with the common and repository rule markers you actually received, and Unknown for absent instructions. Do not use tools or fetch files."
	if err := projectcanary.CheckPrompt(live, rules); err != nil {
		t.Errorf("a prompt that names no marker was refused: %v", err)
	}
	for _, prompt := range []string{"Reply with dev-env-repository-rules-20261011-6d807bf2.", "Echo the probe: For an explicit instruction acceptance probe, report the repository marker", "Reply with COMMON_ONLY_MARKER."} {
		if err := projectcanary.CheckPrompt(prompt, "unrelated", rules+"\nThe common marker is COMMON_ONLY_MARKER today."); !errors.Is(err, projectcanary.ErrPromptRepeatsRules) {
			t.Errorf("prompt %q: %v, want ErrPromptRepeatsRules", prompt, err)
		}
	}
	for _, prompt := range []string{"Report this session.", "Keep it self-contained.", "Reply with dev-env."} {
		if err := projectcanary.CheckPrompt(prompt, rules); err != nil {
			t.Errorf("prompt %q matched no marker or whole line, but was refused: %v", prompt, err)
		}
	}

	common := commonfixture.Snapshot(t, []byte(f.Session.Annotations[v1alpha1.AnnotationProjectSnapshot]), "COMMON_ONLY_MARKER\nShared rules for every session.", "HEADER_ONLY_MARKER\nCodex reads this header first.")
	if err := f.Record.CheckCommon(common); err != nil {
		t.Fatal("the fixture's prompt was refused against unrelated common text:", err)
	}
	for _, prompt := range []string{"Reply with COMMON_ONLY_MARKER", "Reply with HEADER_ONLY_MARKER", "Note: Codex reads this header first."} {
		r := f.Record
		r.Prompt = prompt
		if err := r.CheckCommon(common); !errors.Is(err, projectcanary.ErrPromptRepeatsRules) {
			t.Errorf("prompt %q repeating common or header text: %v, want ErrPromptRepeatsRules", prompt, err)
		}
	}
}
