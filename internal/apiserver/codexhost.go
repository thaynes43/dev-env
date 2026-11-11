package apiserver

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexhostpool"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
	"github.com/thaynes43/dev-env/internal/templates"
)

const (
	codexHostBodyLimit     = 8 << 10
	codexHostLeaseTTL      = 15 * time.Minute
	codexHostLeasePurpose  = "use"
	codexHostSelectPasses  = 8
	codexHostLeaseTries    = 5
	codexHostProfileDenied = "A session asks only for its own profile."
	// codexHostCoordinatorCheckOnly refuses a coordinator a lease. The thread
	// route does not accept that kind, so a lease would be useless to it.
	codexHostCoordinatorCheckOnly = "A coordinator may only check host selection."
	codexHostLaunchIDInvalid      = "launchId is 16 to 64 characters of a-z and 0-9"
)

const (
	codexHostLeaseAnnotation = v1alpha1.LabelPrefix + "codex-host-lease"
	// codexHostPoolLabel is the section 5 pool label. This slice only reads it.
	codexHostPoolLabel           = v1alpha1.LabelPrefix + "codex-host-pool"
	codexHostProtectedAnnotation = v1alpha1.LabelPrefix + "protected"
	codexHostBootstrapReady      = "Ready"
)

// CodexHostConfig turns on POST /v1/codex-hosts/select and /release.
// Nil leaves both unregistered.
type CodexHostConfig struct {
	MaxRunningPerCell int
	MaxRunning        int
	HandsOff          []string
}

type codexLeaseResult int

const (
	codexLeaseWritten codexLeaseResult = iota
	codexLeaseBusy
	codexLeaseConflict
)

func (s *Server) registerCodexHosts(mux *http.ServeMux) {
	if s.CodexHosts == nil {
		return
	}
	mux.Handle(apiv1.CodexHostsSelectPath, s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.selectCodexHost,
	}}))
	mux.Handle(apiv1.CodexHostsReleasePath, s.serve(route{coordinator: true, methods: map[string]handler{
		http.MethodPost: s.releaseCodexHost,
	}}))
}

func (s *Server) selectCodexHost(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	check, err := codexHostCheck(r)
	if err != nil {
		return 0, nil, err
	}
	var req apiv1.CodexHostSelectRequest
	if err := decodeJSON(w, r, codexHostBodyLimit, &req, true); err != nil {
		return 0, nil, err
	}
	if !apiv1.ValidRepoName(req.Repo) {
		return 0, nil, invalid(fieldError("repo", "repo is a repository name"))
	}
	if req.Profile != "" && len(validation.IsDNS1123Label(req.Profile)) > 0 {
		return 0, nil, invalid(fieldError("profile", "profile is a DNS label"))
	}
	if !apiv1.ValidCodexLaunchID(req.LaunchID) {
		return 0, nil, invalid(fieldError("launchId", codexHostLaunchIDInvalid))
	}
	if c.kind == kindCoordinator && !check {
		return 0, nil, forbidden("%s", codexHostCoordinatorCheckOnly)
	}
	tmpl, err := s.codexHostTemplates(ctx)
	if err != nil {
		return 0, nil, err
	}
	asked, err := s.codexHostAskedProfile(c, req.Profile, tmpl.DefaultProfile)
	if err != nil {
		return 0, nil, err
	}
	who := s.codexHostCaller(c)
	who.LaunchID = req.LaunchID
	in := codexhostpool.Input{
		Caller:            who,
		Request:           codexhostpool.Request{Repository: req.Repo},
		ResolvedProfile:   asked,
		DefaultProfile:    tmpl.DefaultProfile,
		MaxRunningPerCell: s.CodexHosts.MaxRunningPerCell,
		MaxRunning:        s.CodexHosts.MaxRunning,
	}
	var last string
	for range codexHostSelectPasses {
		hosts, login, err := s.codexHostFacts(ctx, tmpl)
		if err != nil {
			return 0, nil, fromKubeError(err, "sessions")
		}
		in.Hosts, in.Login, in.Now = hosts, login, s.now()
		res := codexhostpool.Select(in)
		last = res.Host
		if check || res.Action != codexhostpool.ActionUse || res.Host == "" {
			s.logCodexHostSelect(c, res, check)
			return http.StatusOK, res, nil
		}
		uid := ""
		for _, h := range hosts {
			if h.Name == res.Host {
				uid = h.UID
				break
			}
		}
		wrote, err := s.leaseCodexHost(ctx, codexLeaseTarget{
			name: res.Host, uid: uid, caller: who, profile: asked, repo: req.Repo, tmpl: tmpl,
		})
		if err != nil {
			return 0, nil, fromKubeError(err, "session "+res.Host)
		}
		switch wrote {
		case codexLeaseWritten:
			s.logCodexHostSelect(c, res, check)
			return http.StatusOK, res, nil
		case codexLeaseBusy:
			continue
		default:
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s kept changing; try again", res.Host)
		}
	}
	if last == "" {
		last = "selection"
	}
	return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s kept changing; try again", last)
}

