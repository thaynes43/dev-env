package apiserver

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// bindUsageCaptureParent copies the authenticated parent Session UID onto a new
// child when capture is enabled. A failed or replaced read leaves the field
// absent. The request body is not consulted.
func (s *Server) bindUsageCaptureParent(ctx context.Context, c *caller, sess *v1alpha1.AgentSession) {
	if !s.ChildUsageCapture || c == nil || c.kind != kindSession || c.session == nil || c.session.UID == "" || c.parent == "" || c.session.Name != c.parent {
		return
	}
	var parent v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: c.session.Namespace, Name: c.parent}, &parent); err != nil {
		return
	}
	if parent.UID != c.session.UID || !parent.DeletionTimestamp.IsZero() {
		return
	}
	sess.Spec.UsageCaptureParentUID = string(parent.UID)
}

// captureChildUsage copies one accepted initial observation onto the bound
// parent. It reads the child again after the usage write and copies only that
// stored observation. A parent conflict leaves the next identical heartbeat to
// retry. A usage conflict, a different pod, or an invalid observation copies
// nothing.
func (s *Server) captureChildUsage(ctx context.Context, child *v1alpha1.AgentSession, pod *corev1.Pod, incoming *protocol.UsageObservation) error {
	if !s.ChildUsageCapture || incoming == nil || pod == nil || child == nil || child.UID == "" || child.Spec.UsageCaptureParentUID == "" || child.Spec.Parent == "" {
		return nil
	}
	var fresh v1alpha1.AgentSession
	if err := s.Live.Get(ctx, client.ObjectKeyFromObject(child), &fresh); err != nil {
		return err
	}
	if fresh.UID != child.UID || fresh.Status.UsageConflict != nil {
		return nil
	}
	accepted := sessionUsage(&fresh)
	stored := fresh.Status.UsageObservation
	if accepted == nil || stored == nil || accepted.ID != incoming.ID || accepted.Source != incoming.Source || accepted.Source.PodUID != string(pod.UID) || accepted.Source.SessionUID != string(fresh.UID) {
		return nil
	}
	if h := fresh.Status.PrivateHistory; h != nil && (h.SessionUID != string(fresh.UID) || h.LastExecutor == nil || h.LastExecutor.PodUID != string(pod.UID)) {
		return nil
	}
	var parent v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: fresh.Namespace, Name: fresh.Spec.Parent}, &parent); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if string(parent.UID) != fresh.Spec.UsageCaptureParentUID || !parent.DeletionTimestamp.IsZero() {
		return nil
	}
	base := parent.DeepCopy()
	if !appendCapturedUsage(&parent, fresh.Name, stored, s.now()) {
		return nil
	}
	err := s.Client.Status().Patch(ctx, &parent, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}

func capturedUsageKey(parentUID, childUID, podUID, attemptID, observationID string) string {
	return apiv1.CapturedUsageKey(parentUID, childUID, podUID, attemptID, observationID)
}

// appendCapturedUsage updates parent status in memory. It reports whether a
// patch is needed. Replay and a second identity for the same child UID do not
// rewrite an existing row. A row is accepted only when the ledger still fits
// after reserving the overflow mark and the direct-v1 view.
func appendCapturedUsage(parent *v1alpha1.AgentSession, childName string, stored *v1alpha1.UsageObservationStatus, now time.Time) bool {
	if parent == nil || parent.UID == "" || stored == nil || stored.Source.SessionUID == "" || usageObservationView(stored) == nil {
		return false
	}
	stamp := metav1.NewTime(now.UTC().Truncate(time.Second))
	if stamp.IsZero() {
		return false
	}
	key := capturedUsageKey(string(parent.UID), stored.Source.SessionUID, stored.Source.PodUID, stored.Source.AttemptID, stored.ID)
	ledger := parent.Status.CapturedUsage
	if ledger == nil {
		ledger = newCapturedUsage()
	} else {
		ledger = ledger.DeepCopy()
	}
	if ledger.Overflow {
		return false
	}
	for i := range ledger.Receipts {
		row := &ledger.Receipts[i]
		if row.Observation.Source.SessionUID == stored.Source.SessionUID || row.Key == key {
			return false
		}
	}
	next := ledger.DeepCopy()
	next.Receipts = append(next.Receipts, v1alpha1.CapturedUsageReceipt{
		Key: key, ParentUID: string(parent.UID), ChildName: childName, CapturedAt: stamp, Observation: *stored.DeepCopy(),
	})
	reserved := next.DeepCopy()
	reserved.Overflow = true
	if len(next.Receipts) > v1alpha1.CapturedUsageMaxReceipts || !capturedLedgerAcceptable(parent, reserved) {
		marked := ledger.DeepCopy()
		marked.Overflow = true
		if !capturedLedgerAcceptable(parent, marked) {
			return false
		}
		parent.Status.CapturedUsage = marked
		return true
	}
	parent.Status.CapturedUsage = next
	return true
}

