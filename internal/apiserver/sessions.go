package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// nameAttempts bounds the suffixes a generated name tries (-2 to -9) when
// another create took the name in the same second.
const nameAttempts = 9

// createSession serves POST /v1/sessions.
func (s *Server) createSession(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	var req apiv1.CreateSessionRequest
	if err := decodeJSON(w, r, maxCreateBody, &req, true); err != nil {
		return 0, nil, err
	}
	sess, err := s.newSession(ctx, req, c)
	if err != nil {
		return 0, nil, err
	}
	// A restore is checked against the shelf before the lock: it execs.
	if req.Restore != "" {
		if err := s.checkRestore(ctx, req, sess); err != nil {
			return 0, nil, err
		}
	}

	s.createMu.Lock()
	defer s.createMu.Unlock()

	// A repeat of a key comes first: a retry of a create that succeeded must get
	// its session back, even when that session is the one that reached a limit.
	if req.IdempotencyKey != "" {
		existing, err := s.byIdempotencyKey(ctx, c.parent, req.IdempotencyKey)
		if err != nil {
			return 0, nil, err
		}
		if existing != nil {
			if existing.Annotations[v1alpha1.AnnotationRequestHash] != sess.Annotations[v1alpha1.AnnotationRequestHash] {
				return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
					"idempotency key %q already created session %s with a different request", req.IdempotencyKey, existing.Name)
			}
			w.Header().Set("Location", apiv1.SessionPath(existing.Name))
			return http.StatusOK, view(existing, false), nil
		}
	}

	if c.kind == kindSession {
		if c.depth > maxSessionDepth {
			return 0, nil, forbidden("session %s is %d levels deep and may not start sessions: two levels is the limit (DESIGN-001 3.4)", c.parent, c.depth-1)
		}
		n, err := s.runningChildren(ctx, c.parent)
		if err != nil {
			return 0, nil, err
		}
		if n >= maxRunningChildren {
			return 0, nil, newError(http.StatusTooManyRequests, apiv1.CodeLimitExceeded,
				"session %s already has %d running children, the most at a time (DESIGN-001 3.4); one must finish first", c.parent, n)
		}
	}

	base := generatedName(req.Repo, s.now())
	for i := 1; i <= nameAttempts; i++ {
		sess.Name = base
		if i > 1 {
			sess.Name = fmt.Sprintf("%s-%d", base, i)
		}
		if err := checkAgentd(sess); err != nil {
			return 0, nil, err
		}
		err := s.Client.Create(ctx, sess)
		if apierrors.IsAlreadyExists(err) {
			sess.ResourceVersion = ""
			continue
		}
		if err != nil {
			return 0, nil, fromKubeError(err, "create session")
		}
		s.Log.Info("session created", "session", sess.Name, "caller", c.String(), "repo", sess.Spec.Repo,
			"agent", sess.Spec.Agent, "mode", sess.Spec.Mode, "model", sess.Spec.Model, "depth", c.depth)
		w.Header().Set("Location", apiv1.SessionPath(sess.Name))
		return http.StatusCreated, view(sess, false), nil
	}
	return 0, nil, internal("no free session name after %s-%d", base, nameAttempts)
}

// runningChildren counts a session's unfinished children, read from the API
// server so a create a moment ago counts.
func (s *Server) runningChildren(ctx context.Context, parent string) (int, error) {
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return 0, fromKubeError(err, "list sessions")
	}
	n := 0
	for i := range list.Items {
		if list.Items[i].Spec.Parent == parent && unfinished(&list.Items[i]) {
			n++
		}
	}
	return n, nil
}

// byIdempotencyKey finds the caller's unfinished session created with key, the
// newest if there are several.
func (s *Server) byIdempotencyKey(ctx context.Context, parent, key string) (*v1alpha1.AgentSession, error) {
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace),
		client.MatchingLabels{v1alpha1.LabelIdempotencyKey: key}); err != nil {
		return nil, fromKubeError(err, "list sessions")
	}
	var found *v1alpha1.AgentSession
	for i := range list.Items {
		it := &list.Items[i]
		if it.Spec.Parent != parent || !unfinished(it) {
			continue
		}
		if found == nil || it.CreationTimestamp.After(found.CreationTimestamp.Time) {
			found = it
		}
	}
	return found, nil
}

