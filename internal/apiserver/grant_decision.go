package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/grantdecision"
)

type grantDecisionSummary struct {
	Grant           apiv1.Grant           `json:"grant"`
	Binding         grantdecision.Binding `json:"binding"`
	ResourceVersion string                `json:"resourceVersion"`
}
type grantDecisionRequest struct {
	Binding         grantdecision.Binding `json:"binding"`
	ResourceVersion string                `json:"resourceVersion"`
	Decision        string                `json:"decision"`
	TTL             string                `json:"ttl,omitempty"`
	Reason          string                `json:"reason,omitempty"`
	RelayRef        string                `json:"relayRef,omitempty"`
}

// decisionGrant reads the current request and independently verifies the exact
// requesting session and running pod. It never treats caller input as identity.
func (s *Server) decisionGrant(ctx context.Context, r *http.Request, c *caller) (*v1alpha1.AccessGrant, grantdecision.Binding, error) {
	if !s.GrantDecisionEnabled || (c.kind != kindHuman && c.kind != kindCoordinator && c.kind != kindSession) {
		return nil, grantdecision.Binding{}, forbidden("grant decision is unavailable to this caller")
	}
	key, err := s.grantKey(r)
	if err != nil {
		return nil, grantdecision.Binding{}, err
	}
	var g v1alpha1.AccessGrant
	if err := s.Live.Get(ctx, key, &g); err != nil {
		if c.kind == kindCoordinator {
			return nil, grantdecision.Binding{}, coordinatorDenied()
		}
		return nil, grantdecision.Binding{}, fromGrantKubeError(err, "grant")
	}
	if c.kind == kindCoordinator {
		if _, err := s.childForCoordinator(ctx, types.NamespacedName{Namespace: g.Namespace, Name: g.Spec.Requester.Session}, c, g.Spec.Requester.SessionUID); err != nil {
			return nil, grantdecision.Binding{}, err
		}
	}
	if c.kind == kindSession {
		if c.session == nil || c.pod == nil || c.session.UID == g.Spec.Requester.SessionUID || c.session.Name == g.Spec.Requester.Session || !c.session.DeletionTimestamp.IsZero() || !c.pod.DeletionTimestamp.IsZero() || c.pod.Status.Phase != corev1.PodRunning {
			return nil, grantdecision.Binding{}, forbidden("only a current parent session may relay a child decision")
		}
	}
	if g.Spec.Release || !g.DeletionTimestamp.IsZero() || (g.Status.Phase != "" && g.Status.Phase != v1alpha1.GrantPending) || !s.now().Before(g.CreationTimestamp.Add(30*time.Minute)) {
		return nil, grantdecision.Binding{}, newError(http.StatusConflict, apiv1.CodeConflict, "grant is not pending")
	}
	var session v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: g.Namespace, Name: g.Spec.Requester.Session}, &session); err != nil || session.UID != g.Spec.Requester.SessionUID || !session.DeletionTimestamp.IsZero() {
		return nil, grantdecision.Binding{}, newError(http.StatusConflict, apiv1.CodeConflict, "requesting session is unavailable or replaced")
	}
	if c.kind == kindSession && (session.Spec.Parent != c.session.Name || (session.Spec.UsageCaptureParentUID != "" && session.Spec.UsageCaptureParentUID != string(c.session.UID))) {
		return nil, grantdecision.Binding{}, forbidden("only a current parent session may relay a child decision")
	}
	var pod corev1.Pod
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: g.Namespace, Name: session.Name}, &pod); err != nil || pod.UID == "" || !metav1.IsControlledBy(&pod, &session) || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning || pod.Labels[v1alpha1.LabelHold] == "true" || pod.Labels[v1alpha1.LabelSession] != session.Name {
		return nil, grantdecision.Binding{}, newError(http.StatusConflict, apiv1.CodeConflict, "requesting pod is unavailable or replaced")
	}
	binding, err := grantdecision.Bind(&g, pod.UID)
	if err != nil {
		return nil, grantdecision.Binding{}, newError(http.StatusConflict, apiv1.CodeConflict, "request cannot be bound")
	}
	return &g, binding, nil
}

func (s *Server) inspectGrantDecision(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	g, binding, err := s.decisionGrant(ctx, r, c)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, grantDecisionSummary{Grant: s.grantView(g), Binding: binding, ResourceVersion: g.ResourceVersion}, nil
}

func (s *Server) submitGrantDecision(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if !s.GrantDecisionEnabled || (c.kind != kindHuman && c.kind != kindCoordinator && c.kind != kindSession) {
		return 0, nil, forbidden("grant decision is unavailable to this caller")
	}
	var req grantDecisionRequest
	if err := decodeJSON(w, r, 4096, &req, true); err != nil {
		return 0, nil, err
	}
	if req.Decision != "approve" && req.Decision != "deny" || len(req.Reason) > 512 || (req.Decision == "approve" && req.Reason != "") || (req.Decision == "deny" && req.TTL != "") {
		return 0, nil, newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "invalid grant decision")
	}
	if c.kind != kindHuman && !grantdecision.ValidRelayRef(req.RelayRef) {
		return 0, nil, newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "a bounded relay reference is required")
	}
	if c.kind == kindHuman && req.RelayRef != "" {
		return 0, nil, newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "human decisions do not carry a relay reference")
	}
	if req.TTL != "" {
		if d, err := time.ParseDuration(req.TTL); err != nil || d <= 0 {
			return 0, nil, newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "invalid decision TTL")
		}
	}
	g, binding, err := s.decisionGrant(ctx, r, c)
	if err != nil {
		return 0, nil, err
	}
	if binding != req.Binding || req.ResourceVersion == "" || g.ResourceVersion != req.ResourceVersion || g.Annotations[grantdecision.HandoffAnnotation] != "" {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "request changed or already has a decision")
	}
	principal := "human/" + c.parent
	if c.kind == kindCoordinator {
		principal = "coordinator/" + c.parent
	}
	if c.kind == kindSession {
		principal = "session/" + c.session.Name + "/" + string(c.session.UID) + "/" + string(c.pod.UID)
	}
	h := grantdecision.Handoff{Binding: binding, Principal: principal, Decision: req.Decision, TTL: req.TTL, Reason: strings.TrimSpace(req.Reason), RelayRef: req.RelayRef}
	encoded, err := json.Marshal(h)
	if err != nil {
		return 0, nil, newError(http.StatusInternalServerError, apiv1.CodeInternal, "could not encode decision")
	}
	orig := g.DeepCopy()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[grantdecision.HandoffAnnotation] = string(encoded)
	if err := s.Client.Patch(ctx, g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return 0, nil, fromGrantKubeError(err, "record decision handoff")
	}
	return http.StatusAccepted, map[string]string{"status": "submitted"}, nil
}
