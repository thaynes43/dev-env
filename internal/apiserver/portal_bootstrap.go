package apiserver

// This file intentionally implements one fixed bootstrap action, not a generic
// object API. Production RBAC/admission/permit activation remains separate.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

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
	var req apiv1.PortalAdmissionBootstrapRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.Decode(new(any)) != io.EOF || len(validation.IsDNS1123Label(req.RequestID)) != 0 {
		return 0, nil, badRequest("portal admission bootstrap requires one valid requestID")
	}
	permitKey := types.NamespacedName{Namespace: portalBootstrapPermitNamespace, Name: portalBootstrapPermitName}
	var permit corev1.ConfigMap
	if err := s.Live.Get(ctx, permitKey, &permit); err != nil {
		return 0, nil, fromKubeError(err, "read fixed bootstrap permit")
	}
	if err := validPortalBootstrapPermit(&permit, req.RequestID, string(c.session.UID), string(c.pod.UID), s.now()); err != nil {
		return 0, nil, err
	}
	targetKey := types.NamespacedName{Namespace: portalBootstrapTargetNamespace, Name: portalBootstrapTargetName}
	var existing corev1.ConfigMap
	if err := s.Live.Get(ctx, targetKey, &existing); err == nil {
		if exactPortalBootstrapTarget(&existing) {
			return http.StatusOK, apiv1.PortalAdmissionBootstrapResult{Disposition: "Present", RequestID: req.RequestID, TargetUID: string(existing.UID), TargetRV: existing.ResourceVersion}, nil
		}
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "portal admission target already exists with unexpected content")
	} else if !apierrors.IsNotFound(err) {
		return 0, nil, fromKubeError(err, "read portal admission target")
	}
	// Consume first, via resourceVersion CAS. Any error after this update is
	// deliberately terminal: there is no automatic replay or second create.
	permit.Data["state"] = "consumed"
	permit.Data["attemptedAt"] = s.now().UTC().Format(time.RFC3339Nano)
	if err := s.Client.Update(ctx, &permit); err != nil {
		return 0, nil, fromKubeError(err, "consume fixed bootstrap permit")
	}
	// Re-read after consumption: a concurrent producer may have made the fixed
	// witness while this request was reserving its one attempt.
	err := s.Live.Get(ctx, targetKey, &existing)
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
	return http.StatusCreated, apiv1.PortalAdmissionBootstrapResult{Disposition: "Created", RequestID: req.RequestID, TargetUID: string(target.UID), TargetRV: target.ResourceVersion}, nil
}

func validPortalBootstrapPermit(p *corev1.ConfigMap, requestID, sessionUID, podUID string, now time.Time) error {
	if p.UID == "" || p.Data == nil || p.Data["state"] != "active" || p.Data["requestID"] != requestID || p.Data["sessionUID"] != sessionUID || p.Data["podUID"] != podUID || p.Data["payloadDigest"] != portalBootstrapDigest() {
		return forbidden("portal admission bootstrap permit does not match this fixed caller and request")
	}
	expires, err := time.Parse(time.RFC3339, p.Data["expiresAt"])
	if err != nil || !now.Before(expires) || strings.TrimSpace(p.Data["revokedAt"]) != "" {
		return forbidden("portal admission bootstrap permit is expired or revoked")
	}
	return nil
}

func exactPortalBootstrapTarget(cm *corev1.ConfigMap) bool {
	return cm.Namespace == portalBootstrapTargetNamespace && cm.Name == portalBootstrapTargetName && cm.BinaryData == nil && cm.Immutable == nil && len(cm.OwnerReferences) == 0 && len(cm.Finalizers) == 0 && len(cm.Data) == 2 && cm.Data["schema"] == portalBootstrapSchema && cm.Data["state"] == "closed"
}