// unfinished is a session still at work: not reaped, suspended, archived or
// failed, and its agent not ended. A child counts against its parent's four
// until then, and an idempotency key returns it until then (V-03).
func unfinished(s *v1alpha1.AgentSession) bool {
	if !s.DeletionTimestamp.IsZero() || s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended {
		return false
	}
	switch s.Status.Phase {
	case v1alpha1.PhaseSuspended, v1alpha1.PhaseArchived, v1alpha1.PhaseFailed:
		return false
	}
	if a := s.Status.Agent; a != nil {
		switch a.Status {
		case protocol.AgentExited, protocol.AgentFailed, protocol.AgentInterrupted:
			return false
		}
	}
	return true
}

// nameUnsafe is what a repository name may hold that a DNS label may not.
var nameUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// maxRepoInName leaves room in 63 characters for -mmdd-HHMMSS and a -N suffix.
const maxRepoInName = 63 - len("-1006-172226") - len("-9")

// generatedName is v1's session id, <repo>-<mmdd>-<HHMMSS>, in UTC, as a DNS
// label: the repository name lowercased, each run of other characters one '-',
// cut so a -N suffix still fits in 63 characters.
func generatedName(repo string, now time.Time) string {
	r := strings.Trim(nameUnsafe.ReplaceAllString(strings.ToLower(repo), "-"), "-")
	if len(r) > maxRepoInName {
		r = strings.TrimRight(r[:maxRepoInName], "-")
	}
	if r == "" {
		r = "session"
	}
	return r + "-" + now.UTC().Format("0102-150405")
}

