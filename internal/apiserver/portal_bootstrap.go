package apiserver

// This file intentionally implements one fixed bootstrap action, not a generic
// object API. Production RBAC/admission/permit activation remains separate.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	portalBootstrapTargetNamespace = "dev-agents"
	portalBootstrapTargetName      = "dev-env-v2-portal-admission"
	portalBootstrapPermitNamespace = "dev-env-system"
	portalBootstrapPermitName      = "dev-env-portal-bootstrap-permit"
	portalBootstrapSchema          = "1"
)

var portalBootstrapPayload = map[string]string{"schema": portalBootstrapSchema, "state": "closed"}

// PortalAdmissionBootstrap must be explicitly constructed by source wiring.
// A nil value leaves the route disabled even when a permit ConfigMap exists.
type PortalAdmissionBootstrap struct {
	Enabled bool
	// CreateClosed must be the dedicated no-retry client in production.
	CreateClosed func(context.Context, *corev1.ConfigMap) error
	// These immutable operator settings identify the externally approved snapshot.
	// They must never be inferred from the permit's own data or current identity.
	ExpectedPermitUID             string
	ExpectedPermitResourceVersion string
}

func (p *PortalAdmissionBootstrap) anchored() bool {
	if p == nil || !p.Enabled || p.ExpectedPermitUID == "" || len(validation.IsDNS1123Subdomain(p.ExpectedPermitUID)) != 0 {
		return false
	}
	rv, err := strconv.ParseUint(p.ExpectedPermitResourceVersion, 10, 64)
	return err == nil && rv > 0 && strconv.FormatUint(rv, 10) == p.ExpectedPermitResourceVersion
}

// Validate rejects an enabled action without its external approved snapshot.
func (p *PortalAdmissionBootstrap) Validate() error {
	if p != nil && p.Enabled && !p.anchored() {
		return fmt.Errorf("portal admission bootstrap requires a valid externally pinned permit UID and initial resourceVersion")
	}
	return nil
}

