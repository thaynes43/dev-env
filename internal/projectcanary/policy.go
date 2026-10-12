// Package projectcanary validates the two finite operator-owned managed rule
// canaries. It grants no general private project or native-host admission.
package projectcanary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kjson "sigs.k8s.io/json"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/templates"
)

const (
	MaxPolicyBytes                = 64 << 10
	MaxPromptBytes                = 4096
	ActiveDeadlineSeconds   int64 = 600
	TerminationGraceSeconds int64 = 5
	CatalogNamespace              = "dev-env-system"
	CatalogName                   = "dev-env-project-catalog"
	CodexAccessPath               = "/opt/dev-env/codex-access"
	CodexAccessSecret             = "dev-env-codex-live"
)

var ErrRefused = errors.New("private project canary policy or original authority is unconfirmed")

// ErrPromptRepeatsRules refuses a reviewed record whose fixed prompt repeats
// bound instruction text. The canary's evidence is the agent reporting the rule
// markers it loaded, so a marker the prompt supplies proves nothing (DESIGN-011).
var ErrPromptRepeatsRules = fmt.Errorf("%w: the fixed prompt repeats bound rule text", ErrRefused)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var imagePattern = regexp.MustCompile(`^ghcr\.io/thaynes43/dev-env:[A-Za-z0-9_.-]+@sha256:[a-f0-9]{64}$`)
var templateRevisionPattern = regexp.MustCompile(`^(?:[A-Za-z0-9_.-]+-)?[a-f0-9]{10}$`)

// Record is trusted operator configuration. Requests supply only its ID.
type Record struct {
	CommonRevision   string `json:"commonRevision,omitempty"`
	CaptureOutput    bool   `json:"captureOutput,omitempty"`
	ID               string `json:"id"`
	Client           string `json:"client"`
	Project          string `json:"project"`
	Repository       string `json:"repository"`
	Provider         string `json:"provider"`
	Profile          string `json:"profile"`
	Model            string `json:"model"`
	Effort           string `json:"effort"`
	Size             string `json:"size"`
	CatalogRevision  string `json:"catalogRevision"`
	RulesRevision    string `json:"rulesRevision"`
	TemplateRevision string `json:"templateRevision"`
	Image            string `json:"image"`
	IdempotencyKey   string `json:"idempotencyKey"`
	Prompt           string `json:"prompt"`
	Timeout          string `json:"timeout"`
	MaxTurns         int32  `json:"maxTurns"`
}

type Policy struct{ records []Record }

// Parse is bounded, case-sensitive and rejects duplicate/unknown fields.
func Parse(raw string) (*Policy, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > MaxPolicyBytes || !utf8.ValidString(raw) {
		return nil, ErrRefused
	}
	var records []Record
	strict, err := kjson.UnmarshalStrict([]byte(raw), &records)
	if err != nil || len(strict) != 0 || len(records) < 1 || len(records) > 2 {
		return nil, ErrRefused
	}
	ids, providers, keys := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range records {
		key := r.Client + "\x00" + r.IdempotencyKey
		if r.Validate() != nil || ids[r.ID] || providers[r.Provider] || keys[key] {
			return nil, ErrRefused
		}
		ids[r.ID], providers[r.Provider], keys[key] = true, true, true
	}
	return &Policy{records: records}, nil
}

func (p *Policy) Lookup(id string) (Record, bool) {
	if p != nil {
		for _, r := range p.records {
			if r.ID == id {
				return r, true
			}
		}
	}
	return Record{}, false
}

func (p *Policy) ValidateClients(clients []string, managedCodex bool, reserved ...string) error {
	if p == nil || len(p.records) == 0 {
		return ErrRefused
	}
	for _, r := range p.records {
		if !slices.Contains(clients, r.Client) || slices.Contains(reserved, r.Client) || r.Provider == "codex" && !managedCodex {
			return ErrRefused
		}
	}
	return nil
}

