package executor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
)

// Sources is the production Authorizer and TemplateResolver (DESIGN-054
// S3b2): it reads, uncached, the dispatch templates ConfigMap and the owner
// session, and asks the pure authority package. One value serves both
// interfaces, so the template a decision was made under is the template the
// session is built from.
//
//   - Resolve reads the templates ConfigMap. An absent ConfigMap or one with
//     no data is no templates, and an invalid document or an unknown name
//     is a *Refusal, which parks the occurrence. Only a failed read is a
//     plain error, tried again inside the grace.
//   - Authorize resolves the item's template, reads the owner session and
//     its pod when the owner is a session, and decides with authority.Decide
//     and authority.Choose against the template's admitted bases. A template
//     refusal is no basis, with the template's reason: the occurrence parks
//     at once and never waits on an owner it could not run for. The
//     approval's template check compares the template's digest with the one
//     the approval recorded in status.approval.templateDigest (the API's
//     approve route writes it); an approval without one is void.
//
// Authorize returns the digest of the template it decided under
// (Authority.TemplateDigest) and Resolve the digest of the template it reads
// (Template.Digest); the executor builds only when they are equal, and tries
// again otherwise. Sources keeps no state between calls, so concurrent
// callers cannot cross.
type Sources struct {
	// Reader reads from the API server, never a cache.
	Reader client.Reader
	// SessionNamespace holds the owner sessions and their pods.
	SessionNamespace string
	// TemplatesNamespace and TemplatesName name the templates ConfigMap;
	// empty takes authority.TemplatesNamespace and
	// authority.TemplatesConfigMap.
	TemplatesNamespace, TemplatesName string
	// StaleAfter bounds the owner's heartbeat age; zero is
	// authority.DefaultHeartbeatStaleAfter.
	StaleAfter time.Duration

	// recordedDigest overrides the approval's recorded template digest, for
	// tests. Nil reads status.approval.templateDigest (approvalDigest), which
	// the API's approve route writes with the approval.
	recordedDigest func(*v1alpha1.ScheduledWork) string
}

var (
	_ Authorizer       = (*Sources)(nil)
	_ TemplateResolver = (*Sources)(nil)
)

// refusalClip bounds a refusal's reason, inside the 256 bytes of a record.
const refusalClip = 200

// templates reads and parses the templates ConfigMap. A *Refusal is a
// definite refusal; any other error is a failed read.
func (s *Sources) templates(ctx context.Context) (map[string]authority.Template, error) {
	ns, name := s.TemplatesNamespace, s.TemplatesName
	if ns == "" {
		ns = authority.TemplatesNamespace
	}
	if name == "" {
		name = authority.TemplatesConfigMap
	}
	var cm corev1.ConfigMap
	if err := s.Reader.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &Refusal{Reason: fmt.Sprintf("no dispatch templates: ConfigMap %s/%s does not exist", ns, name)}
		}
		return nil, fmt.Errorf("read the dispatch templates ConfigMap %s/%s: %w", ns, name, err)
	}
	ts, err := authority.ParseTemplates(cm.Data)
	if err != nil {
		return nil, &Refusal{Reason: clip("invalid dispatch templates: "+err.Error(), refusalClip)}
	}
	return ts, nil
}

// template resolves one template by name: a *Refusal when there is none.
func (s *Sources) template(ctx context.Context, name string) (authority.Template, error) {
	ts, err := s.templates(ctx)
	if err != nil {
		return authority.Template{}, err
	}
	t, ok := ts[name]
	if !ok {
		if len(ts) == 0 {
			return authority.Template{}, &Refusal{Reason: "no dispatch templates are configured"}
		}
		return authority.Template{}, &Refusal{Reason: "no such dispatch template"}
	}
	return t, nil
}

// Resolve implements TemplateResolver.
func (s *Sources) Resolve(ctx context.Context, name string) (Template, error) {
	t, err := s.template(ctx, name)
	if err != nil {
		return Template{}, err
	}
	return executorTemplate(t), nil
}