func (s *Server) releaseCodexHost(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	var req apiv1.CodexHostReleaseRequest
	if err := decodeJSON(w, r, codexHostBodyLimit, &req, true); err != nil {
		return 0, nil, err
	}
	if len(validation.IsDNS1123Label(req.Host)) > 0 {
		return 0, nil, invalid(fieldError("host", "host is a session name"))
	}
	if !apiv1.ValidCodexLaunchID(req.LaunchID) {
		return 0, nil, invalid(fieldError("launchId", codexHostLaunchIDInvalid))
	}
	key := types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: req.Host}
	done := apiv1.CodexHostReleaseResponse{Host: req.Host, Released: true}
	for range codexHostLeaseTries {
		var sess v1alpha1.AgentSession
		if err := s.Live.Get(ctx, key, &sess); err != nil {
			if apierrors.IsNotFound(err) {
				return http.StatusOK, done, nil
			}
			return 0, nil, fromKubeError(err, "session "+req.Host)
		}
		raw := sess.Annotations[codexHostLeaseAnnotation]
		if raw == "" {
			return http.StatusOK, done, nil
		}
		holder, launchID, _, ok := parseCodexHostLease(raw)
		if !ok || holder != c.String() || launchID != req.LaunchID {
			return 0, nil, newError(http.StatusConflict, apiv1.CodexHostLeaseHeld, "%s", apiv1.CodexHostLeaseHeldSentence)
		}
		orig := sess.DeepCopy()
		delete(sess.Annotations, codexHostLeaseAnnotation)
		if err := s.Client.Patch(ctx, &sess, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return 0, nil, fromKubeError(err, "session "+req.Host)
		}
		s.Log.Info("codex host lease released", "host", sess.Name, "caller", c.String())
		return http.StatusOK, done, nil
	}
	return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s kept changing; try again", req.Host)
}

func codexHostCheck(r *http.Request) (bool, error) {
	if r.URL.RawQuery == "" {
		return false, nil
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false, invalid(fieldError("check", "check is true or omitted"))
	}
	v, ok := q["check"]
	if !ok || len(q) != 1 || len(v) != 1 || v[0] != "true" {
		return false, invalid(fieldError("check", "check is true or omitted"))
	}
	return true, nil
}

func (s *Server) codexHostTemplates(ctx context.Context) (*templates.Templates, error) {
	if s.Templates == nil {
		return nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "templates are not configured")
	}
	tmpl, err := s.Templates(ctx)
	if err != nil || tmpl == nil {
		return nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "templates are not usable")
	}
	return tmpl, nil
}

func (s *Server) codexHostAskedProfile(c *caller, named, def string) (string, error) {
	own := c.profile
	if own == "" && c.pod != nil {
		own = c.pod.Labels[v1alpha1.LabelProfile]
	}
	switch c.kind {
	case kindSession:
		if s.isWorkbenchSession(c) {
			if named != "" {
				return named, nil
			}
			return def, nil
		}
		if named != "" && named != own {
			return "", forbidden("%s", codexHostProfileDenied)
		}
		return own, nil
	case kindCoordinator:
		if named != "" && named != own {
			return "", forbidden("%s", codexHostProfileDenied)
		}
		return own, nil
	default:
		if named != "" {
			return named, nil
		}
		return def, nil
	}
}