func (r Record) Validate() error {
	if r.CommonRevision != "" && !commonrules.ValidDigest(r.CommonRevision) {
		return ErrRefused
	}
	parts := strings.Split(r.Client, "/")
	if len(parts) != 2 || !projectcatalog.ValidName(parts[0]) || !projectcatalog.ValidName(parts[1]) || !projectcatalog.ValidName(r.ID) || !projectcatalog.ValidName(r.Project) || !projectcatalog.ValidName(r.Repository) || !projectcatalog.ValidName(r.Profile) || r.Profile == "full" || r.Size != "S" || len(validation.IsValidLabelValue(r.IdempotencyKey)) != 0 || r.IdempotencyKey == "" || !digestPattern.MatchString(r.CatalogRevision) || !digestPattern.MatchString(r.RulesRevision) || len(r.TemplateRevision) > 63 || !templateRevisionPattern.MatchString(r.TemplateRevision) || len(r.Image) > 512 || !imagePattern.MatchString(r.Image) || len(r.Prompt) == 0 || len(r.Prompt) > MaxPromptBytes || !utf8.ValidString(r.Prompt) || strings.IndexByte(r.Prompt, 0) >= 0 {
		return ErrRefused
	}
	d, err := time.ParseDuration(r.Timeout)
	if err != nil || d <= 0 || d > 180*time.Second {
		return ErrRefused
	}
	switch r.Provider {
	case "claude":
		if protocol.ValidateClaudeModel(r.Model) != nil || r.Effort != "" && !apiv1.ClaudeEffortAccepted(r.Model, r.Effort) || r.MaxTurns < 1 || r.MaxTurns > 3 {
			return ErrRefused
		}
	case "codex":
		if protocol.ValidateCodexModel(r.Model) != nil || r.Effort != "" && protocol.ValidateCodexEffort(r.Model, r.Effort) != nil || r.MaxTurns != 0 {
			return ErrRefused
		}
	default:
		return ErrRefused
	}
	return nil
}

func (r Record) Digest() string {
	if d, err := time.ParseDuration(r.Timeout); err == nil {
		r.Timeout = d.String()
	}
	data, _ := json.Marshal(r)
	return projectcatalog.Digest(data)
}
func (r Record) SessionName() string {
	return "private-canary-" + projectcatalog.Digest([]byte(r.Client + "\x00" + r.ID))[:32]
}

type CatalogEvidence struct {
	Snapshot        []byte
	UID             string
	ResourceVersion string
}

// ResolveCatalog always reads only the accepted named API ConfigMap. It refuses
// a record whose prompt repeats the bound project rules (CheckPrompt), so the
// API, the controller and the result reader all refuse one.
func ResolveCatalog(ctx context.Context, reader client.Reader, owner string, r Record) (CatalogEvidence, error) {
	var out CatalogEvidence
	if reader == nil || !projectcatalog.ValidName(owner) || r.Validate() != nil {
		return out, ErrRefused
	}
	var cm corev1.ConfigMap
	if reader.Get(ctx, types.NamespacedName{Namespace: CatalogNamespace, Name: CatalogName}, &cm) != nil || !cm.DeletionTimestamp.IsZero() || cm.UID == "" || len(cm.UID) > 128 || cm.ResourceVersion == "" || len(cm.ResourceVersion) > 128 {
		return out, ErrRefused
	}
	catalog, err := projectcatalog.Parse([]byte(cm.Data["catalog.json"]))
	if err != nil || catalog.Revision() != r.CatalogRevision {
		return out, ErrRefused
	}
	snapshot, err := catalog.Snapshot(r.Project, r.Repository)
	if err != nil || snapshot.RulesRevision() != r.RulesRevision || strings.TrimSpace(snapshot.Rules()) == "" || strings.Split(snapshot.Selected().GitHub, "/")[0] != owner {
		return out, ErrRefused
	}
	if err := CheckPrompt(r.Prompt, snapshot.Rules()); err != nil {
		return out, err
	}
	out.Snapshot, err = json.Marshal(snapshot)
	if err != nil || len(out.Snapshot) > projectcatalog.MaxSnapshotBytes {
		return CatalogEvidence{}, ErrRefused
	}
	out.UID, out.ResourceVersion = string(cm.UID), cm.ResourceVersion
	return out, nil
}

// minRuleLine is the shortest trimmed rule line CheckPrompt compares whole.
// Shorter lines are mostly the tails of wrapped prose ("session.", "PR."), which
// carry no instruction of their own; markers are caught as tokens instead.
const minRuleLine = 16

