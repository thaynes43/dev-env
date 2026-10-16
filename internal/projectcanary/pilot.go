package projectcanary

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
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
	PilotActiveDeadlineSeconds int64 = 1200
	PilotCaller                      = "dev/dev-env"
	PilotProject                     = "dev-env"
	PilotProfile                     = "rule-canary-codex"
	PilotSealNamespace               = "dev-env-budgets"
	PilotSealName                    = "dev-env-private-pilot-v1"
	PilotAnnotation                  = v1alpha1.LabelPrefix + "private-pilot"
	PilotFinalizer                   = v1alpha1.LabelPrefix + "private-pilot-retained"
)

// PilotPolicy is one reviewed, sealed window, never a rolling allowance. Only
// prompts and pinned authority/time vary; scope, two slots and limits are fixed.
const PilotPolicyAnnotation = v1alpha1.LabelPrefix + "private-pilot-policy"

type PilotPolicy struct {
	NotBefore        time.Time `json:"notBefore"`
	ExpiresAt        time.Time `json:"expiresAt"`
	SealUID          string    `json:"sealUID"`
	CatalogRevision  string    `json:"catalogRevision"`
	RulesRevision    string    `json:"rulesRevision"`
	CommonRevision   string    `json:"commonRevision"`
	TemplateRevision string    `json:"templateRevision"`
	Image            string    `json:"image"`
	Prompts          []string  `json:"prompts"`
}

func ParsePilot(raw string) (*PilotPolicy, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > MaxPolicyBytes || !utf8.ValidString(raw) {
		return nil, ErrRefused
	}
	var p PilotPolicy
	strict, err := kjson.UnmarshalStrict([]byte(raw), &p)
	if err != nil || len(strict) != 0 || p.Validate() != nil {
		return nil, ErrRefused
	}
	return &p, nil
}
func (p *PilotPolicy) Validate() error {
	if p == nil || p.NotBefore.IsZero() || !p.ExpiresAt.After(p.NotBefore) || p.ExpiresAt.Sub(p.NotBefore) > 30*time.Minute || !protocol.ValidKubeUID(p.SealUID) || !digestPattern.MatchString(p.CatalogRevision) || !digestPattern.MatchString(p.RulesRevision) || !commonrules.ValidDigest(p.CommonRevision) || !templateRevisionPattern.MatchString(p.TemplateRevision) || !imagePattern.MatchString(p.Image) || len(p.Prompts) != 2 {
		return ErrRefused
	}
	for _, t := range []time.Time{p.NotBefore, p.ExpiresAt} {
		_, offset := t.Zone()
		if offset != 0 {
			return ErrRefused
		}
	}
	for _, prompt := range p.Prompts {
		if len(prompt) == 0 || len(prompt) > MaxPromptBytes || !utf8.ValidString(prompt) || strings.IndexByte(prompt, 0) >= 0 {
			return ErrRefused
		}
	}
	return nil
}
func (p *PilotPolicy) Open(now time.Time) bool {
	return p.Validate() == nil && !now.Before(p.NotBefore) && now.Before(p.ExpiresAt)
}
func (p *PilotPolicy) Digest() string {
	copy := *p
	copy.SealUID = ""
	copy.NotBefore = copy.NotBefore.UTC()
	copy.ExpiresAt = copy.ExpiresAt.UTC()
	data, _ := json.Marshal(copy)
	return projectcatalog.Digest(data)
}
func PilotSessionName(slot int) string {
	if slot == 0 {
		return "private-pilot-v1-a"
	}
	if slot == 1 {
		return "private-pilot-v1-b"
	}
	return ""
}
func PilotKey(slot int) string {
	if slot == 0 {
		return "c7-private-pilot-v1-a"
	}
	if slot == 1 {
		return "c7-private-pilot-v1-b"
	}
	return ""
}
func IsPilot(s *v1alpha1.AgentSession) bool {
	_, marked := s.Annotations[PilotAnnotation]
	return marked || slices.Contains(s.Finalizers, PilotFinalizer) || s.Name == PilotSessionName(0) || s.Name == PilotSessionName(1)
}
func (p *PilotPolicy) Request(slot int) apiv1.CreateSessionRequest {
	if slot < 0 || slot > 1 {
		return apiv1.CreateSessionRequest{}
	}
	return apiv1.CreateSessionRequest{Project: PilotProject, Repo: PilotProject, Agent: "codex", Mode: "task", Model: "gpt-6.1-sol", Effort: "high", Prompt: p.Prompts[slot], Size: "S", Profile: PilotProfile, IdempotencyKey: PilotKey(slot), Limits: &apiv1.Limits{Timeout: "15m"}}
}
func (p *PilotPolicy) Slot(req apiv1.CreateSessionRequest) (int, bool) {
	if p.Validate() != nil {
		return 0, false
	}
	data, _ := json.Marshal(req)
	for i := range 2 {
		expected, _ := json.Marshal(p.Request(i))
		if string(data) == string(expected) {
			return i, true
		}
	}
	return 0, false
}
func (p *PilotPolicy) Binding(slot int) string {
	return p.SealUID + ":" + p.Digest() + ":" + PilotKey(slot)
}