func (s *Server) codexHostCaller(c *caller) codexhostpool.Caller {
	profile := c.profile
	if profile == "" && c.pod != nil {
		profile = c.pod.Labels[v1alpha1.LabelProfile]
	}
	return codexhostpool.Caller{
		Kind:      string(c.kind),
		Name:      c.String(),
		Profile:   profile,
		ParentRef: c.parent,
		Workbench: s.isWorkbenchSession(c),
	}
}

func (s *Server) logCodexHostSelect(c *caller, res codexhostpool.Result, check bool) {
	// The launch id is not logged. Presence is the request having been accepted.
	s.Log.Info("codex host select", "caller", c.String(), "host", res.Host, "action", res.Action, "reason", res.Reason, "check", check)
}

func (s *Server) codexHostFacts(ctx context.Context, tmpl *templates.Templates) ([]codexhostpool.Host, codexhostpool.Login, error) {
	if s.Live == nil {
		return nil, "", newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "sessions cannot be read")
	}
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return nil, "", err
	}
	now := s.now()
	hosts := make([]codexhostpool.Host, 0, len(list.Items))
	var bestAt time.Time
	var bestName, bestState string
	var have bool
	for i := range list.Items {
		sess := &list.Items[i]
		off := s.codexHostHandsOff(sess.Name)
		if !codexRemoteSession(sess) && !off {
			continue
		}
		pod, err := s.codexHostPod(ctx, sess)
		if err != nil {
			return nil, "", err
		}
		hosts = append(hosts, codexHostFrom(sess, pod, tmpl, off))
		if !codexLoginReading(sess, pod, now) {
			continue
		}
		at := sess.Status.Agent.LastHeartbeat.Time
		state := ""
		if sess.Status.RemoteControl != nil {
			state = sess.Status.RemoteControl.State
		}
		if !have || at.After(bestAt) || (at.Equal(bestAt) && sess.Name < bestName) {
			have, bestAt, bestName, bestState = true, at, sess.Name, state
		}
	}
	if !have {
		return hosts, codexhostpool.LoginUnknown, nil
	}
	if bestState == codexhostpool.StateLoginRequired {
		return hosts, codexhostpool.LoginLapsed, nil
	}
	return hosts, codexhostpool.LoginCurrent, nil
}

func codexRemoteSession(sess *v1alpha1.AgentSession) bool {
	return sess.Spec.Agent == v1alpha1.AgentCodex && sess.Spec.Mode == v1alpha1.ModeRemote
}

func codexLoginReading(sess *v1alpha1.AgentSession, pod *corev1.Pod, now time.Time) bool {
	if !codexRemoteSession(sess) || sess.Status.Phase != v1alpha1.PhaseRunning || !codexHostPodReady(sess, pod) {
		return false
	}
	if sess.Status.Agent == nil {
		return false
	}
	return codexHeartbeatFresh(sess.Status.Agent.LastHeartbeat, now)
}

// codexHostPodReady is the one Ready test for a host and for a login reading:
// the pod is this session's, Running and Ready, not deleting, and not a hold pod.
func codexHostPodReady(sess *v1alpha1.AgentSession, pod *corev1.Pod) bool {
	if sess == nil || pod == nil {
		return false
	}
	if !metav1.IsControlledBy(pod, sess) || !pod.DeletionTimestamp.IsZero() {
		return false
	}
	if pod.Labels[v1alpha1.LabelHold] == "true" {
		return false
	}
	return pod.Status.Phase == corev1.PodRunning && podIsReady(pod)
}

func codexHeartbeatFresh(at *metav1.Time, now time.Time) bool {
	if at == nil || at.After(now) {
		return false
	}
	return now.Sub(at.Time) <= authority.DefaultHeartbeatStaleAfter
}

