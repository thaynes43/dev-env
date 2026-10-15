package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

// TaskClaimReadProjects is an independent project metadata grant. Ordinary
// session/client permissions never imply it. Sessions remain in their saved
// project; configured coordinators may discover metadata in this explicit scope.
func ParseTaskClaimReadProjects(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 4096 {
		return nil, errors.New("task claim read scope exceeds its bound")
	}
	projects := strings.Split(raw, ",")
	seen := map[string]bool{}
	if len(projects) > projectcatalog.MaxProjects {
		return nil, errors.New("task claim read scope exceeds its bound")
	}
	for _, p := range projects {
		if !projectcatalog.ValidName(p) || seen[p] {
			return nil, errors.New("task claim read scope must name distinct explicit projects")
		}
		seen[p] = true
	}
	return projects, nil
}

func claimDenied() error {
	return forbidden("task claim access requires its explicit project scope and live owned identity")
}

func (s *Server) claimsAvailable() error {
	if s.TaskClaims == nil || s.Live == nil || s.Projects == nil || len(s.TaskClaimReadProjects) == 0 {
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "task claims are not enabled")
	}
	return nil
}

// claimCaller rechecks current bound Pod and original Session UID through Live.
// resolveCaller's legacy cached classification is insufficient for this new ACL.
func (s *Server) claimCaller(ctx context.Context, c *caller) (*v1alpha1.AgentSession, error) {
	if c.kind == kindCoordinator {
		for _, host := range s.Policy.Coordinators {
			if host.ServiceAccount == c.identity.serviceAccountRef() {
				_, err := s.resolveCoordinator(ctx, c.identity, host)
				return nil, err
			}
		}
		return nil, claimDenied()
	}
	if c.kind != kindSession || c.session == nil || c.pod == nil || s.Live == nil {
		return nil, claimDenied()
	}
	var pod corev1.Pod
	var sess v1alpha1.AgentSession
	if s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: c.identity.PodName}, &pod) != nil ||
		string(pod.UID) != c.identity.PodUID || !pod.DeletionTimestamp.IsZero() || pod.Spec.ServiceAccountName != s.Policy.SessionServiceAccount ||
		s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: c.session.Name}, &sess) != nil ||
		sess.UID == "" || sess.UID != c.session.UID || !sess.DeletionTimestamp.IsZero() || !metav1.IsControlledBy(&pod, &sess) {
		return nil, claimDenied()
	}
	return &sess, nil
}

// sameSessionBinding is the final identity fence for the coordination routes.
// It compares what binds a Session to its authority: UID, generation and spec,
// deletion, labels, annotations (the saved project snapshot among them), owner
// references and finalizers. It ignores status, resourceVersion and managed
// fields, so a heartbeat or controller status write between the first read and
// the fence is not a refusal. Callers recheck the live status facts they need.
func sameSessionBinding(a, b *v1alpha1.AgentSession) bool {
	return a != nil && b != nil && a.UID != "" && a.UID == b.UID && a.Namespace == b.Namespace && a.Name == b.Name &&
		a.Generation == b.Generation && a.DeletionTimestamp.Equal(b.DeletionTimestamp) &&
		maps.Equal(a.Labels, b.Labels) && maps.Equal(a.Annotations, b.Annotations) &&
		reflect.DeepEqual(a.OwnerReferences, b.OwnerReferences) && slices.Equal(a.Finalizers, b.Finalizers) && reflect.DeepEqual(a.Spec, b.Spec)
}

func claimSnapshot(sess *v1alpha1.AgentSession) (projectcatalog.Snapshot, error) {
	snapshot, err := projectcatalog.ParseSnapshot([]byte(sess.Annotations[v1alpha1.AnnotationProjectSnapshot]))
	if err != nil || snapshot.Selected().Name != sess.Spec.Repo || sess.Spec.Workspace != nil {
		return projectcatalog.Snapshot{}, claimDenied()
	}
	return snapshot, nil
}

func (s *Server) claimReadScope(ctx context.Context, c *caller, project string) error {
	if !projectcatalog.ValidName(project) || !slices.Contains(s.TaskClaimReadProjects, project) {
		return claimDenied()
	}
	sess, err := s.claimCaller(ctx, c)
	if err != nil {
		return err
	}
	if c.kind == kindSession {
		snapshot, err := claimSnapshot(sess)
		if err != nil || snapshot.Project() != project {
			return claimDenied()
		}
	}
	return nil
}

func (s *Server) claimOwner(ctx context.Context, c *caller, req apiv1.CreateTaskClaimRequest) (*v1alpha1.AgentSession, error) {
	own, err := s.claimCaller(ctx, c)
	if err != nil {
		return nil, err
	}
	if c.kind == kindSession {
		if req.Owner != nil && (req.Owner.Namespace != own.Namespace || req.Owner.Name != own.Name || req.Owner.UID != string(own.UID)) {
			return nil, claimDenied()
		}
		return own, nil
	}
	if req.Owner == nil || req.Owner.Namespace != s.Policy.SessionNamespace || req.Owner.Name == "" || req.Owner.UID == "" {
		return nil, claimDenied()
	}
	child, err := s.childForCoordinator(ctx, types.NamespacedName{Namespace: req.Owner.Namespace, Name: req.Owner.Name}, c, types.UID(req.Owner.UID))
	if err != nil || !child.DeletionTimestamp.IsZero() {
		return nil, claimDenied()
	}
	return child, nil
}