// PilotEvidence is the existing common/catalog/template composition. No secret,
// provider file, mutable lease or service-created authority is introduced.
type PilotEvidence struct {
	CatalogUID             string
	CatalogResourceVersion string
	Snapshot               []byte
	Common                 commonrules.Snapshot
	Templates              *templates.Templates
}

func (p *PilotPolicy) Resolve(ctx context.Context, reader client.Reader, templateKey types.NamespacedName, now time.Time) (PilotEvidence, error) {
	var out PilotEvidence
	if reader == nil || !p.Open(now) || templateKey != (types.NamespacedName{Namespace: "dev-env-system", Name: templates.DefaultName}) {
		return out, ErrRefused
	}
	var seal corev1.ConfigMap
	if reader.Get(ctx, types.NamespacedName{Namespace: PilotSealNamespace, Name: PilotSealName}, &seal) != nil || seal.Namespace != PilotSealNamespace || seal.Name != PilotSealName || string(seal.UID) != p.SealUID || seal.ResourceVersion == "" || !seal.DeletionTimestamp.IsZero() || seal.Immutable == nil || !*seal.Immutable || len(seal.OwnerReferences) != 0 || len(seal.BinaryData) != 0 || len(seal.Data) != 1 || seal.Data["policySHA256"] != p.Digest() {
		return out, ErrRefused
	}
	var cm corev1.ConfigMap
	if reader.Get(ctx, types.NamespacedName{Namespace: CatalogNamespace, Name: CatalogName}, &cm) != nil || cm.Namespace != CatalogNamespace || cm.Name != CatalogName || cm.UID == "" || cm.ResourceVersion == "" || !cm.DeletionTimestamp.IsZero() {
		return out, ErrRefused
	}
	catalog, err := projectcatalog.Parse([]byte(cm.Data["catalog.json"]))
	if err != nil || catalog.Revision() != p.CatalogRevision {
		return out, ErrRefused
	}
	snapshot, err := catalog.Snapshot(PilotProject, PilotProject)
	if err != nil || snapshot.RulesRevision() != p.RulesRevision || snapshot.Selected().GitHub != "thaynes43/dev-env" || snapshot.Selected().DefaultBranch != "main" {
		return out, ErrRefused
	}
	out.CatalogUID, out.CatalogResourceVersion = string(cm.UID), cm.ResourceVersion
	out.Snapshot, err = json.Marshal(snapshot)
	if err != nil {
		return PilotEvidence{}, ErrRefused
	}
	out.Common, err = commonrules.Resolve(ctx, reader, projectcatalog.Digest(out.Snapshot))
	if err != nil || out.Common.CommonRevision != p.CommonRevision {
		return PilotEvidence{}, ErrRefused
	}
	for _, prompt := range p.Prompts {
		if CheckPrompt(prompt, snapshot.Rules(), out.Common.Common.Text, out.Common.Header.Text) != nil {
			return PilotEvidence{}, ErrRefused
		}
	}
	out.Templates, err = ReadTemplates(ctx, reader, templateKey)
	if err != nil || p.ValidateTemplate(out.Templates) != nil {
		return PilotEvidence{}, ErrRefused
	}
	return out, nil
}
func (p *PilotPolicy) ValidateTemplate(t *templates.Templates) error {
	if p.Validate() != nil {
		return ErrRefused
	}
	return validatePrivateTemplate(t, "thaynes43", "codex", PilotProfile, p.TemplateRevision, p.Image)
}
func (p *PilotPolicy) MatchSession(s *v1alpha1.AgentSession, slot int) error {
	if p.Validate() != nil || slot < 0 || slot > 1 || s == nil || s.Namespace != "dev-agents" || s.Name != PilotSessionName(slot) || s.UID == "" || !s.DeletionTimestamp.IsZero() || !slices.Contains(s.Finalizers, PilotFinalizer) || s.Annotations[PilotAnnotation] != p.Binding(slot) || s.Labels[v1alpha1.LabelIdempotencyKey] != PilotKey(slot) {
		return ErrRefused
	}
	record, err := decodePilotRecord(s)
	if err != nil || record.Policy.Digest() != p.Digest() || record.Policy.SealUID != p.SealUID {
		return ErrRefused
	}
	spec := s.Spec
	req := p.Request(slot)
	if spec.Parent != PilotCaller || spec.Agent != v1alpha1.AgentCodex || spec.Mode != v1alpha1.ModeTask || spec.Repo != PilotProject || spec.Base != "main" || spec.Model != req.Model || spec.Effort != req.Effort || spec.Prompt != req.Prompt || spec.Profile != PilotProfile || spec.Size != v1alpha1.SizeS || spec.Workspace != nil || spec.ProjectCanary != nil || spec.Restore != "" || spec.Lifecycle != nil || len(spec.Tools) != 0 || spec.LLM != nil || spec.Caller != "" || spec.Lane != "" || spec.Limits == nil || spec.Limits.Timeout == nil || spec.Limits.Timeout.Duration != 15*time.Minute || spec.Limits.MaxTurns != 0 || spec.SessionDirectory == nil || spec.SessionDirectory.Version != 1 || spec.SessionDirectory.Project != PilotProject {
		return ErrRefused
	}
	snapshot, err := projectcatalog.ParseSnapshot([]byte(s.Annotations[v1alpha1.AnnotationProjectSnapshot]))
	if err != nil || snapshot.Project() != PilotProject || snapshot.Selected().Name != PilotProject || snapshot.Selected().GitHub != "thaynes43/dev-env" || snapshot.CatalogRevision() != p.CatalogRevision || snapshot.RulesRevision() != p.RulesRevision {
		return ErrRefused
	}
	common, err := commonrules.BoundSession(s)
	if err != nil || common.CommonRevision != p.CommonRevision {
		return ErrRefused
	}
	return nil
}