// listSessions serves GET /v1/sessions.
func (s *Server) listSessions(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	f, err := parseFilters(r.URL.Query())
	if err != nil {
		return 0, nil, err
	}
	var list v1alpha1.AgentSessionList
	reader := client.Reader(s.Client)
	if c.kind == kindCoordinator {
		reader = s.Live
	}
	if err := reader.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return 0, nil, fromKubeError(err, "list sessions")
	}
	items := list.Items
	slices.SortFunc(items, func(a, b v1alpha1.AgentSession) int {
		if c := b.CreationTimestamp.Compare(a.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	out := apiv1.SessionList{Sessions: []apiv1.Session{}}
	for i := range items {
		if (c.kind != kindCoordinator || items[i].Spec.Parent == c.parent) && f.match(&items[i], c) {
			out.Sessions = append(out.Sessions, view(&items[i], false))
		}
	}
	return http.StatusOK, out, nil
}

// filters are a list's query parameters.
type filters struct {
	repo, state, caller, lane, outcome string
	mine                               bool
}

func parseFilters(q url.Values) (filters, error) {
	var f filters
	for k, v := range q {
		if len(v) != 1 {
			return f, badRequest("query parameter %s is given %d times", k, len(v))
		}
		switch k {
		case apiv1.FilterRepo:
			f.repo = v[0]
		case apiv1.FilterState:
			f.state = v[0]
		case apiv1.FilterCaller:
			f.caller = v[0]
		case apiv1.FilterLane:
			f.lane = v[0]
		case apiv1.FilterOutcome:
			f.outcome = v[0]
		case apiv1.FilterMine:
			switch v[0] {
			case "true":
				f.mine = true
			case "false":
			default:
				return f, badRequest("mine is true or false, not %q", v[0])
			}
		default:
			return f, badRequest("unknown query parameter %q: the filters are repo, state, mine, caller, lane and outcome", k)
		}
	}
	return f, nil
}

func (f filters) match(s *v1alpha1.AgentSession, c *caller) bool {
	outcome := ""
	if s.Status.Outcome != nil {
		outcome = string(s.Status.Outcome.State)
	}
	switch {
	case f.repo != "" && s.Spec.Repo != f.repo,
		f.state != "" && !strings.EqualFold(phaseOf(s), f.state),
		f.caller != "" && s.Spec.Caller != f.caller,
		f.lane != "" && string(s.Spec.Lane) != f.lane,
		f.outcome != "" && outcome != f.outcome,
		f.mine && s.Spec.Parent != c.parent:
		return false
	}
	return true
}

// phaseOf is the session's phase; one the reconciler has not seen yet is
// Pending.
func phaseOf(s *v1alpha1.AgentSession) string {
	if s.Status.Phase == "" {
		return string(v1alpha1.PhasePending)
	}
	return string(s.Status.Phase)
}

// sessionKey reads and checks the {name} path value.
func (s *Server) sessionKey(r *http.Request) (types.NamespacedName, error) {
	name := r.PathValue("name")
	if len(validation.IsDNS1123Label(name)) > 0 {
		return types.NamespacedName{}, notFound("no session %q: a session name is a DNS label", name)
	}
	return types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, nil
}

// getSession serves GET /v1/sessions/{name}.
func (s *Server) getSession(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	key, err := s.sessionKey(r)
	if err != nil {
		return 0, nil, err
	}
	if c.kind == kindCoordinator {
		sess, err := s.childForCoordinator(ctx, key, c, "")
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, view(sess, true), nil
	}
	var sess v1alpha1.AgentSession
	if err := s.get(ctx, key, &sess); err != nil {
		return 0, nil, fromKubeError(err, "session "+key.Name)
	}
	return http.StatusOK, view(&sess, true), nil
}

// reapSession serves DELETE /v1/sessions/{name}: it deletes the AgentSession,
// which is a reap (D-45). The operator's rescue finalizer holds the session, its
// pod and its volume until the rescue; there is no way to skip it here
// (DESIGN-001 3.4). Any caller the API serves may reap any session, as in v1:
// nothing is lost, because the rescue comes first.
func (s *Server) reapSession(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	key, err := s.sessionKey(r)
	if err != nil {
		return 0, nil, err
	}
	// Read from the API server: the finalizer check below must not trust a
	// cache that lags.
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, key, &sess); err != nil {
		if c.kind == kindCoordinator {
			return 0, nil, coordinatorDenied()
		}
		return 0, nil, fromKubeError(err, "session "+key.Name)
	}
	if c.kind == kindCoordinator && (sess.Spec.Parent != c.parent || sess.UID == "") {
		return 0, nil, coordinatorDenied()
	}
	if err := s.authorizeChildMutation(ctx, &sess, c); err != nil {
		return 0, nil, err
	}
	if !sess.DeletionTimestamp.IsZero() {
		return http.StatusAccepted, view(&sess, false), nil
	}
	// Without the finalizer, deleting the session would let the garbage
	// collector take a pod or volume it already has, unrescued. The operator
	// adds the finalizer before it creates either (D-45); a session that has
	// neither yet has nothing to lose.
	if !controllerutil.ContainsFinalizer(&sess, controller.Finalizer) && (sess.Status.PodName != "" || sess.Status.Phase != "") {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
			"session %s lacks the %s finalizer, so a delete could lose its pod and volume unrescued; the operator adds it on its next reconcile", sess.Name, controller.Finalizer)
	}
	uid := sess.UID
	err = s.Client.Delete(ctx, &sess,
		client.PropagationPolicy(metav1.DeletePropagationBackground),
		client.Preconditions{UID: &uid})
	if err != nil {
		return 0, nil, fromKubeError(err, "session "+key.Name)
	}
	s.Log.Info("session reap requested", "session", sess.Name, "caller", c.String())
	var after v1alpha1.AgentSession
	if err := s.Live.Get(ctx, key, &after); err == nil && (c.kind != kindCoordinator || (after.UID == sess.UID && after.Spec.Parent == c.parent)) {
		return http.StatusAccepted, view(&after, false), nil
	}
	v := view(&sess, false)
	v.Reaping = true
	return http.StatusAccepted, v, nil
}
