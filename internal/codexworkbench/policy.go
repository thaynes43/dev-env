// Package codexworkbench is the shared account and creation-shape guard for
// operator-named Codex remote hosts (DESIGN-067 slice a).
package codexworkbench

import (
	"errors"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

const (
	NotNamed             = "CodexWorkbenchHostNotNamed"
	InvalidShape         = "CodexWorkbenchHostInvalidShape"
	TemplatesUnavailable = "CodexWorkbenchHostTemplatesUnavailable"
	NotNamedMessage      = "The operator has not named this Codex host to run on the workbench account."
)

// Refusal contains only fixed public text, never template contents.
type Refusal struct{ Reason, Message string }

func (e *Refusal) Error() string { return e.Message }

// Policy reserves at most one exact name in the session namespace. Its zero
// value permits no workbench-account Codex host; the guard is always active.
type Policy struct{ Name, Namespace string }

// Parse accepts an empty setting or one exact DNS session name. Repeated flags
// are refused by the operator too, rather than letting the last one win.
func Parse(raw, namespace string) (Policy, error) {
	p := Policy{Namespace: namespace}
	if raw == "" {
		return p, nil
	}
	if len(validation.IsDNS1123Label(raw)) != 0 {
		return Policy{}, errors.New("codex-workbench-hosts must be empty or one exact DNS session name, without duplicates or wildcards")
	}
	p.Name = raw
	return p, nil
}

// Applies leaves other agents and Codex task/local policy unchanged, except
// that none may occupy the configured reserved name.
func (p Policy) Applies(s *v1alpha1.AgentSession) bool {
	return s != nil && (IsHost(s) || p.Name != "" && s.Name == p.Name)
}

func IsHost(s *v1alpha1.AgentSession) bool {
	return s.Spec.Agent == v1alpha1.AgentCodex && s.Spec.Mode == v1alpha1.ModeRemote
}

// Resolution is one immutable profile/account answer for a request/reconcile.
// Both the guard and pod builder consume it; rechecking never rereads templates.
type Resolution struct {
	requested, name string
	profile         templates.Profile
	unavailable     bool
}

func Resolve(t *templates.Templates, profile string) *Resolution {
	r := &Resolution{requested: profile}
	if t == nil {
		r.unavailable = true
		return r
	}
	var err error
	r.name, r.profile, err = t.Profile(profile)
	// templates.Validate permits only empty (ordinary), workbench or named
	// coordinator accounts. Treat an unvalidated unknown account as unavailable.
	r.unavailable = err != nil || r.profile.ServiceAccount != "" && r.profile.ServiceAccount != templates.WorkbenchServiceAccount && !templates.IsCoordinatorServiceAccount(r.profile.ServiceAccount)
	return r
}

func (r *Resolution) Profile() (string, templates.Profile) { return r.name, r.profile }
func (r *Resolution) Available() bool                      { return r != nil && !r.unavailable }

func (r *Resolution) Workbench() bool {
	return r != nil && !r.unavailable && r.profile.ServiceAccount == templates.WorkbenchServiceAccount
}

// Check is the one admission predicate used by the API and every agent-pod
// builder. No caller class, labels, annotations, title or model exempts it.
func (p Policy) Check(s *v1alpha1.AgentSession, r *Resolution) error {
	if !p.Applies(s) {
		return nil
	}
	if r == nil || r.unavailable || r.requested != s.Spec.Profile {
		return &Refusal{TemplatesUnavailable, "The templates cannot confirm the account for this Codex host or reserved name."}
	}
	reserved := p.Name != "" && s.Name == p.Name
	if reserved && (!IsHost(s) || !r.Workbench()) {
		return &Refusal{InvalidShape, "The reserved Codex host name requires a workbench-account Codex remote host with the declared GitOps shape."}
	}
	if !IsHost(s) || !r.Workbench() {
		return nil
	}
	if !reserved {
		return &Refusal{NotNamed, NotNamedMessage}
	}
	if p.Namespace == "" || s.Namespace != p.Namespace || s.Labels[v1alpha1.LabelDepth] != "0" ||
		s.Spec.Parent != "flux-system/kustomize-controller" || s.Spec.Caller != "" || s.Spec.Summon != nil ||
		s.Spec.ProjectCanary != nil || s.Spec.PrivatePilot != nil || s.Spec.Workspace != nil || s.Spec.SessionDirectory != nil ||
		s.Annotations[v1alpha1.AnnotationProjectSnapshot] != "" || scheduled(s) {
		return &Refusal{InvalidShape, "The reserved Codex host name requires a workbench-account Codex remote host with the declared GitOps shape."}
	}
	return nil
}

func scheduled(s *v1alpha1.AgentSession) bool {
	if _, ok := s.Labels[v1alpha1.LabelScheduledWork]; ok {
		return true
	}
	for _, key := range []string{v1alpha1.AnnotationScheduledWorkName, v1alpha1.AnnotationScheduledWorkOccurrence, v1alpha1.AnnotationScheduledWorkStartDeadline} {
		if _, ok := s.Annotations[key]; ok {
			return true
		}
	}
	return controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer)
}