func (s *Server) createTaskClaim(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.claimsAvailable(); err != nil {
		return 0, nil, err
	}
	var req apiv1.CreateTaskClaimRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		return 0, nil, badRequest("expected a bounded task claim request with explicit identity")
	}
	identity := coord.TaskIdentity{Project: req.Identity.Project, Repository: req.Identity.Repository, TaskKey: req.Identity.TaskKey}
	if !taskclaim.ValidIdentity(identity) {
		return 0, nil, badRequest("task identity requires project, declared repository and canonical issue:<number> or task:<key>")
	}
	if err := s.claimReadScope(ctx, c, identity.Project); err != nil {
		return 0, nil, err
	}
	owner, err := s.claimOwner(ctx, c, req)
	if err != nil {
		return 0, nil, err
	}
	snapshot, err := claimSnapshot(owner)
	if err != nil || snapshot.Project() != identity.Project || snapshot.Selected().Name != identity.Repository {
		return 0, nil, claimDenied()
	}
	// Current named catalog authority must still accept this repository identity;
	// provenance retains the owner's saved catalog/rules, rather than refreshing it.
	_, selected, err := s.Projects.snapshot(ctx, s.Live, identity.Project, identity.Repository)
	if err != nil || !strings.EqualFold(selected.GitHub, snapshot.Selected().GitHub) {
		return 0, nil, claimDenied()
	}
	provenance := coord.ClaimProvenance{CatalogRevision: snapshot.CatalogRevision(), RulesRevision: snapshot.RulesRevision(),
		GitHub: strings.ToLower(snapshot.Selected().GitHub), DefaultBranch: snapshot.Selected().DefaultBranch, InitialPinState: "Unknown"}
	if a := owner.Status.Agent; a != nil && a.LastHeartbeat != nil && !a.LastHeartbeat.IsZero() && !a.LastHeartbeat.After(s.now()) {
		provenance.ReportedHead, provenance.ReportedAt = a.Head, a.LastHeartbeat.DeepCopy()
		probe := provenance
		probe.ReportedHead, probe.ReportedAt = "", nil
		// Invalid/unbounded heartbeat strings are omitted, never copied as provenance.
		if len(a.Head) != 40 && len(a.Head) != 64 {
			provenance = probe
		}
	}
	spec := coord.TaskClaimSpec{Identity: identity, Owner: coord.SessionIdentity{Namespace: owner.Namespace, Name: owner.Name, UID: string(owner.UID)},
		Actor: coord.ClaimActor{Kind: string(c.kind), ServiceAccount: c.identity.serviceAccountRef(), PodName: c.identity.PodName, PodUID: c.identity.PodUID}, Provenance: provenance}
	if !taskclaim.ValidSpec(spec) {
		spec.Provenance.ReportedHead, spec.Provenance.ReportedAt = "", nil
		if !taskclaim.ValidSpec(spec) {
			return 0, nil, claimDenied()
		}
	}
	// Fence the owner again after catalog I/O and before the atomic create.
	fresh, err := s.claimOwner(ctx, c, req)
	if err != nil || !sameSessionBinding(fresh, owner) {
		return 0, nil, claimDenied()
	}
	claim, created, err := s.TaskClaims.Create(ctx, spec)
	if errors.Is(err, taskclaim.ErrLimit) {
		return 0, nil, newError(http.StatusTooManyRequests, apiv1.CodeLimitExceeded, "task claim limit reached for this owner Session or project; nothing was claimed")
	}
	if err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "task ownership is unconfirmed; retry the same identity and owner")
	}
	status, outcome := http.StatusOK, "Confirmed"
	// The same work may already be claimed under another repository key.
	if claim.Spec.Owner != spec.Owner || claim.Spec.Identity != spec.Identity {
		status, outcome = http.StatusConflict, "Conflict"
	} else if created {
		status, outcome = http.StatusCreated, "Created"
	}
	return status, apiv1.TaskClaimResult{Outcome: outcome, Claim: s.claimView(ctx, claim)}, nil
}

