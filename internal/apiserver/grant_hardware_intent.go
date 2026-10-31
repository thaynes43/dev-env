package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

const hardwareIntentAnnotation = v1alpha1.LabelPrefix + "hardware-intent"

// recordHardwareIntent records audit intent on an already installed own C4
// lease. classes is explicit default-empty configuration, never body input.
// This path cannot approve, issue, extend or use credentials.
func (s *Server) recordHardwareIntent(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller, classes []string) (int, any, error) {
	if !slices.Contains(classes, "rd4-hardware") {
		return 0, nil, forbidden("RD4hardware audit intent is disabled (--reserved-decision-classes)")
	}
	if c.kind != kindSession || c.session == nil || c.pod == nil || c.identity.PodUID == "" {
		return 0, nil, forbidden("hardware intent is recorded only by the authenticated requester on its own C4 lease")
	}
	var req apiv1.HardwareIntentRequest
	if err := decodeJSON(w, r, 4096, &req, true); err != nil {
		return 0, nil, err
	}
	if err := apiv1.ValidateHardwareIntentRequest(req); err != nil {
		return 0, nil, invalid(fieldError("hardwareIntent", "%s", err))
	}
	key, err := s.grantKey(r)
	if err != nil {
		return 0, nil, err
	}
	var g v1alpha1.AccessGrant
	if err := s.Live.Get(ctx, key, &g); err != nil {
		return 0, nil, fromGrantKubeError(err, "read hardware lease")
	}
	if !grantRequestedBy(&g, c.parent, c.session.UID) || g.Spec.Type != v1alpha1.GrantCredential || g.Spec.Credential == nil ||
		(g.Spec.Credential.Name != v1alpha1.CredentialHWSSH && g.Spec.Credential.Name != v1alpha1.CredentialProxmox) {
		return 0, nil, forbidden("hardware intent requires the caller's own C4 credential lease")
	}
	now := s.now()
	if string(g.UID) != req.GrantUID || grantattest.ScopeDigest(&g) != req.Digest || !g.DeletionTimestamp.IsZero() || g.Spec.Release ||
		g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedAt == nil || g.Status.ApprovedAt.After(now) ||
		g.Status.ExpiresAt == nil || !now.Before(g.Status.ExpiresAt.Time) || !strings.HasPrefix(g.Status.ApprovedBy, "policy/") ||
		g.Status.InstalledPodUID != c.identity.PodUID || g.Status.InstalledAt == nil || g.Status.InstalledAt.Before(g.Status.ApprovedAt) ||
		g.Status.InstalledAt.After(now) || g.Annotations[hardwareIntentAnnotation] != "" {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "stale, uninstalled, ended, replaced or already recorded hardware intent; read the lease")
	}
	// Validate the original caller binding live. TokenReview authenticated the
	// bearer, while these reads refuse a replaced, ended or reaped requester.
	var session v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: c.parent}, &session); err != nil {
		return 0, nil, fromKubeError(err, "read hardware intent requester")
	}
	var pod corev1.Pod
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: c.identity.Namespace, Name: c.identity.PodName}, &pod); err != nil {
		return 0, nil, fromKubeError(err, "read hardware intent pod")
	}
	if session.UID != c.session.UID || !session.DeletionTimestamp.IsZero() || session.Status.Phase != v1alpha1.PhaseRunning ||
		session.Status.ArchivedAt != nil || (session.Spec.OperatingMode != "" && session.Spec.OperatingMode != v1alpha1.OperatingModeRunning) ||
		string(pod.UID) != c.identity.PodUID || pod.Spec.ServiceAccountName != c.identity.ServiceAccount || pod.Status.Phase != corev1.PodRunning ||
		!pod.DeletionTimestamp.IsZero() || !metav1.IsControlledBy(&pod, &session) {
		return 0, nil, forbidden("hardware intent requester or pod is replaced or inactive")
	}
	lease := &broker.Broker{APIReader: s.Live, SessionNamespace: s.Policy.SessionNamespace, PolicyNamespace: s.PolicyNamespace}
	if err := lease.CheckHardwareIntentLease(ctx, &g, c.identity.PodUID, now); err != nil {
		return 0, nil, forbidden("hardware intent lease validation: %s", err)
	}
	// The pure helper checks audit semantics using the authenticated session
	// owner UID; the live projected Pod and keeper receipt were checked above.
	if _, err := grantattest.EvaluateReservedHardware(grantattest.HardwareInput{
		Classes: classes, Actor: grantattest.Actor{UID: string(session.UID), Session: session.Name, SessionUID: string(session.UID)},
		Lease:  grantattest.LeaseView{Class: grantattest.LeaseClassC4, UID: string(g.UID), OwnerUID: string(g.Spec.Requester.SessionUID), Phase: string(g.Status.Phase), ExpiresAt: g.Status.ExpiresAt.Time.UTC().Truncate(time.Second)},
		Intent: grantattest.HardwareIntent{Operation: req.Operation, Host: req.Host, Device: req.Device, Firmware: req.Firmware, TomRef: req.TomRef},
		At:     now.UTC().Truncate(time.Second), Now: now.UTC().Truncate(time.Second), BoundsRevision: grantattest.BoundsRevision, RequesterUID: string(session.UID),
	}); err != nil {
		return 0, nil, invalid(fieldError("hardwareIntent", "%s", err))
	}
	receipt := apiv1.HardwareIntentReceipt{HardwareIntentRequest: req, SessionUID: string(session.UID), PodUID: string(pod.UID), RecordedAt: now.UTC(),
		Notice: "Audit intent only; agent assertion, not verified human consent. Credential authority and keeper policy approval are unchanged; no command or target enforcement."}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return 0, nil, err
	}
	orig := g.DeepCopy()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[hardwareIntentAnnotation] = string(raw)
	if err := s.Client.Patch(ctx, &g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsConflict(err) {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "hardware lease changed while intent was recorded; read the lease")
		}
		return 0, nil, fromGrantKubeError(err, "record hardware audit intent")
	}
	s.Log.Info("hardware audit intent recorded (agent assertion, not verified human consent)", "grant", g.Name, "grantUID", g.UID, "caller", c.String(), "tomRef", req.TomRef)
	return http.StatusCreated, receipt, nil
}

func (s *Server) hardwareIntent(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	return s.recordHardwareIntent(ctx, w, r, c, s.ReservedDecisionClasses)
}
