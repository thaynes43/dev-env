package taskbudget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	LedgerLabel    = "dev-env.haynesops.com/task-budget"
	LedgerKey      = "ledger.json"
	MaxLedgerBytes = 768 << 10
)

// KubeStore must use a dedicated protected namespace and an uncached reader.
// Kubernetes RBAC cannot restrict dynamically named objects by name prefix.
// Only the operator writes here; clients/agents must have no create/update/delete
// permission. GitOps integration must establish that boundary before activation.
type KubeStore struct {
	Client    client.Client
	Live      client.Reader
	Namespace string
}

func ledgerName(uid string) string {
	h := sha256.Sum256([]byte(uid))
	return "task-budget-" + hex.EncodeToString(h[:20])
}

func encodeLedger(l *Ledger) ([]byte, error) {
	if l.Version != 1 || !validBinding(l.Binding) || !validSpec(l.Spec) || l.Parent == "" ||
		len(l.Events) > MaxEvents || len(l.Workers) > MaxWorkers || len(l.Extensions) > 32 || len(l.Escalations) > 32 ||
		l.CreatedAt.IsZero() || l.AccountedAt.Before(l.CreatedAt) || l.LastProgress.Before(l.CreatedAt) ||
		l.LastProgress.After(l.AccountedAt) || l.StallAnchor.Before(l.CreatedAt) || l.StallAnchor.After(l.AccountedAt) ||
		l.NextCheckpoint.IsZero() || l.EffortMilliseconds < 0 || l.EffortCeilingMilliseconds <= 0 ||
		l.CheckpointSeconds <= 0 || l.CheckpointSeconds > 86400 ||
		l.Latched && (l.Escalation == nil || l.Escalation.ID == "" || l.Reason == "") {
		return nil, ErrUnavailable
	}
	for _, e := range l.Events {
		if !validEvent(e.Request) || !validBinding(e.Request.Binding) || e.Request.Binding.TaskUID != l.Binding.TaskUID || e.At.Before(l.CreatedAt) || e.At.After(l.AccountedAt) {
			return nil, ErrUnavailable
		}
	}
	data, err := json.Marshal(l)
	if err != nil || len(data) > MaxLedgerBytes {
		return nil, ErrUnavailable
	}
	return data, nil
}

func (s KubeStore) ready() bool { return s.Client != nil && s.Live != nil && s.Namespace != "" }

func (s KubeStore) Create(ctx context.Context, l *Ledger) error {
	if !s.ready() {
		return ErrUnavailable
	}
	data, err := encodeLedger(l)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: ledgerName(l.Binding.TaskUID),
		Labels: map[string]string{LedgerLabel: "retained-v1"}}, Data: map[string]string{LedgerKey: string(data)}}
	if err := s.Client.Create(ctx, cm); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ErrConflict
		}
		return fmt.Errorf("%w: ledger create", ErrUnavailable)
	}
	return s.confirm(ctx, l, string(data))
}

func (s KubeStore) Read(ctx context.Context, uid string) (*Ledger, string, error) {
	if !s.ready() || !identifier.MatchString(uid) {
		return nil, "", ErrUnavailable
	}
	var cm corev1.ConfigMap
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: ledgerName(uid)}, &cm); err != nil {
		return nil, "", ErrUnavailable
	}
	if cm.Labels[LedgerLabel] != "retained-v1" || len(cm.OwnerReferences) != 0 || !cm.DeletionTimestamp.IsZero() || cm.ResourceVersion == "" || cm.Immutable != nil && *cm.Immutable {
		return nil, "", ErrUnavailable
	}
	data := cm.Data[LedgerKey]
	if len(data) == 0 || len(data) > MaxLedgerBytes {
		return nil, "", ErrUnavailable
	}
	var l Ledger
	if json.Unmarshal([]byte(data), &l) != nil || l.Binding.TaskUID != uid {
		return nil, "", ErrUnavailable
	}
	if _, err := encodeLedger(&l); err != nil {
		return nil, "", err
	}
	return &l, cm.ResourceVersion, nil
}

func (s KubeStore) Update(ctx context.Context, l *Ledger, rv string) error {
	if !s.ready() || rv == "" {
		return ErrUnavailable
	}
	data, err := encodeLedger(l)
	if err != nil {
		return err
	}
	// Update carries the exact resourceVersion read from Live. It preserves no
	// foreign object fields: Read rejects ownerrefs, immutable or dying ledgers.
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: ledgerName(l.Binding.TaskUID), ResourceVersion: rv,
		Labels: map[string]string{LedgerLabel: "retained-v1"}}, Data: map[string]string{LedgerKey: string(data)}}
	if err := s.Client.Update(ctx, cm); err != nil {
		if apierrors.IsConflict(err) {
			return ErrConflict
		}
		return fmt.Errorf("%w: ledger update result unknown", ErrUnavailable)
	}
	return s.confirm(ctx, l, string(data))
}

func (s KubeStore) confirm(ctx context.Context, l *Ledger, data string) error {
	var cm corev1.ConfigMap
	if s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: ledgerName(l.Binding.TaskUID)}, &cm) != nil ||
		cm.Data[LedgerKey] != data || cm.Labels[LedgerLabel] != "retained-v1" || len(cm.OwnerReferences) != 0 || !cm.DeletionTimestamp.IsZero() || cm.ResourceVersion == "" || cm.Immutable != nil && *cm.Immutable {
		return ErrUnavailable
	}
	return nil
}
