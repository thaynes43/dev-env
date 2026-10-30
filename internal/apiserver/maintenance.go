package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
)

func maintenanceSpec(m *apiv1.MaintenanceTask) *v1alpha1.MaintenanceTask {
	raw, _ := json.Marshal(m)
	var out v1alpha1.MaintenanceTask
	_ = json.Unmarshal(raw, &out)
	return &out
}
func maintenanceView(m *v1alpha1.MaintenanceTask) *apiv1.MaintenanceTask {
	raw, _ := json.Marshal(m)
	var out apiv1.MaintenanceTask
	_ = json.Unmarshal(raw, &out)
	return &out
}

// decideMaintenance is a thin OP219 adapter from existing TokenReview human auth
// to the broker's Decide. It neither enables Asks nor accepts a claimed approver.
func (s *Server) decideMaintenance(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindHuman {
		return 0, nil, forbidden("maintenance decisions require the configured authenticated human identity")
	}
	var req apiv1.MaintenanceDecisionRequest
	if err := decodeJSON(w, r, 4096, &req, true); err != nil {
		return 0, nil, err
	}
	key, err := s.grantKey(r)
	if err != nil {
		return 0, nil, err
	}
	var g v1alpha1.AccessGrant
	if err := s.Live.Get(ctx, key, &g); err != nil {
		return 0, nil, fromGrantKubeError(err, "read maintenance grant")
	}
	if err := broker.ValidateMaintenance(&g); err != nil {
		return 0, nil, forbidden("%s", err)
	}
	if req.GrantUID != string(g.UID) || req.Digest != broker.MaintenanceDigest(&g) || !g.DeletionTimestamp.IsZero() || g.Spec.Release || (g.Status.Phase != "" && g.Status.Phase != v1alpha1.GrantPending) || !s.now().Before(g.CreationTimestamp.Add(broker.PendingTimeout)) || g.Annotations[broker.MaintenanceDecisionAnnotation] != "" {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "stale, replayed or already decided maintenance request")
	}
	ttl := g.Spec.TTL.Duration
	if req.TTL != "" {
		ttl, err = time.ParseDuration(req.TTL)
		if err != nil || ttl < broker.MinTTL || ttl > g.Spec.TTL.Duration {
			return 0, nil, invalid(fieldError("ttl", "approve only the requested TTL or less, at least 10m"))
		}
	}
	if req.Approve {
		if err := broker.CheckMaintenanceIdentities(ctx, s.Live, s.Policy.SessionNamespace, &g); err != nil {
			return 0, nil, forbidden("%s", err)
		}
	}
	d := broker.MaintenanceDecision{GrantUID: string(g.UID), Digest: req.Digest, By: c.String(), Approve: req.Approve, TTL: ttl, At: s.now().UTC()}
	raw, err := json.Marshal(d)
	if err != nil {
		return 0, nil, err
	}
	orig := g.DeepCopy()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[broker.MaintenanceDecisionAnnotation] = string(raw)
	if err := s.Client.Patch(ctx, &g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return 0, nil, fromGrantKubeError(err, "record bound maintenance decision")
	}
	return http.StatusAccepted, s.grantView(&g), nil
}

// boundedCommandOutput drains output without unbounded allocation or logging it.
type boundedCommandOutput struct{ text strings.Builder }

func (b *boundedCommandOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := 64*1024 - b.text.Len()
	if left > 0 {
		_, _ = b.text.Write(p[:min(left, n)])
	}
	return n, nil
}