func portalBootstrapDigest() string {
	b, _ := json.Marshal(portalBootstrapPayload)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (s *Server) portalAdmissionBootstrap(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if !s.PortalAdmissionBootstrap.anchored() || s.Live == nil || s.Client == nil || s.PortalAdmissionBootstrap.CreateClosed == nil {
		return 0, nil, notFound("portal admission bootstrap is disabled")
	}
	if c == nil || c.kind != kindSession || c.session == nil || c.pod == nil {
		return 0, nil, forbidden("portal admission bootstrap requires its configured session pod")
	}
	req, err := strictPortalBootstrapRequest(r.Body)
	if err != nil || r.URL.RawQuery != "" || len(validation.IsDNS1123Label(req.RequestID)) != 0 {
		return 0, nil, badRequest("portal admission bootstrap requires one valid requestID")
	}
	current, err := s.livePortalBootstrapCaller(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	permitKey := types.NamespacedName{Namespace: portalBootstrapPermitNamespace, Name: portalBootstrapPermitName}
	var permit corev1.ConfigMap
	if err := s.Live.Get(ctx, permitKey, &permit); err != nil {
		return 0, nil, internal("portal admission bootstrap permit read failed")
	}
	if err := validPortalBootstrapPermit(&permit, s.PortalAdmissionBootstrap, req.RequestID, current, c.pod.UID, s.now()); err != nil {
		return 0, nil, err
	}
	if permit.Data["state"] == "consumed" {
		return s.reconcilePortalBootstrap(ctx, &permit, req.RequestID)
	}
	targetKey := types.NamespacedName{Namespace: portalBootstrapTargetNamespace, Name: portalBootstrapTargetName}
	var existing corev1.ConfigMap
	observedErr := s.Live.Get(ctx, targetKey, &existing)
	if observedErr != nil && !apierrors.IsNotFound(observedErr) {
		return 0, nil, internal("portal admission target read failed")
	}
	if observedErr == nil && !exactPortalBootstrapTarget(&existing) {
		return 0, nil, portalBootstrapUncertain()
	}
	// This check uses the uncached APIReader again immediately before the CAS.
	// Authentication/classification may have used an earlier cache snapshot.
	current, err = s.livePortalBootstrapCaller(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	if err = validPortalBootstrapPermit(&permit, s.PortalAdmissionBootstrap, req.RequestID, current, c.pod.UID, s.now()); err != nil {
		return 0, nil, err
	}
	permit.Data["state"] = "consumed"
	permit.Data["consumedFromResourceVersion"] = permit.ResourceVersion
	permit.Data["attemptedAt"] = s.now().UTC().Format(time.RFC3339Nano)
	permit.Data["outcome"] = "CreateUncertain"
	if observedErr == nil {
		setPortalBootstrapReceipt(&permit, &existing, "Present")
	}
	if err := s.Client.Update(ctx, &permit); err != nil {
		// A CAS loser or lost update response may only observe; it cannot create.
		var after corev1.ConfigMap
		if s.Live.Get(ctx, types.NamespacedName{Namespace: permit.Namespace, Name: permit.Name}, &after) == nil &&
			validPortalBootstrapPermit(&after, s.PortalAdmissionBootstrap, req.RequestID, current, c.pod.UID, s.now()) == nil && after.Data["state"] == "consumed" {
			return s.reconcilePortalBootstrap(ctx, &after, req.RequestID)
		}
		return 0, nil, portalBootstrapUncertain()
	}
	if observedErr == nil {
		return s.reconcilePortalBootstrap(ctx, &permit, req.RequestID)
	}
	// A producer can win after consumption. Record its UID as Present; it is
	// never attributed to this action as Created.
	err = s.Live.Get(ctx, targetKey, &existing)
	if err == nil {
		return s.recordPortalBootstrapReceipt(ctx, &permit, &existing, req.RequestID, "Present")
	}
	if !apierrors.IsNotFound(err) {
		return 0, nil, portalBootstrapUncertain()
	}
	// A consumed permit is never retried, but revocation or caller termination
	// before the final create boundary must still stop the sole attempt.
	var latestPermit corev1.ConfigMap
	if s.Live.Get(ctx, permitKey, &latestPermit) != nil || latestPermit.ResourceVersion != permit.ResourceVersion {
		return 0, nil, portalBootstrapUncertain()
	}
	current, err = s.livePortalBootstrapCaller(ctx, c)
	if err != nil || validPortalBootstrapPermit(&latestPermit, s.PortalAdmissionBootstrap, req.RequestID, current, c.pod.UID, s.now()) != nil {
		return 0, nil, portalBootstrapUncertain()
	}
	target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: portalBootstrapTargetNamespace, Name: portalBootstrapTargetName}, Data: map[string]string{"schema": portalBootstrapSchema, "state": "closed"}}
	if err := s.PortalAdmissionBootstrap.CreateClosed(ctx, target); err != nil {
		if apierrors.IsAlreadyExists(err) && s.Live.Get(ctx, targetKey, &existing) == nil {
			return s.recordPortalBootstrapReceipt(ctx, &permit, &existing, req.RequestID, "Present")
		}
		// Even timeout before commit consumes authority. A matching object on a
		// later read cannot establish who created it; manual disposition is needed.
		return 0, nil, portalBootstrapUncertain()
	}
	return s.recordPortalBootstrapReceipt(ctx, &permit, target, req.RequestID, "Created")
}

func portalBootstrapUncertain() error {
	return newError(http.StatusConflict, apiv1.CodeConflict, "portal admission bootstrap is consumed, uncertain or drifted; manual disposition required; do not repeat the POST")
}

func setPortalBootstrapReceipt(permit, target *corev1.ConfigMap, outcome string) {
	permit.Data["outcome"] = outcome
	permit.Data["targetUID"] = string(target.UID)
	permit.Data["targetRV"] = target.ResourceVersion
}

func (s *Server) recordPortalBootstrapReceipt(ctx context.Context, permit, target *corev1.ConfigMap, requestID, outcome string) (int, any, error) {
	if !exactPortalBootstrapTarget(target) {
		return 0, nil, portalBootstrapUncertain()
	}
	setPortalBootstrapReceipt(permit, target, outcome)
	// Only the permit is updated. A failed receipt write leaves the durable
	// consumed/uncertain state; never reconstruct provenance from payload alone.
	if s.Client.Update(ctx, permit) != nil {
		return 0, nil, portalBootstrapUncertain()
	}
	status := http.StatusOK
	if outcome == "Created" {
		status = http.StatusCreated
	}
	return status, apiv1.PortalAdmissionBootstrapResult{Disposition: outcome, RequestID: requestID, TargetUID: string(target.UID), TargetRV: target.ResourceVersion}, nil
}

func (s *Server) reconcilePortalBootstrap(ctx context.Context, permit *corev1.ConfigMap, requestID string) (int, any, error) {
	if (permit.Data["outcome"] != "Present" && permit.Data["outcome"] != "Created") || permit.Data["targetUID"] == "" || permit.Data["targetRV"] == "" {
		return 0, nil, portalBootstrapUncertain()
	}
	var target corev1.ConfigMap
	if s.Live.Get(ctx, types.NamespacedName{Namespace: portalBootstrapTargetNamespace, Name: portalBootstrapTargetName}, &target) != nil || !exactPortalBootstrapTarget(&target) || string(target.UID) != permit.Data["targetUID"] {
		return 0, nil, portalBootstrapUncertain()
	}
	return http.StatusOK, apiv1.PortalAdmissionBootstrapResult{Disposition: "Present", RequestID: requestID, TargetUID: string(target.UID), TargetRV: target.ResourceVersion}, nil
}

func validPortalBootstrapPermit(p *corev1.ConfigMap, anchor *PortalAdmissionBootstrap, requestID string, sess *v1alpha1.AgentSession, podUID types.UID, now time.Time) error {
	if !anchor.anchored() || string(p.UID) != anchor.ExpectedPermitUID || p.Data == nil || (p.Data["state"] != "active" && p.Data["state"] != "consumed") || p.Data["requestID"] != requestID || p.Data["sessionUID"] != string(sess.UID) || p.Data["podUID"] != string(podUID) || p.Data["payloadDigest"] != portalBootstrapDigest() || p.Data["permitUID"] != string(p.UID) || p.Data["sessionGeneration"] != fmt.Sprint(sess.Generation) {
		return forbidden("portal admission bootstrap permit does not match this fixed caller and request")
	}
	if p.Data["state"] == "active" && p.ResourceVersion != anchor.ExpectedPermitResourceVersion || p.Data["state"] == "consumed" && p.Data["consumedFromResourceVersion"] != anchor.ExpectedPermitResourceVersion {
		return forbidden("portal admission bootstrap permit incarnation is not approved")
	}
	expires, err := time.Parse(time.RFC3339, p.Data["expiresAt"])
	if err != nil || !now.Before(expires) || p.Data["revokedAt"] != "" {
		return forbidden("portal admission bootstrap permit is expired or revoked")
	}
	return nil
}

func (s *Server) livePortalBootstrapCaller(ctx context.Context, c *caller) (*v1alpha1.AgentSession, error) {
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: c.session.Namespace, Name: c.session.Name}, &sess); err != nil || sess.UID == "" || sess.UID != c.session.UID || sess.Namespace != portalBootstrapTargetNamespace || sess.Namespace != c.identity.Namespace || !sess.DeletionTimestamp.IsZero() || sess.Spec.Mode != v1alpha1.ModeRemote || sess.Spec.OperatingMode != v1alpha1.OperatingModeRunning || (sess.Status.Phase != v1alpha1.PhaseRunning && sess.Status.Phase != v1alpha1.PhaseIdle) {
		return nil, forbidden("portal admission bootstrap caller session is no longer live")
	}
	var pod corev1.Pod
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: c.pod.Namespace, Name: c.pod.Name}, &pod); err != nil || pod.UID == "" || pod.UID != c.pod.UID || string(pod.UID) != c.identity.PodUID || pod.Name != c.identity.PodName || pod.Namespace != sess.Namespace || !pod.DeletionTimestamp.IsZero() || !s.sessionPodAccount(pod.Spec.ServiceAccountName, c) || pod.Status.Phase != corev1.PodRunning {
		return nil, forbidden("portal admission bootstrap caller pod is no longer live")
	}
	owner := metav1.GetControllerOf(&pod)
	if owner == nil || owner.Kind != "AgentSession" || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.Name != sess.Name || owner.UID != sess.UID {
		return nil, forbidden("portal admission bootstrap caller ownership changed")
	}
	return &sess, nil
}