func (s *Server) codexHostPod(ctx context.Context, sess *v1alpha1.AgentSession) (*corev1.Pod, error) {
	name := sess.Status.PodName
	if name == "" {
		name = sess.Name
	}
	var pod corev1.Pod
	err := s.Live.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: name}, &pod)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(&pod, sess) || !pod.DeletionTimestamp.IsZero() {
		return nil, nil
	}
	return &pod, nil
}

func codexHostFrom(sess *v1alpha1.AgentSession, pod *corev1.Pod, tmpl *templates.Templates, handsOff bool) codexhostpool.Host {
	h := codexhostpool.Host{
		Name:            sess.Name,
		UID:             string(sess.UID),
		Agent:           string(sess.Spec.Agent),
		Mode:            string(sess.Spec.Mode),
		Phase:           string(sess.Status.Phase),
		OperatingMode:   codexOperatingMode(sess),
		Deletion:        !sess.DeletionTimestamp.IsZero(),
		ResolvedProfile: codexResolvedProfile(sess, pod, tmpl.DefaultProfile),
		ReadyRepos:      codexReadyRepos(sess.Status.Agent),
		SpecRepo:        sess.Spec.Repo,
		HandsOff:        handsOff,
		Parent:          sess.Spec.Parent,
		Archived:        sess.Status.ArchivedAt != nil,
	}
	if pod != nil {
		h.PodReady = codexHostPodReady(sess, pod)
		h.HoldPod = pod.Labels[v1alpha1.LabelHold] == "true"
		h.ImageCurrent = pod.Labels[v1alpha1.LabelRevision] == tmpl.Revision() && tmpl.Revision() != "" &&
			!conditionIsTrue(sess.Status.Conditions, controller.ConditionOutdated)
		if pod.Labels[codexHostPoolLabel] == "true" {
			h.Pool = true
		}
	}
	if sess.Labels[codexHostPoolLabel] == "true" {
		h.Pool = true
	}
	if sess.Annotations[codexHostProtectedAnnotation] == "true" {
		h.ProtectedAnnotation = true
	}
	if holder, launchID, exp, ok := parseCodexHostLease(sess.Annotations[codexHostLeaseAnnotation]); ok {
		h.LeaseHolder, h.LeaseLaunchID, h.LeaseExpiry = holder, launchID, exp
	}
	if sess.Status.RemoteControl != nil {
		h.RemoteState = sess.Status.RemoteControl.State
	}
	if sess.Status.Agent != nil && sess.Status.Agent.LastActivity != nil {
		h.LastActivity = sess.Status.Agent.LastActivity.Time
	}
	if sess.Status.SuspendedAt != nil {
		h.SuspendedAt = sess.Status.SuspendedAt.Time
	}
	h.SuspendedBy = sess.Annotations[v1alpha1.AnnotationSuspendedBy]
	h.RescueFailed = conditionIsTrue(sess.Status.Conditions, controller.ConditionRescueFailed)
	h.RemovalBlocked = conditionIsTrue(sess.Status.Conditions, controller.ConditionRemovalBlocked)
	if sess.Status.Rescue != nil {
		h.RescueState = string(sess.Status.Rescue.Result)
		h.RescueSuperseded = sess.Status.Rescue.Superseded
		h.UnpushedRefs = len(sess.Status.Rescue.UnpushedRefs)
		h.OmittedRefs = int(sess.Status.Rescue.OmittedRefs)
	}
	return h
}

func codexOperatingMode(sess *v1alpha1.AgentSession) string {
	if sess.Spec.OperatingMode == "" {
		return codexhostpool.ModeRunning
	}
	return string(sess.Spec.OperatingMode)
}

func codexResolvedProfile(sess *v1alpha1.AgentSession, pod *corev1.Pod, def string) string {
	if pod != nil && pod.Labels[v1alpha1.LabelProfile] != "" {
		return pod.Labels[v1alpha1.LabelProfile]
	}
	if sess.Spec.Profile != "" {
		return sess.Spec.Profile
	}
	return def
}

