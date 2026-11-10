package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	meatballAccount       = "dev-env-system/dev-env-meatball"
	callerPolicyNamespace = "dev-env-system"
	callerPolicyName      = "meatball"
	callerParentUID       = "dev-env.haynesops.com/caller-capacity-parent-uid"
)

// readCallerPolicy never consults the manager's cache: removal revokes the
// caller immediately. A missing CRD or object enrolls nobody.
func (s *Server) readCallerPolicy(ctx context.Context) (*v1alpha1.CallerPolicy, error) {
	if s.Live == nil {
		return nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "CallerPolicy cannot be read")
	}
	var p v1alpha1.CallerPolicy
	err := s.Live.Get(ctx, types.NamespacedName{Namespace: callerPolicyNamespace, Name: callerPolicyName}, &p)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fromKubeError(err, "CallerPolicy")
	}
	// Fail closed on a stale installed schema as well as on invalid fake data.
	timeout, err := time.ParseDuration(p.Spec.MaxTimeout)
	if !p.DeletionTimestamp.IsZero() || p.UID == "" || p.Spec.ServiceAccount != meatballAccount ||
		p.Spec.Mode != "task" || p.Spec.Profile != "dev" || p.Spec.MaxConcurrent < 1 || p.Spec.MaxConcurrent > 2 ||
		p.Spec.MaxWorkers < 1 || p.Spec.MaxWorkers > 4 || err != nil || timeout <= 0 || timeout > 4*time.Hour ||
		len(p.Spec.MaxTimeout) > 32 || len(p.Spec.Models) < 1 || len(p.Spec.Models) > 3 {
		return nil, forbidden("CallerPolicy is not a valid Meatball policy")
	}
	seen := make(map[string]bool)
	for _, model := range p.Spec.Models {
		if _, _, ok := callerModelPreset(model); !ok || seen[model] {
			return nil, forbidden("CallerPolicy must list distinct allowed driver models")
		}
		seen[model] = true
	}
	return &p, nil
}

func callerModelPreset(model string) (agent, effort string, ok bool) {
	switch model {
	case "claude-opus-5-5", "claude-fable-5-2":
		return "claude", "xhigh", true
	case "gpt-6-astra":
		return "codex", "max", true
	default:
		return "", "", false
	}
}

// preparePolicySession enforces caller input before the ordinary create path
// can apply defaults or select another route. It never chooses a model.
func (s *Server) preparePolicySession(ctx context.Context, req apiv1.CreateSessionRequest, c *caller) (apiv1.CreateSessionRequest, error) {
	deny := func(field, message string) (apiv1.CreateSessionRequest, error) {
		return req, invalid(fieldError(field, "%s", message))
	}
	if c.policy == nil {
		return req, forbidden("CallerPolicy is unavailable")
	}
	p := c.policy.Spec
	if req.Mode != p.Mode {
		return deny("mode", "CallerPolicy permits task sessions only")
	}
	if req.Profile != p.Profile {
		return deny("profile", "CallerPolicy requires profile dev")
	}
	if req.Model == "" {
		return deny("model", "a driver model is required; CallerPolicy has no default")
	}
	agent, effort, ok := callerModelPreset(req.Model)
	if !ok || !slices.Contains(p.Models, req.Model) {
		return deny("model", "the driver model is not allowed by CallerPolicy")
	}
	if req.Agent != agent {
		return deny("agent", "the agent must match the named driver model")
	}
	if req.Effort != "" {
		return deny("effort", "CallerPolicy accepts no effort setting; the model uses its preset")
	}
	if req.Limits == nil || req.Limits.Timeout == "" {
		return deny("limits.timeout", "CallerPolicy requires a positive task timeout")
	}
	limit, _ := time.ParseDuration(p.MaxTimeout) // checked by readCallerPolicy
	timeout, err := time.ParseDuration(req.Limits.Timeout)
	if err != nil || timeout <= 0 || timeout > limit {
		return deny("limits.timeout", fmt.Sprintf("CallerPolicy requires a positive task timeout at most %s", p.MaxTimeout))
	}
	if req.Limits.MaxWorkers == nil || *req.Limits.MaxWorkers < 1 || *req.Limits.MaxWorkers > p.MaxWorkers {
		return deny("limits.maxWorkers", fmt.Sprintf("CallerPolicy requires a worker cap from 1 to %d", p.MaxWorkers))
	}
	if req.Restore != "" || req.CanaryID != "" || req.CoordinatorInstructions || req.CoordinatorInstructionPublisher {
		return req, forbidden("CallerPolicy permits fresh ordinary tasks only, without restore, canary or coordinator instruction enrollment")
	}
	if s.Templates == nil {
		return req, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "CallerPolicy requires the dev profile templates")
	}
	t, err := s.Templates(ctx)
	if err != nil || t == nil {
		return req, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "CallerPolicy dev profile cannot be verified")
	}
	_, profile, err := t.Profile("dev")
	if err != nil || profile.ServiceAccount != "" {
		return req, forbidden("CallerPolicy requires dev to run as the ordinary session ServiceAccount")
	}
	req.Effort = effort
	return req, nil
}