// PilotRecord is retained privately on the original Session. Directory projection
// exports only its revision/authority metadata, never prompts or rule bodies.
type PilotRecord struct {
	Policy                 PilotPolicy `json:"policy"`
	CatalogUID             string      `json:"catalogUID"`
	CatalogResourceVersion string      `json:"catalogResourceVersion"`
}

func (p *PilotPolicy) Record(e PilotEvidence) string {
	data, _ := json.Marshal(PilotRecord{Policy: *p, CatalogUID: e.CatalogUID, CatalogResourceVersion: e.CatalogResourceVersion})
	return string(data)
}
func decodePilotRecord(s *v1alpha1.AgentSession) (PilotRecord, error) {
	var r PilotRecord
	data := []byte(s.Annotations[PilotPolicyAnnotation])
	if len(data) > MaxPolicyBytes || !utf8.Valid(data) {
		return r, ErrRefused
	}
	strict, err := kjson.UnmarshalStrict(data, &r)
	if err != nil || len(strict) != 0 || r.Policy.Validate() != nil || r.CatalogUID == "" || len(r.CatalogUID) > 128 || r.CatalogResourceVersion == "" || len(r.CatalogResourceVersion) > 128 {
		return r, ErrRefused
	}
	return r, nil
}
func BoundPilot(s *v1alpha1.AgentSession) (PilotRecord, error) {
	r, err := decodePilotRecord(s)
	if err != nil {
		return r, err
	}
	slot := 0
	if s.Name == PilotSessionName(1) {
		slot = 1
	}
	if r.Policy.MatchSession(s, slot) != nil {
		return r, ErrRefused
	}
	return r, nil
}