func strictPortalBootstrapRequest(body io.Reader) (apiv1.PortalAdmissionBootstrapRequest, error) {
	b, err := io.ReadAll(io.LimitReader(body, 1025))
	if err != nil || len(b) > 1024 || !utf8.Valid(b) {
		return apiv1.PortalAdmissionBootstrapRequest{}, io.ErrUnexpectedEOF
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return apiv1.PortalAdmissionBootstrapRequest{}, io.ErrUnexpectedEOF
	}
	var req apiv1.PortalAdmissionBootstrapRequest
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return req, err
		}
		name, ok := key.(string)
		if !ok || seen[name] || name != "requestID" {
			return req, io.ErrUnexpectedEOF
		}
		seen[name] = true
		if err := dec.Decode(&req.RequestID); err != nil {
			return req, err
		}
	}
	if tok, err = dec.Token(); err != nil || tok != json.Delim('}') || dec.Decode(new(any)) != io.EOF || !seen["requestID"] {
		return req, io.ErrUnexpectedEOF
	}
	return req, nil
}

func exactPortalBootstrapTarget(cm *corev1.ConfigMap) bool {
	return cm.UID != "" && cm.ResourceVersion != "" && len(cm.Labels) == 0 && len(cm.Annotations) == 0 && cm.GenerateName == "" && cm.DeletionTimestamp.IsZero() && cm.Namespace == portalBootstrapTargetNamespace && cm.Name == portalBootstrapTargetName && cm.BinaryData == nil && cm.Immutable == nil && len(cm.OwnerReferences) == 0 && len(cm.Finalizers) == 0 && len(cm.Data) == 2 && cm.Data["schema"] == portalBootstrapSchema && cm.Data["state"] == "closed"
}