func codexReadyRepos(agent *v1alpha1.AgentStatus) []string {
	if agent == nil || agent.ProjectBootstrap == nil {
		return nil
	}
	var out []string
	for _, repo := range agent.ProjectBootstrap.Repositories {
		if repo.State == codexHostBootstrapReady && repo.Name != "" {
			out = append(out, repo.Name)
		}
	}
	return out
}

func (s *Server) codexHostHandsOff(name string) bool {
	return s.CodexHosts != nil && slices.Contains(s.CodexHosts.HandsOff, name)
}

func (s *Server) codexHostBlocked(sess *v1alpha1.AgentSession) bool {
	if sess.Annotations[codexHostProtectedAnnotation] == "true" || s.codexHostHandsOff(sess.Name) {
		return true
	}
	if conditionIsTrue(sess.Status.Conditions, controller.ConditionRescueFailed) || conditionIsTrue(sess.Status.Conditions, controller.ConditionRemovalBlocked) {
		return true
	}
	return sess.Status.Rescue != nil && sess.Status.Rescue.Result == v1alpha1.RescueFailed
}

type codexLeaseTarget struct {
	name, uid, profile, repo string
	caller                   codexhostpool.Caller
	tmpl                     *templates.Templates
}

func (s *Server) leaseCodexHost(ctx context.Context, want codexLeaseTarget) (codexLeaseResult, error) {
	key := types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: want.name}
	for range codexHostLeaseTries {
		var sess v1alpha1.AgentSession
		if err := s.Live.Get(ctx, key, &sess); err != nil {
			if apierrors.IsNotFound(err) {
				return codexLeaseBusy, nil
			}
			return 0, err
		}
		if string(sess.UID) != want.uid {
			return codexLeaseBusy, nil
		}
		if !sess.DeletionTimestamp.IsZero() || s.codexHostBlocked(&sess) {
			return codexLeaseBusy, nil
		}
		pod, err := s.codexHostPod(ctx, &sess)
		if err != nil {
			return 0, err
		}
		now := s.now()
		host := codexHostFrom(&sess, pod, want.tmpl, s.codexHostHandsOff(sess.Name))
		if !codexhostpool.Suitable(host, want.caller, want.profile, want.repo, now) {
			return codexLeaseBusy, nil
		}
		if codexLeaseHeldByOther(sess.Annotations[codexHostLeaseAnnotation], want.caller.Name, want.caller.LaunchID, now) {
			return codexLeaseBusy, nil
		}
		orig := sess.DeepCopy()
		if sess.Annotations == nil {
			sess.Annotations = map[string]string{}
		}
		sess.Annotations[codexHostLeaseAnnotation] = formatCodexHostLease(codexHostLeasePurpose, now.Add(codexHostLeaseTTL), want.caller.Name, want.caller.LaunchID)
		err = s.Client.Patch(ctx, &sess, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		return codexLeaseWritten, nil
	}
	return codexLeaseConflict, nil
}

func codexLeaseHeldByOther(raw, caller, launchID string, now time.Time) bool {
	holder, id, exp, ok := parseCodexHostLease(raw)
	if !ok || !exp.After(now) {
		return false
	}
	return holder != caller || id != launchID
}

func formatCodexHostLease(purpose string, expiry time.Time, holder, launchID string) string {
	return purpose + " " + expiry.UTC().Format(time.RFC3339) + " " + holder + " " + launchID
}

func parseCodexHostLease(raw string) (holder, launchID string, expiry time.Time, ok bool) {
	parts := strings.Split(raw, " ")
	if len(parts) != 4 || parts[0] == "" || parts[2] == "" || !apiv1.ValidCodexLaunchID(parts[3]) {
		return "", "", time.Time{}, false
	}
	exp, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return "", "", time.Time{}, false
	}
	return parts[2], parts[3], exp, true
}

func conditionIsTrue(conds []metav1.Condition, typ string) bool {
	c := meta.FindStatusCondition(conds, typ)
	return c != nil && c.Status == metav1.ConditionTrue
}