func newCapturedUsage() *v1alpha1.CapturedUsageStatus {
	return &v1alpha1.CapturedUsageStatus{Version: 1, Scope: v1alpha1.CapturedUsageScope, Coverage: v1alpha1.CapturedUsageCoverage, Retention: v1alpha1.CapturedUsageRetention}
}

func capturedUsageSize(ledger *v1alpha1.CapturedUsageStatus) int {
	raw, err := json.Marshal(ledger)
	if err != nil {
		return v1alpha1.CapturedUsageMaxBytes + 1
	}
	return len(raw)
}

func capturedLedgerAcceptable(parent *v1alpha1.AgentSession, ledger *v1alpha1.CapturedUsageStatus) bool {
	if parent == nil || ledger == nil {
		return false
	}
	saved := parent.Status.CapturedUsage
	parent.Status.CapturedUsage = ledger
	view := capturedUsageView(parent, &caller{kind: kindHuman}, true)
	parent.Status.CapturedUsage = saved
	return view != nil && view.Available && len(view.Receipts) == len(ledger.Receipts)
}

// captureViewAllowed reports whether this caller may see the receipt view.
// Humans and clients may. A session may see only its own ledger. A coordinator
// and any other session may not.
func captureViewAllowed(sess *v1alpha1.AgentSession, c *caller) bool {
	if sess == nil || c == nil {
		return false
	}
	if c.kind == kindHuman || c.kind == kindClient {
		return true
	}
	return c.kind == kindSession && c.session != nil && c.session.UID != "" && c.session.UID == sess.UID
}

func capturedUsageView(sess *v1alpha1.AgentSession, c *caller, enabled bool) *apiv1.CapturedUsage {
	closed := func(reason string) *apiv1.CapturedUsage {
		return &apiv1.CapturedUsage{Available: false, Reason: reason}
	}
	if !enabled {
		return closed("disabled")
	}
	if !captureViewAllowed(sess, c) {
		return closed("unavailable")
	}
	ledger := sess.Status.CapturedUsage
	if ledger == nil || (len(ledger.Receipts) == 0 && !ledger.Overflow) {
		return closed("unavailable")
	}
	if ledger.Version != 1 || ledger.Scope != v1alpha1.CapturedUsageScope || ledger.Coverage != v1alpha1.CapturedUsageCoverage || ledger.Retention != v1alpha1.CapturedUsageRetention {
		return closed("malformed")
	}
	if len(ledger.Receipts) > v1alpha1.CapturedUsageMaxReceipts || capturedUsageSize(ledger) > v1alpha1.CapturedUsageMaxBytes {
		return closed("oversized")
	}
	rows := make([]apiv1.CapturedUsageReceipt, 0, len(ledger.Receipts))
	for i := range ledger.Receipts {
		row := &ledger.Receipts[i]
		obs := usageObservationView(&row.Observation)
		if obs == nil || row.CapturedAt.IsZero() {
			return closed("malformed")
		}
		rows = append(rows, apiv1.CapturedUsageReceipt{
			Key: row.Key, ParentUID: row.ParentUID, ChildName: row.ChildName, CapturedAt: row.CapturedAt.UTC(), Observation: obs,
		})
	}
	slicesSort(rows)
	return apiv1.NormalizeCapturedUsage(string(sess.UID), &apiv1.CapturedUsage{
		Available: true, Scope: ledger.Scope, Coverage: ledger.Coverage, Retention: ledger.Retention, Overflow: ledger.Overflow, Receipts: rows,
	})
}

func slicesSort(rows []apiv1.CapturedUsageReceipt) {
	for i := 1; i < len(rows); i++ {
		j := i
		for j > 0 && strings.Compare(rows[j-1].Key, rows[j].Key) > 0 {
			rows[j-1], rows[j] = rows[j], rows[j-1]
			j--
		}
	}
}