func (s *Server) execMaintenance(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindSession || c.session == nil || c.pod == nil {
		return 0, nil, forbidden("maintenance execution requires an authenticated session pod")
	}
	key, err := s.grantKey(r)
	if err != nil {
		return 0, nil, err
	}
	var req apiv1.MaintenanceCommand
	if err := decodeJSON(w, r, maxGrantBody, &req, true); err != nil {
		return 0, nil, err
	}
	if len(req.Command) == 0 || len(req.Command) > 128 || len(req.RestoreEvidence) > 900 {
		return 0, nil, invalid(fieldError("command", "requires 1 to128 arguments and bounded evidence"))
	}
	for _, arg := range req.Command {
		if strings.ContainsRune(arg, 0) {
			return 0, nil, invalid(fieldError("command", "NUL is forbidden"))
		}
	}
	var g v1alpha1.AccessGrant
	if err := s.Live.Get(ctx, key, &g); err != nil {
		return 0, nil, fromGrantKubeError(err, "read maintenance grant")
	}
	if err := broker.MaintenanceActor(&g, c.session.Name, string(c.session.UID), string(c.pod.UID), s.now()); err != nil {
		return 0, nil, forbidden("%s", err)
	}
	if err := broker.CheckMaintenanceIdentities(ctx, s.Live, s.Policy.SessionNamespace, &g); err != nil {
		return 0, nil, forbidden("%s", err)
	}
	if g.Status.Maintenance == nil || g.Status.Maintenance.Executor != "Running" || g.Status.Maintenance.PodUID == "" || s.Exec == nil {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "maintenance executor is unavailable")
	}
	var p corev1.Pod
	if err := s.Live.Get(ctx, key, &p); err != nil {
		return 0, nil, fromGrantKubeError(err, "read maintenance executor")
	}
	owner := metav1.GetControllerOf(&p)
	if string(p.UID) != g.Status.Maintenance.PodUID || p.Labels[broker.MaintenanceLabel] != string(g.UID) || !p.DeletionTimestamp.IsZero() || p.Status.Phase != corev1.PodRunning || p.Spec.NodeName != g.Spec.Maintenance.Node || p.Spec.ServiceAccountName != "dev-env-maintenance" || owner == nil || owner.UID != g.UID || owner.Kind != "AccessGrant" || owner.APIVersion != v1alpha1.GroupVersion.String() {
		return 0, nil, forbidden("executor binding changed")
	}
	if req.RestoreEvidence != "" && g.Spec.Maintenance.Restore == "" {
		return 0, nil, forbidden("restore evidence requires restore/readback in the approved task")
	}
	originalUID, originalDigest := g.UID, broker.MaintenanceDigest(&g)
	// A second fresh grant read narrows release races before opening the stream.
	if err := s.Live.Get(ctx, key, &g); err != nil {
		return 0, nil, fromGrantKubeError(err, "recheck maintenance lease")
	}
	if err := broker.MaintenanceActor(&g, c.session.Name, string(c.session.UID), string(c.pod.UID), s.now()); err != nil {
		return 0, nil, forbidden("%s", err)
	}
	if g.UID != originalUID || broker.MaintenanceDigest(&g) != originalDigest || g.Status.Maintenance == nil || g.Status.Maintenance.Executor != "Running" || g.Status.Maintenance.PodUID != string(p.UID) {
		return 0, nil, forbidden("maintenance grant or executor binding changed before execution")
	}
	end, cancel := context.WithDeadline(ctx, g.Status.ExpiresAt.Time)
	defer cancel()
	var stdout, stderr boundedCommandOutput
	err = s.Exec.Run(end, p.Namespace, p.Name, "maintenance", req.Command, nil, &stdout, &stderr)
	result := apiv1.MaintenanceCommandResult{Stdout: stdout.text.String(), Stderr: stderr.text.String()}
	if err != nil {
		result.Error = err.Error()
	}
	s.Log.Info("maintenance command finished", "grant", g.Name, "grantUID", g.UID, "actor", c.String(), "argc", len(req.Command), "succeeded", err == nil)
	if req.RestoreEvidence != "" {
		if g.Spec.Maintenance.Restore == "" {
			return 0, nil, forbidden("restore evidence requires restore/readback in the approved task")
		}
		// Preserve failures distinctly. This is an attributed report, not proof of rollback.
		if readErr := s.Live.Get(ctx, key, &g); readErr != nil {
			return 0, nil, fromGrantKubeError(readErr, "read restore report target")
		}
		orig := g.DeepCopy()
		if g.Annotations == nil {
			g.Annotations = map[string]string{}
		}
		g.Annotations[broker.MaintenanceRestoreAnnotation] = fmt.Sprintf("actor=%s success=%t report=%s", c.String(), err == nil, req.RestoreEvidence)
		if patchErr := s.Client.Patch(ctx, &g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); patchErr != nil {
			return 0, nil, fromGrantKubeError(patchErr, "record restore report")
		}
	}
	return http.StatusOK, result, nil
}
