package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const (
	// ProviderSourceDataKey is the single report entry in the fixed slot.
	ProviderSourceDataKey = "report.json"
	// ProviderSourceFieldManager is the update field manager for that entry.
	// It is not a GitOps manager and it does not own the slot's metadata.
	ProviderSourceFieldManager = "dev-env-provider-source"
	providerSourceMaxBody      = 16 * 1024
)

// Sentinel errors for one fixed current-source slot. Text is the code.
// None of them contain report bytes.
var (
	ErrProviderSourceAbsent      = errors.New("provider_source_absent")
	ErrProviderSourceConflict    = errors.New("provider_source_conflict")
	ErrProviderSourceRejected    = errors.New("provider_source_rejected")
	ErrProviderSourceDigest      = errors.New("provider_source_digest")
	ErrProviderSourceRetrieval   = errors.New("provider_source_retrieval")
	ErrProviderSourceUnavailable = errors.New("provider_source_unavailable")
)

// objectGetter is an uncached live read. The store never lists or watches.
type objectGetter interface {
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
}

// objectUpdater is the only write the slot performs. Create and delete are
// not part of the interface: a missing object stays missing.
type objectUpdater interface {
	Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
}

// ProviderSourceReportStore keeps one canonical current-source report in an
// existing ConfigMap. It reads through Live and updates that object at the
// resourceVersion it just read. It does not create, delete, or rebuild metadata.
type ProviderSourceReportStore struct {
	Writer    objectUpdater
	Live      objectGetter
	Namespace string
	Name      string
	DataKey   string
	Selection providerallowance.CurrentSourceSelection
}

// ProviderSourceView is a read of the slot. Raw is set only when the stored
// bytes are still the selected fresh report. A suppressed or empty view does
// not carry those bytes, and a read never writes them back.
type ProviderSourceView struct {
	Found           bool
	Fresh           bool
	Suppressed      bool
	Raw             []byte
	ResourceVersion string
}

func (s *ProviderSourceReportStore) configured() bool {
	return s.ready() && s.Selection.PublisherRef != "" && s.Selection.ProducerIncarnation != "" && s.Selection.SourceGeneration > 0
}

func (s *ProviderSourceReportStore) ready() bool {
	return s != nil && s.Writer != nil && s.Live != nil && s.Namespace != "" && s.Name != "" && s.DataKey != ""
}

// Read returns the stored report when it still validates for now. Expiry, a
// generation mismatch, and a digest mismatch suppress the value. They do not
// delete the object, clear data, or mint a retrieval time.
func (s *ProviderSourceReportStore) Read(ctx context.Context, now time.Time) (ProviderSourceView, error) {
	if !s.ready() {
		return ProviderSourceView{}, ErrProviderSourceUnavailable
	}
	cm, err := s.live(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ProviderSourceView{}, nil
		}
		return ProviderSourceView{}, fmt.Errorf("%w: %w", ErrProviderSourceUnavailable, err)
	}
	view := ProviderSourceView{ResourceVersion: cm.ResourceVersion}
	raw := slotBytes(cm, s.DataKey)
	if raw == "" {
		return view, nil
	}
	view.Found = true
	if !s.fresh(raw, now) {
		view.Suppressed = true
		return view, nil
	}
	view.Fresh = true
	view.Raw = append([]byte(nil), raw...)
	return view, nil
}

// Put stores one newer canonical report, or leaves the object untouched.
// An equal body is a replay and does not write. The same or an older
// retrieval, a digest mismatch, a generation the selection does not name,
// and a conflict all leave the stored bytes as they were.
func (s *ProviderSourceReportStore) Put(ctx context.Context, raw []byte, now time.Time) error {
	if s == nil {
		return ErrProviderSourceUnavailable
	}
	raw = append([]byte(nil), raw...)
	report, err := providerallowance.DecodeCurrentSource(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProviderSourceRejected, err)
	}
	if err := providerallowance.ValidateCurrentSource(report, s.Selection, now); err != nil {
		return fmt.Errorf("%w: %w", ErrProviderSourceRejected, err)
	}
	canonical, err := json.Marshal(report)
	if err != nil || !bytes.Equal(raw, canonical) {
		return ErrProviderSourceDigest
	}
	if !s.ready() {
		return ErrProviderSourceUnavailable
	}
	cm, err := s.live(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ErrProviderSourceAbsent
		}
		return fmt.Errorf("%w: %w", ErrProviderSourceUnavailable, err)
	}
	existing := slotBytes(cm, s.DataKey)
	if existing == string(raw) {
		return nil
	}
	if !cm.DeletionTimestamp.IsZero() || (cm.Immutable != nil && *cm.Immutable) {
		return ErrProviderSourceRejected
	}
	if cm.ResourceVersion == "" {
		return ErrProviderSourceUnavailable
	}
	if existing != "" {
		if err := supersede(existing, report); err != nil {
			return err
		}
	}
	priorRV := cm.ResourceVersion
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[s.DataKey] = string(raw)
	if err := s.Writer.Update(ctx, cm, client.FieldOwner(ProviderSourceFieldManager)); err != nil {
		if apierrors.IsConflict(err) {
			return ErrProviderSourceConflict
		}
		return s.confirm(ctx, string(raw), priorRV)
	}
	if slotBytes(cm, s.DataKey) != string(raw) {
		return s.confirm(ctx, string(raw), priorRV)
	}
	return nil
}

func (s *ProviderSourceReportStore) fresh(raw string, now time.Time) bool {
	report, err := providerallowance.DecodeCurrentSource([]byte(raw))
	if err != nil {
		return false
	}
	canonical, err := json.Marshal(report)
	if err != nil || !bytes.Equal([]byte(raw), canonical) {
		return false
	}
	return providerallowance.ValidateCurrentSource(report, s.Selection, now) == nil
}

func supersede(existing string, report providerallowance.CurrentSourceReport) error {
	prev, err := providerallowance.DecodeCurrentSource([]byte(existing))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProviderSourceRejected, err)
	}
	if prev.SourceGeneration != report.SourceGeneration {
		return nil
	}
	prevAt, prevErr := time.Parse(time.RFC3339Nano, prev.RetrievedAt)
	nextAt, nextErr := time.Parse(time.RFC3339Nano, report.RetrievedAt)
	if prevErr != nil || nextErr != nil || !nextAt.After(prevAt) {
		return ErrProviderSourceRetrieval
	}
	return nil
}

func (s *ProviderSourceReportStore) confirm(ctx context.Context, raw, priorRV string) error {
	cm, err := s.live(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProviderSourceUnavailable, err)
	}
	if cm.ResourceVersion == "" || cm.ResourceVersion == priorRV || slotBytes(cm, s.DataKey) != raw {
		return ErrProviderSourceUnavailable
	}
	return nil
}

func (s *ProviderSourceReportStore) live(ctx context.Context) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, &cm); err != nil {
		return nil, err
	}
	return &cm, nil
}

func slotBytes(cm *corev1.ConfigMap, key string) string {
	if cm == nil || cm.Data == nil {
		return ""
	}
	return cm.Data[key]
}