// ruleToken is a run of the characters a rule marker is made of.
var ruleToken = regexp.MustCompile(`[A-Za-z0-9_-]+`)

// CheckPrompt refuses a fixed prompt that repeats instruction text it must not
// supply (DESIGN-011, DESIGN-015): a trimmed line of any of texts at least
// minRuleLine bytes long, or a marker-shaped token of them, even mid-sentence.
// A false match only refuses a reviewed record, which is then reworded.
func CheckPrompt(prompt string, texts ...string) error {
	for _, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimSpace(line); len(line) >= minRuleLine && strings.Contains(prompt, line) {
				return ErrPromptRepeatsRules
			}
		}
		for _, token := range ruleToken.FindAllString(text, -1) {
			if markerShaped(token) && strings.Contains(prompt, token) {
				return ErrPromptRepeatsRules
			}
		}
	}
	return nil
}

// markerShaped reports whether a rule token looks like a value an acceptance
// probe asks for: at least 12 characters, joined by '-' or '_', with a letter,
// and with a digit or no lowercase letter. That covers
// dev-env-repository-rules-20261011-6d807bf2 and COMMON_RULE_MARKER, and leaves
// prose such as self-contained alone.
func markerShaped(token string) bool {
	return len(token) >= 12 && strings.ContainsAny(token, "-_") && strings.IndexFunc(token, unicode.IsLetter) >= 0 && (strings.ContainsAny(token, "0123456789") || strings.IndexFunc(token, unicode.IsLower) < 0)
}

// CheckCommon refuses a record whose prompt repeats its retained common or
// Codex header instruction text, as ResolveCatalog does for the project rules.
func (r Record) CheckCommon(common commonrules.Snapshot) error {
	return CheckPrompt(r.Prompt, common.Common.Text, common.Header.Text)
}

func ReadTemplates(ctx context.Context, reader client.Reader, key types.NamespacedName) (*templates.Templates, error) {
	if reader == nil || key.Namespace == "" || key.Name == "" {
		return nil, ErrRefused
	}
	var cm corev1.ConfigMap
	if reader.Get(ctx, key, &cm) != nil || !cm.DeletionTimestamp.IsZero() {
		return nil, ErrRefused
	}
	t, err := templates.Parse(cm.Data)
	if err != nil {
		return nil, ErrRefused
	}
	return t, nil
}

