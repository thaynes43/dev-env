package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const usageCaptureVersion = "1"

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
// parent. It does not change the child write that already finished, and a
// parent conflict leaves the next heartbeat to replay.
func (s *Server) captureChildUsage(ctx context.Context, child *v1alpha1.AgentSession, incoming *protocol.UsageObservation) error {
	if !s.ChildUsageCapture || incoming == nil || child == nil || child.UID == "" || child.Spec.UsageCaptureParentUID == "" || child.Spec.Parent == "" {
		return nil
	}
	var fresh v1alpha1.AgentSession
	if err := s.Live.Get(ctx, client.ObjectKeyFromObject(child), &fresh); err != nil {
		return err
	}
	stored := fresh.Status.UsageObservation
	if fresh.UID != child.UID || stored == nil || stored.ID != incoming.ID || stored.Source.AttemptID != incoming.Source.AttemptID || stored.Source.PodUID != incoming.Source.PodUID || stored.Source.SessionUID != string(fresh.UID) {
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
	sum := sha256.Sum256([]byte(strings.Join([]string{usageCaptureVersion, parentUID, childUID, podUID, attemptID, observationID}, "\n")))
	return hex.EncodeToString(sum[:])
}

// appendCapturedUsage updates parent status in memory. It reports whether a
// patch is needed. Replay and a second identity for the same child UID do not
// rewrite an existing row.
func appendCapturedUsage(parent *v1alpha1.AgentSession, childName string, stored *v1alpha1.UsageObservationStatus, now time.Time) bool {
	if parent == nil || stored == nil || stored.Source.SessionUID == "" {
		return false
	}
	stamp := metav1.NewTime(now.UTC().Truncate(time.Second))
	key := capturedUsageKey(string(parent.UID), stored.Source.SessionUID, stored.Source.PodUID, stored.Source.AttemptID, stored.ID)
	ledger := parent.Status.CapturedUsage
	if ledger == nil {
		ledger = newCapturedUsage()
	} else {
		ledger = ledger.DeepCopy()
	}
	for i := range ledger.Receipts {
		row := &ledger.Receipts[i]
		if row.Observation.Source.SessionUID == stored.Source.SessionUID {
			return false
		}
		if row.Key == key {
			return false
		}
	}
	next := ledger.DeepCopy()
	next.Receipts = append(next.Receipts, v1alpha1.CapturedUsageReceipt{
		Key: key, ParentUID: string(parent.UID), ChildName: childName, CapturedAt: stamp, Observation: *stored.DeepCopy(),
	})
	if len(next.Receipts) > v1alpha1.CapturedUsageMaxReceipts || capturedUsageSize(next) > v1alpha1.CapturedUsageMaxBytes {
		if ledger.Overflow {
			return false
		}
		ledger.Overflow = true
		parent.Status.CapturedUsage = ledger
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

func capturedUsageView(sess *v1alpha1.AgentSession, c *caller, enabled bool) *apiv1.CapturedUsage {
	if !enabled {
		return &apiv1.CapturedUsage{Available: false, Reason: "disabled"}
	}
	if c == nil || (c.kind != kindHuman && c.kind != kindClient && !(c.kind == kindSession && c.session != nil && c.session.UID == sess.UID)) {
		return &apiv1.CapturedUsage{Available: false, Reason: "unavailable"}
	}
	ledger := sess.Status.CapturedUsage
	if ledger == nil {
		return &apiv1.CapturedUsage{Available: true, Scope: v1alpha1.CapturedUsageScope, Coverage: v1alpha1.CapturedUsageCoverage, Retention: v1alpha1.CapturedUsageRetention}
	}
	if !validCapturedLedger(sess, ledger) {
		reason := "malformed"
		if len(ledger.Receipts) > v1alpha1.CapturedUsageMaxReceipts || capturedUsageSize(ledger) > v1alpha1.CapturedUsageMaxBytes {
			reason = "oversized"
		}
		return &apiv1.CapturedUsage{Available: false, Reason: reason}
	}
	rows := make([]apiv1.CapturedUsageReceipt, 0, len(ledger.Receipts))
	for i := range ledger.Receipts {
		row := &ledger.Receipts[i]
		obs := usageObservationView(&row.Observation)
		if obs == nil {
			return &apiv1.CapturedUsage{Available: false, Reason: "malformed"}
		}
		rows = append(rows, apiv1.CapturedUsageReceipt{Key: row.Key, ParentUID: row.ParentUID, ChildName: row.ChildName, CapturedAt: row.CapturedAt.UTC(), Observation: obs})
	}
	slices.SortFunc(rows, func(a, b apiv1.CapturedUsageReceipt) int { return strings.Compare(a.Key, b.Key) })
	return &apiv1.CapturedUsage{Available: true, Scope: ledger.Scope, Coverage: ledger.Coverage, Retention: ledger.Retention, Overflow: ledger.Overflow, Receipts: rows}
}

func validCapturedLedger(sess *v1alpha1.AgentSession, ledger *v1alpha1.CapturedUsageStatus) bool {
	if ledger.Version != 1 || ledger.Scope != v1alpha1.CapturedUsageScope || ledger.Coverage != v1alpha1.CapturedUsageCoverage || ledger.Retention != v1alpha1.CapturedUsageRetention {
		return false
	}
	if len(ledger.Receipts) > v1alpha1.CapturedUsageMaxReceipts || capturedUsageSize(ledger) > v1alpha1.CapturedUsageMaxBytes {
		return false
	}
	seen := map[string]bool{}
	children := map[string]bool{}
	for i := range ledger.Receipts {
		row := &ledger.Receipts[i]
		if row.ParentUID != string(sess.UID) || seen[row.Key] || children[row.Observation.Source.SessionUID] {
			return false
		}
		want := capturedUsageKey(row.ParentUID, row.Observation.Source.SessionUID, row.Observation.Source.PodUID, row.Observation.Source.AttemptID, row.Observation.ID)
		if row.Key != want || usageObservationView(&row.Observation) == nil {
			return false
		}
		seen[row.Key] = true
		children[row.Observation.Source.SessionUID] = true
	}
	return true
}