func policyCallerDenied() error {
	return forbidden("CallerPolicy permits creating tasks and reading or reaping only sessions this caller started")
}

// policyCallerAllowed is a positive route and method list. New routes grant
// nothing implicitly. In particular the log route execs and is not a read here.
func policyCallerAllowed(c *caller, r *http.Request) error {
	if c.kind != kindPolicy {
		return nil
	}
	if r.URL.Path == apiv1.SessionsPath && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		return nil
	}
	if name := r.PathValue("name"); name != "" && r.URL.Path == apiv1.SessionPath(name) && (r.Method == http.MethodGet || r.Method == http.MethodDelete) {
		return nil
	}
	return policyCallerDenied()
}

func policyOwns(c *caller, sess *v1alpha1.AgentSession) bool {
	return sess.UID != "" && sess.Spec.Parent == c.parent
}

// Keep existing strict clients able to read a new policy-created session.
func withoutCallerWorkerCap(body any) any {
	strip := func(session apiv1.Session) apiv1.Session {
		if session.Limits != nil {
			limits := *session.Limits
			limits.MaxWorkers = nil
			session.Limits = &limits
		}
		return session
	}
	switch b := body.(type) {
	case apiv1.Session:
		return strip(b)
	case *apiv1.Session:
		out := strip(*b)
		return &out
	case apiv1.SessionList:
		b.Sessions = slices.Clone(b.Sessions)
		for i := range b.Sessions {
			b.Sessions[i] = strip(b.Sessions[i])
		}
		return b
	case apiv1.PilotPreflight:
		b.Session = strip(b.Session)
		return b
	}
	return body
}

func (s *Server) policySession(ctx context.Context, key types.NamespacedName, c *caller) (*v1alpha1.AgentSession, error) {
	var sess v1alpha1.AgentSession
	if s.Live == nil || s.Live.Get(ctx, key, &sess) != nil || !policyOwns(c, &sess) {
		return nil, policyCallerDenied()
	}
	return &sess, nil
}

// callerWorkerCap is absent on existing sessions. It changes no legacy caller's
// child or depth rule, and is an additional aggregate cap on new policy roots.
func callerWorkerCap(c *caller) *int32 {
	if c.kind == kindSession && c.session != nil && c.session.Spec.Limits != nil {
		return c.session.Spec.Limits.MaxWorkers
	}
	return nil
}

// admitCallerCapacity runs under createMu, just before the create; a bounded
// resume runs admitCallerCapacityExcept there too, excluding the session
// itself, before its patch. Live reads count other replicas' writes once they
// commit. Two replicas admitting in the same instant can exceed a cap by one;
// DESIGN-060 7.1 accepts that, as the per-agent child rule already does.
func (s *Server) admitCallerCapacity(ctx context.Context, c *caller) error {
	return s.admitCallerCapacityExcept(ctx, c, "")
}