// ValidateTemplate checks both reviewed exact data and the finite private shape.
// Signature verification belongs to publication and policy review; the trusted
// record names that immutable image, never a caller-selected image.
func (r Record) ValidateTemplate(t *templates.Templates, owner string) error {
	if t == nil || r.Validate() != nil || t.Revision() != r.TemplateRevision || t.Image != r.Image || t.Workspace != nil && t.Workspace.Enabled {
		return ErrRefused
	}
	_, profile, err := t.Profile(r.Profile)
	if err != nil || len(profile.EnvFrom) != 0 || len(profile.Labels) != 0 {
		return ErrRefused
	}
	values := map[string]string{}
	allowed := []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "DISABLE_AUTOUPDATER", "DEV_ENV_CLAUDE_MODEL", "AGENTD_API_CA_FILE", "AGENTD_ENABLE_PRIVATE_PROJECT_BOOTSTRAP", "AGENTD_ENABLE_PRIVATE_PROJECT_ROOTS", "AGENTD_PROJECT_CATALOG", "AGENTD_PROJECT_CLONE_OWNER", "AGENTD_CODEX_ACCESS_FILE"}
	for _, env := range append(slices.Clone(t.Env), profile.Env...) {
		if env.ValueFrom != nil || !slices.Contains(allowed, env.Name) {
			return ErrRefused
		}
		if _, duplicate := values[env.Name]; duplicate {
			return ErrRefused
		}
		values[env.Name] = env.Value
	}
	for key, want := range map[string]string{"CLAUDE_CONFIG_DIR": "/home/dev/.claude", "CODEX_HOME": "/home/dev/.codex", "DISABLE_AUTOUPDATER": "1", "AGENTD_ENABLE_PRIVATE_PROJECT_BOOTSTRAP": "true", "AGENTD_ENABLE_PRIVATE_PROJECT_ROOTS": "true", "AGENTD_PROJECT_CATALOG": CatalogNamespace + "/" + CatalogName, "AGENTD_PROJECT_CLONE_OWNER": owner} {
		if values[key] != want {
			return ErrRefused
		}
	}
	if !projectcatalog.ValidName(owner) {
		return ErrRefused
	}
	if r.Provider == "codex" && values["AGENTD_CODEX_ACCESS_FILE"] != CodexAccessPath+"/access.json" || r.Provider == "claude" && values["AGENTD_CODEX_ACCESS_FILE"] != "" {
		return ErrRefused
	}
	allowedMounts := map[string]string{"/opt/dev-env/config/claude": "dev-env-config-claude", "/opt/dev-env/config/codex": "dev-env-config-codex", "/opt/dev-env/scripts": "dev-env-scripts", "/etc/codex": "dev-env-codex-requirements", "/opt/dev-env/api-ca": "dev-env-api-ca"}
	for _, m := range t.Mounts {
		if allowedMounts[m.Path] == "" || m.ConfigMap != allowedMounts[m.Path] || m.Secret != "" {
			return ErrRefused
		}
		delete(allowedMounts, m.Path)
	}
	if len(allowedMounts) != 0 || values["AGENTD_API_CA_FILE"] != "/opt/dev-env/api-ca/ca.crt" {
		return ErrRefused
	}
	gh, access := false, false
	for _, m := range profile.Mounts {
		switch m.Path {
		case "/creds":
			if gh || m.ConfigMap != "" || m.Secret != "dev-env-gh-token" {
				return ErrRefused
			}
			gh = true
		case CodexAccessPath:
			if access || r.Provider != "codex" || m.ConfigMap != "" || m.Secret != CodexAccessSecret {
				return ErrRefused
			}
			access = true
		default:
			return ErrRefused
		}
	}
	if !gh || r.Provider == "codex" && !access || r.Provider == "claude" && (t.Claude.StaticToken.Name != "dev-env-claude-secret" || t.Claude.StaticToken.Key != "CLAUDE_CODE_OAUTH_TOKEN") {
		return ErrRefused
	}
	size, err := t.Size(v1alpha1.SizeS)
	if err != nil {
		return ErrRefused
	}
	for name, cap := range map[corev1.ResourceName]resource.Quantity{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")} {
		req, hasReq := size.Requests[name]
		limit, hasLimit := size.Limits[name]
		if !hasReq || !hasLimit || req.Sign() <= 0 || limit.Sign() <= 0 || req.Cmp(limit) > 0 || limit.Cmp(cap) > 0 {
			return ErrRefused
		}
	}
	return nil
}

// BoundRecord reconstructs the exact retained policy projection for the pure
// renderer. Actual controller admission also requires the independently supplied
// enabled policy and current uncached catalog/template authority.
func BoundRecord(s *v1alpha1.AgentSession) (Record, error) {
	if s == nil || s.Spec.ProjectCanary == nil || s.Spec.Limits == nil || s.Spec.Limits.Timeout == nil {
		return Record{}, ErrRefused
	}
	b := s.Spec.ProjectCanary
	snapshot, err := projectcatalog.ParseSnapshot([]byte(s.Annotations[v1alpha1.AnnotationProjectSnapshot]))
	if err != nil {
		return Record{}, ErrRefused
	}
	r := Record{CaptureOutput: b.CaptureOutput, CommonRevision: b.CommonRevision, ID: b.ID, Client: b.Client, Project: snapshot.Project(), Repository: s.Spec.Repo, Provider: string(s.Spec.Agent), Profile: s.Spec.Profile, Model: s.Spec.Model, Effort: s.Spec.Effort, Size: string(s.Spec.Size), CatalogRevision: b.CatalogRevision, RulesRevision: b.RulesRevision, TemplateRevision: b.TemplateRevision, Image: b.Image, IdempotencyKey: s.Labels[v1alpha1.LabelIdempotencyKey], Prompt: s.Spec.Prompt, Timeout: s.Spec.Limits.Timeout.Duration.String(), MaxTurns: s.Spec.Limits.MaxTurns}
	if r.MatchSession(s, b.CloneOwner) != nil {
		return Record{}, ErrRefused
	}
	return r, nil
}

