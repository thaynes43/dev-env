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
	"strings"
	"time"

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
type PortalAdmissionBootstrap struct{ Enabled bool }

func portalBootstrapDigest() string {
	b, _ := json.Marshal(portalBootstrapPayload)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (s *Server) portalAdmissionBootstrap(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if s.PortalAdmissionBootstrap == nil || !s.PortalAdmissionBootstrap.Enabled || s.Live == nil || s.Client == nil {
		return 0, nil, notFound("portal admission bootstrap is disabled")
	}
	if c == nil || c.kind != kindSession || c.session == nil || c.pod == nil {
		return 0, nil, forbidden("portal admission bootstrap requires its configured session pod")
	}
	req, err := strictPortalBootstrapRequest(r.Body)
	if err != nil || len(validation.IsDNS1123Label(req.RequestID)) != 0 {
		return 0, nil, badRequest("portal admission bootstrap requires one valid requestID")
	}
	current, err := s.livePortalBootstrapCaller(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	permitKey := types.NamespacedName{Namespace: portalBootstrapPermitNamespace, Name: portalBootstrapPermitName}
	var permit corev1.ConfigMap
	if err := s.Live.Get(ctx, permitKey, &permit); err != nil {
		return 0, nil, fromKubeError(err, "read fixed bootstrap permit")
	}
	if err := validPortalBootstrapPermit(&permit, req.RequestID, current, c.pod.UID, s.now()); err != nil {
		return 0, nil, err
	}
	targetKey := types.NamespacedName{Namespace: portalBootstrapTargetNamespace, Name: portalBootstrapTargetName}
	var existing corev1.ConfigMap
	err = s.Live.Get(ctx, targetKey, &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return 0, nil, fromKubeError(err, "read portal admission target")
	}
	if err == nil && !exactPortalBootstrapTarget(&existing) {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "portal admission target already exists with unexpected content")
	}
	// Consume first, via resourceVersion CAS.  The permit is the durable receipt:
	// a replay, timeout, process restart, or later target deletion cannot make a
	// second CREATE eligible.
	permit.Data["state"] = "consumed"
	permit.Data["attemptedAt"] = s.now().UTC().Format(time.RFC3339Nano)
	permit.Data["outcome"] = "CreateAttempted"
	permit.Data["permitUID"] = string(permit.UID)
	permit.Data["sessionGeneration"] = fmt.Sprint(current.Generation)
	if err == nil {
		permit.Data["outcome"] = "Present"
		permit.Data["targetUID"] = string(existing.UID)
		permit.Data["targetRV"] = existing.ResourceVersion
	}
	if err := s.Client.Update(ctx, &permit); err != nil {
		return 0, nil, fromKubeError(err, "consume fixed bootstrap permit")
	}
	if err == nil {
		return http.StatusOK, apiv1.PortalAdmissionBootstrapResult{Disposition: "Present", RequestID: req.RequestID, TargetUID: string(existing.UID), TargetRV: existing.ResourceVersion}, nil
	}
	// Re-read after consumption: a concurrent producer may have made the fixed
	// witness while this request was reserving its one attempt.
	err = s.Live.Get(ctx, targetKey, &existing)
	if err == nil {
		if exactPortalBootstrapTarget(&existing) {
			return http.StatusOK, apiv1.PortalAdmissionBootstrapResult{Disposition: "Present", RequestID: req.RequestID, TargetUID: string(existing.UID), TargetRV: existing.ResourceVersion}, nil
		}
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "portal admission target already exists with unexpected content")
	}
	if !apierrors.IsNotFound(err) {
		return 0, nil, fromKubeError(err, "read portal admission target")
	}
	target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: portalBootstrapTargetNamespace, Name: portalBootstrapTargetName}, Data: map[string]string{"schema": portalBootstrapSchema, "state": "closed"}}
	if err := s.Client.Create(ctx, target); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "portal admission target appeared after permit consumption")
		}
		return 0, nil, fromKubeError(err, "create fixed portal admission target")
	}
	if target.UID == "" || target.ResourceVersion == "" {
		return 0, nil, internal("portal admission target receipt is incomplete")
	}
	permit.Data["outcome"] = "Created"
	permit.Data["targetUID"] = string(target.UID)
	permit.Data["targetRV"] = target.ResourceVersion
	if err := s.Client.Update(ctx, &permit); err != nil {
		return 0, nil, internal("portal admission target receipt could not be recorded; do not repeat the POST")
	}
	return http.StatusCreated, apiv1.PortalAdmissionBootstrapResult{Disposition: "Created", RequestID: req.RequestID, TargetUID: string(target.UID), TargetRV: target.ResourceVersion}, nil
}

func validPortalBootstrapPermit(p *corev1.ConfigMap, requestID string, sess *v1alpha1.AgentSession, podUID types.UID, now time.Time) error {
	if p.UID == "" || p.Data == nil || p.Data["state"] != "active" || p.Data["requestID"] != requestID || p.Data["sessionUID"] != string(sess.UID) || p.Data["podUID"] != string(podUID) || p.Data["payloadDigest"] != portalBootstrapDigest() || p.Data["permitUID"] != string(p.UID) || p.Data["sessionGeneration"] != fmt.Sprint(sess.Generation) {
		return forbidden("portal admission bootstrap permit does not match this fixed caller and request")
	}
	expires, err := time.Parse(time.RFC3339, p.Data["expiresAt"])
	if err != nil || !now.Before(expires) || strings.TrimSpace(p.Data["revokedAt"]) != "" {
		return forbidden("portal admission bootstrap permit is expired or revoked")
	}
	return nil
}

func (s *Server) livePortalBootstrapCaller(ctx context.Context, c *caller) (*v1alpha1.AgentSession, error) {
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: c.session.Namespace, Name: c.session.Name}, &sess); err != nil || sess.UID != c.session.UID || !sess.DeletionTimestamp.IsZero() {
		return nil, forbidden("portal admission bootstrap caller session is no longer live")
	}
	var pod corev1.Pod
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: c.pod.Namespace, Name: c.pod.Name}, &pod); err != nil || pod.UID != c.pod.UID || !pod.DeletionTimestamp.IsZero() || pod.Spec.ServiceAccountName != c.identity.ServiceAccount {
		return nil, forbidden("portal admission bootstrap caller pod is no longer live")
	}
	owner := metav1.GetControllerOf(&pod)
	if owner == nil || owner.Kind != "AgentSession" || owner.UID != sess.UID {
		return nil, forbidden("portal admission bootstrap caller ownership changed")
	}
	return &sess, nil
}

func strictPortalBootstrapRequest(body io.Reader) (apiv1.PortalAdmissionBootstrapRequest, error) {
	b, err := io.ReadAll(io.LimitReader(body, 1025))
	if err != nil || len(b) > 1024 {
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
	return cm.Namespace == portalBootstrapTargetNamespace && cm.Name == portalBootstrapTargetName && cm.BinaryData == nil && cm.Immutable == nil && len(cm.OwnerReferences) == 0 && len(cm.Finalizers) == 0 && len(cm.Data) == 2 && cm.Data["schema"] == portalBootstrapSchema && cm.Data["state"] == "closed"
}