func (s *Server) admitCallerCapacityExcept(ctx context.Context, c *caller, except types.UID) error {
	var cap int32
	switch {
	case c.kind == kindPolicy:
		p, err := s.readCallerPolicy(ctx)
		if err != nil {
			return err
		}
		if p == nil || c.policy == nil || p.UID != c.policy.UID || p.ResourceVersion != c.policy.ResourceVersion {
			return forbidden("CallerPolicy changed during admission; retry with its current bounds")
		}
		cap = p.Spec.MaxConcurrent
	case callerWorkerCap(c) != nil:
		var parent v1alpha1.AgentSession
		if s.Live == nil || s.Live.Get(ctx, client.ObjectKeyFromObject(c.session), &parent) != nil ||
			parent.UID != c.session.UID || parent.Spec.Limits == nil || parent.Spec.Limits.MaxWorkers == nil ||
			*parent.Spec.Limits.MaxWorkers != *callerWorkerCap(c) {
			return forbidden("the bounded worker parent cannot be verified")
		}
		cap = *parent.Spec.Limits.MaxWorkers
		if cap < 1 || cap > 4 {
			return forbidden("the parent has an invalid worker cap")
		}
	default:
		return nil
	}
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return fromKubeError(err, "count CallerPolicy sessions")
	}
	n := 0
	for i := range list.Items {
		it := &list.Items[i]
		if it.Spec.Parent == c.parent && (except == "" || it.UID != except) && unfinished(it) {
			n++
		}
	}
	if n >= int(cap) {
		if c.kind == kindPolicy {
			return tooManyRequests("CallerPolicy permits at most %d unfinished sessions; one must finish first", cap)
		}
		return tooManyRequests("the parent worker cap is %d unfinished direct workers across all agent kinds; one must finish first", cap)
	}
	return nil
}

// createWithCallerCap writes a session admitted by admitCallerCapacity. A
// bounded create asks for strict field validation: an older installed CRD
// must refuse it, never silently prune limits.maxWorkers and launch an
// unbounded coordinator. A bounded parent's worker records the parent's UID.
func (s *Server) createWithCallerCap(ctx context.Context, sess *v1alpha1.AgentSession, c *caller) error {
	if c.kind != kindPolicy && callerWorkerCap(c) == nil {
		return s.Client.Create(ctx, sess)
	}
	if c.kind == kindSession {
		if sess.Annotations == nil {
			sess.Annotations = map[string]string{}
		}
		sess.Annotations[callerParentUID] = string(c.session.UID)
	}
	return s.Client.Create(ctx, sess, client.FieldValidation(metav1.FieldValidationStrict))
}

// Only newly bounded targets enter this path, whoever requested their resume.
// A worker's durable parent UID prevents orphaning or name reuse from turning
// its next resume into an unbounded legacy operation.
func (s *Server) callerForBoundedResume(ctx context.Context, sess *v1alpha1.AgentSession) (*caller, error) {
	if sess.Spec.Parent == meatballAccount && sess.Spec.Limits != nil && sess.Spec.Limits.MaxWorkers != nil {
		p, err := s.readCallerPolicy(ctx)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, forbidden("CallerPolicy is unavailable; cannot resume its bounded session")
		}
		return &caller{kind: kindPolicy, parent: meatballAccount, policy: p}, nil
	}
	uid := sess.Annotations[callerParentUID]
	if uid == "" {
		return nil, nil
	}
	var parent v1alpha1.AgentSession
	if s.Live == nil || s.Live.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Spec.Parent}, &parent) != nil ||
		string(parent.UID) != uid || parent.Spec.Limits == nil || parent.Spec.Limits.MaxWorkers == nil {
		return nil, forbidden("the bounded worker parent cannot be verified for resume")
	}
	return &caller{kind: kindSession, parent: parent.Name, session: &parent}, nil
}