func (s *Server) getTaskClaim(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.claimsAvailable(); err != nil {
		return 0, nil, err
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["project"]) != 1 || len(query["uid"]) > 1 {
		return 0, nil, badRequest("invalid task claim query")
	}
	for key := range query {
		if key != "project" && key != "uid" {
			return 0, nil, badRequest("invalid task claim query")
		}
	}
	project, uid := query.Get("project"), query.Get("uid")
	if _, suppliedUID := query["uid"]; suppliedUID && !protocol.ValidKubeUID(uid) {
		return 0, nil, badRequest("invalid task claim UID")
	}
	if err := s.claimReadScope(ctx, c, project); err != nil {
		return 0, nil, err
	}
	if !taskclaim.ValidName(r.PathValue("claim")) {
		return 0, nil, badRequest("invalid task claim name")
	}
	name := r.PathValue("claim")
	claim, err := s.TaskClaims.Get(ctx, name)
	if err != nil {
		if errors.Is(err, taskclaim.ErrNotFound) {
			if !s.CoordinationArchiveReads {
				return 0, nil, notFound("task claim not found")
			}
			return s.archivedTaskClaimView(ctx, c, project, name, uid)
		}
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "task ownership is unconfirmed")
	}
	if claim.Spec.Identity.Project != project {
		// The digest is deterministic: knowing a foreign identity must not
		// reveal whether its scoped claim exists.
		return 0, nil, notFound("task claim not found")
	}
	if uid != "" && uid != string(claim.UID) {
		if !s.CoordinationArchiveReads {
			return 0, nil, notFound("task claim not found")
		}
		return s.archivedTaskClaimView(ctx, c, project, name, uid)
	}
	return http.StatusOK, s.claimView(ctx, claim), nil
}

func (s *Server) listTaskClaims(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := s.claimsAvailable(); err != nil {
		return 0, nil, err
	}
	project := r.URL.Query().Get("project")
	if err := s.claimReadScope(ctx, c, project); err != nil {
		return 0, nil, err
	}
	archivedValues := r.URL.Query()["archived"]
	if len(archivedValues) > 1 {
		return 0, nil, badRequest("archived must be only")
	}
	archived := r.URL.Query().Get("archived")
	if archived != "" && archived != "only" {
		return 0, nil, badRequest("archived must be only")
	}
	limit := int64(100)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || limit < 1 || limit > 100 {
			return 0, nil, badRequest("limit must be between 1 and 100")
		}
	}
	if archived == "only" {
		for key, values := range r.URL.Query() {
			if len(values) != 1 || (key != "project" && key != "limit" && key != "continue" && key != "archived") {
				return 0, nil, badRequest("invalid archived task claim query")
			}
		}
		if !s.CoordinationArchiveReads {
			return 0, nil, notFound("task claim not found")
		}
		return s.listArchivedTaskClaims(ctx, project, limit, r.URL.Query().Get("continue"))
	}
	claims, next, err := s.TaskClaims.List(ctx, project, limit, r.URL.Query().Get("continue"))
	if err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "task claims are unavailable")
	}
	views := make([]apiv1.TaskClaim, 0, len(claims))
	for i := range claims {
		views = append(views, s.claimView(ctx, &claims[i]))
	}
	return http.StatusOK, apiv1.TaskClaimList{Items: views, Continue: next}, nil
}

func (s *Server) claimView(ctx context.Context, c *coord.TaskClaim) apiv1.TaskClaim {
	view := claimMetadata(c)
	var owner v1alpha1.AgentSession
	if s.Live.Get(ctx, types.NamespacedName{Namespace: c.Spec.Owner.Namespace, Name: c.Spec.Owner.Name}, &owner) == nil && string(owner.UID) == c.Spec.Owner.UID {
		if !owner.DeletionTimestamp.IsZero() {
			view.OwnerState = "Deleting"
		} else if owner.Status.Phase != "" {
			view.OwnerState = string(owner.Status.Phase)
		}
	}
	return view
}

// claimMetadata projects immutable TaskClaim data. Archive reads use this
// projection without consulting a live owner Session.
func claimMetadata(c *coord.TaskClaim) apiv1.TaskClaim {
	p := c.Spec.Provenance
	view := apiv1.TaskClaim{Name: c.Name, UID: string(c.UID), CreatedAt: c.CreationTimestamp.Time,
		Identity:   apiv1.TaskClaimIdentity{Project: c.Spec.Identity.Project, Repository: c.Spec.Identity.Repository, TaskKey: c.Spec.Identity.TaskKey},
		Owner:      apiv1.TaskClaimOwner{Namespace: c.Spec.Owner.Namespace, Name: c.Spec.Owner.Name, UID: c.Spec.Owner.UID},
		Actor:      apiv1.TaskClaimActor{Kind: c.Spec.Actor.Kind, ServiceAccount: c.Spec.Actor.ServiceAccount, PodName: c.Spec.Actor.PodName, PodUID: c.Spec.Actor.PodUID},
		Provenance: apiv1.TaskClaimProvenance{CatalogRevision: p.CatalogRevision, RulesRevision: p.RulesRevision, GitHub: p.GitHub, DefaultBranch: p.DefaultBranch, InitialPinState: p.InitialPinState, ReportedHead: p.ReportedHead},
		OwnerState: "Unknown", StopState: "Unknown"}
	if p.ReportedAt != nil {
		at := p.ReportedAt.Time
		view.Provenance.ReportedAt = &at
	}
	return view
}