func (r Record) Binding(e CatalogEvidence, owner string) *v1alpha1.ProjectCanaryBinding {
	return &v1alpha1.ProjectCanaryBinding{CaptureOutput: r.CaptureOutput, CommonRevision: r.CommonRevision, Version: 1, ID: r.ID, Client: r.Client, PolicyDigest: r.Digest(), SnapshotDigest: projectcatalog.Digest(e.Snapshot), CatalogUID: e.UID, CatalogResourceVersion: e.ResourceVersion, CatalogRevision: r.CatalogRevision, RulesRevision: r.RulesRevision, TemplateRevision: r.TemplateRevision, Image: r.Image, CloneOwner: owner}
}

// MatchSession uses only retained immutable inputs, so a terminal retry needs
// no fresh fetch or provider call and can never request another executor.
func (r Record) MatchSession(s *v1alpha1.AgentSession, owner string) error {
	if s == nil || r.Validate() != nil || s.Spec.ProjectCanary == nil {
		return ErrRefused
	}
	b := s.Spec.ProjectCanary
	if b.CommonRevision != r.CommonRevision {
		return ErrRefused
	}
	if r.CommonRevision == "" {
		if s.Spec.CommonRules != nil {
			return ErrRefused
		}
	} else {
		snapshot, err := commonrules.BoundSession(s)
		if err != nil || snapshot.CommonRevision != r.CommonRevision {
			return ErrRefused
		}
	}
	if b.CaptureOutput != r.CaptureOutput || b.Version != 1 || b.ID != r.ID || b.Client != r.Client || b.PolicyDigest != r.Digest() || b.TemplateRevision != r.TemplateRevision || b.Image != r.Image || b.CloneOwner != owner || b.CatalogRevision != r.CatalogRevision || b.RulesRevision != r.RulesRevision || b.CatalogUID == "" || b.CatalogResourceVersion == "" || s.Name != r.SessionName() || !slices.Contains(s.Finalizers, v1alpha1.ProjectCanaryFinalizer) || s.Spec.Parent != r.Client || s.Spec.Agent != v1alpha1.AgentKind(r.Provider) || s.Spec.Mode != v1alpha1.ModeTask || s.Spec.Repo != r.Repository || s.Spec.Model != r.Model || s.Spec.Effort != r.Effort || s.Spec.Profile != r.Profile || s.Spec.Size != v1alpha1.SizeS || s.Spec.Prompt != r.Prompt || s.Spec.Workspace != nil || s.Spec.Restore != "" || len(s.Spec.Tools) != 0 || s.Spec.LLM != nil || s.Spec.Caller != "" || s.Spec.Lane != "" || s.Spec.IdempotencyKey != "" || s.Spec.Lifecycle != nil || s.Labels[v1alpha1.LabelIdempotencyKey] != r.IdempotencyKey {
		return ErrRefused
	}
	d, _ := time.ParseDuration(r.Timeout)
	if s.Spec.Limits == nil || s.Spec.Limits.Timeout == nil || s.Spec.Limits.Timeout.Duration != d || s.Spec.Limits.MaxTurns != r.MaxTurns {
		return ErrRefused
	}
	raw := []byte(s.Annotations[v1alpha1.AnnotationProjectSnapshot])
	snapshot, err := projectcatalog.ParseSnapshot(raw)
	if err != nil || b.SnapshotDigest != projectcatalog.Digest(raw) || snapshot.Project() != r.Project || snapshot.CatalogRevision() != r.CatalogRevision || snapshot.RulesRevision() != r.RulesRevision || snapshot.Selected().Name != r.Repository || snapshot.Selected().DefaultBranch != s.Spec.Base || strings.Split(snapshot.Selected().GitHub, "/")[0] != owner {
		return ErrRefused
	}
	return nil
}

// ValidateCommon keeps this route independent and disabled without exact revisions.
func (p *Policy) ValidateCommon(enabled bool) error {
	if p == nil {
		if enabled {
			return ErrRefused
		}
		return nil
	}
	for _, r := range p.records {
		if enabled != (r.CommonRevision != "") {
			return ErrRefused
		}
	}
	return nil
}

func (p *Policy) OutputCaptureEnabled() bool {
	if p == nil {
		return false
	}
	for _, r := range p.records {
		if r.CaptureOutput {
			return true
		}
	}
	return false
}