// approvalDigest is the template digest the stored approval recorded: ""
// without an approval or without the digest, which authority.Decide holds
// void (authority.ReasonApprovalNoTemplate).
func approvalDigest(w *v1alpha1.ScheduledWork) string {
	if w.Status.Approval == nil {
		return ""
	}
	return w.Status.Approval.TemplateDigest
}

// Authorize implements Authorizer.
func (s *Sources) Authorize(ctx context.Context, w *v1alpha1.ScheduledWork, now time.Time) (Authority, error) {
	name := w.Spec.Work.Template
	t, err := s.template(ctx, name)
	var refusal *Refusal
	switch {
	case errors.As(err, &refusal):
		// No template, no basis: the occurrence parks on the template's
		// reason at once, and never waits on an owner it could not run for.
		return Authority{Reason: fmt.Sprintf("template %s: %s", name, refusal.Reason)}, nil
	case err != nil:
		return Authority{}, err
	}
	digest := authority.TemplateDigest(t)
	owner, err := s.owner(ctx, w, now)
	if err != nil {
		return Authority{}, err
	}
	recorded := approvalDigest(w)
	if s.recordedDigest != nil {
		recorded = s.recordedDigest(w)
	}
	stale := s.StaleAfter
	if stale <= 0 {
		stale = authority.DefaultHeartbeatStaleAfter
	}
	d := authority.Decide(w, owner, authority.TemplateBinding{Current: digest, Recorded: recorded}, now, stale)
	a := authorityOf(authority.Choose(d, t.Admits))
	a.TemplateDigest = digest
	return a, nil
}

// owner reads the owner session and its pod, uncached, when w's owner is a
// session: NotFound is nil, any other error is a failed read.
func (s *Sources) owner(ctx context.Context, w *v1alpha1.ScheduledWork, now time.Time) (authority.OwnerState, error) {
	name, _, ok := authority.OwnerSession(w)
	if !ok {
		return authority.OwnerState{}, nil
	}
	var sess v1alpha1.AgentSession
	if err := s.Reader.Get(ctx, types.NamespacedName{Namespace: s.SessionNamespace, Name: name}, &sess); err != nil {
		if apierrors.IsNotFound(err) {
			return authority.OwnerStateOf(nil, nil, now), nil
		}
		return authority.OwnerState{}, fmt.Errorf("read the owner session %s: %w", name, err)
	}
	var pod *corev1.Pod
	var p corev1.Pod
	switch err := s.Reader.Get(ctx, types.NamespacedName{Namespace: s.SessionNamespace, Name: name}, &p); {
	case err == nil:
		pod = &p
	case !apierrors.IsNotFound(err):
		return authority.OwnerState{}, fmt.Errorf("read the owner session's pod %s: %w", name, err)
	}
	return authority.OwnerStateOf(&sess, pod, now), nil
}

// authorityOf maps a choice to the executor's Authority: an unproven owner
// is Unproven, and a refusal says "no standing authority" first.
func authorityOf(c authority.Choice) Authority {
	a := Authority{Basis: Basis(c.Basis), Unproven: c.Wait}
	switch {
	case c.Basis == authority.BasisSession && c.Owner != nil:
		a.Owner = &scheduledwork.SessionRef{Name: c.Owner.Name, UID: types.UID(c.Owner.UID)}
	case c.Wait:
		a.Reason = c.Reason
	case c.Basis == authority.BasisNone:
		a.Reason = ReasonNoAuthority + ": " + c.Reason
	}
	return a
}

// executorTemplate converts a parsed template, copying its lists.
func executorTemplate(t authority.Template) Template {
	admits := make([]Basis, 0, len(t.Admits))
	for _, b := range t.Admits {
		admits = append(admits, Basis(b))
	}
	return Template{
		Agent: t.Agent, Model: t.Model, Effort: t.Effort, Size: t.Size, Profile: t.Profile,
		Repos: slices.Clone(t.Repos), Modes: slices.Clone(t.Modes),
		MaxDuration: t.MaxDuration, IdleSuspendAfter: t.IdleSuspendAfter, Admits: admits,
		Digest: authority.TemplateDigest(t),
	}
}
