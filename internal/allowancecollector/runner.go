package allowancecollector

import (
	"context"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	kubeuuid "k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/internal/usagecollector"
)

type Remote interface {
	HelperSHA256() string
	Run(context.Context, Host, usagecollector.RemoteRequest) ([]byte, error)
}

// Runner is installed only after explicit validated configuration. It runs on
// the existing elected operator leader; a collector failure leaves API service
// alive and the prior report untouched. No API/logger receives provider output.
type Runner struct {
	Config *Config
	Client client.Client
	Live   client.Reader
	Remote Remote
	Now    func() time.Time
}

func (*Runner) NeedLeaderElection() bool { return true }
func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
func (r *Runner) Start(ctx context.Context) error {
	if r == nil || r.Config == nil {
		return nil
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		_ = r.Step(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (r *Runner) Step(ctx context.Context) error {
	if r == nil || r.Config == nil {
		return nil
	}
	cfg, now := r.Config, r.now()
	if r.Client == nil || r.Live == nil || r.Remote == nil || !now.Before(cfg.until) || !remoteDigest(r.Remote.HelperSHA256()) || CheckHost(ctx, r.Live, cfg.Host, now) != nil {
		return ErrCollector
	}
	cm, err := r.slot(ctx)
	if err != nil {
		return err
	}
	report := []byte(cm.Data[apiserver.AggregateAllowanceDataKey])
	held, err := readJournal([]byte(cm.Data[StateKey]), report, cfg, now)
	if err != nil {
		return err
	}
	fresh := held.Phase == "idle"
	if fresh {
		if held.NotBefore != "" {
			next, e := time.Parse(time.RFC3339Nano, held.NotBefore)
			if e != nil {
				return ErrCollector
			}
			if now.Before(next) {
				return nil
			}
		}
		var intent []byte
		held, intent, err = reserve(held, cfg, string(kubeuuid.NewUUID()), now)
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[StateKey] = string(intent)
		if r.Client.Update(ctx, cm, client.FieldOwner("dev-env-allowance-collector")) != nil {
			confirmed, e := r.slot(ctx)
			if e != nil || confirmed.Data[StateKey] != string(intent) || confirmed.Data[apiserver.AggregateAllowanceDataKey] != string(report) {
				return ErrCollector
			}
			cm = confirmed
		}
	}
	request := r.request(held, fresh)
	operationCtx, cancel := context.WithTimeout(ctx, RemoteLimit)
	raw, remoteErr := r.Remote.Run(operationCtx, cfg.Host, request)
	cancel()
	// Recheck even on transport error; a replacement cannot be adopted while a
	// delayed remote process from the original pod might still be running.
	now = r.now()
	if CheckHost(ctx, r.Live, cfg.Host, now) != nil || !now.Before(cfg.until) || remoteErr != nil {
		return ErrCollector
	}
	receipt, err := usagecollector.ParseRemoteReceipt(raw, request)
	if err != nil {
		return ErrCollector
	}
	if receipt.State == "busy" || receipt.State == "uncertain" {
		return ErrCollector
	}
	return r.finish(ctx, cm, held, request, receipt, now)
}

func remoteDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

func (r *Runner) request(held journal, capture bool) usagecollector.RemoteRequest {
	cfg := r.Config
	request := usagecollector.RemoteRequest{Version: usagecollector.RemoteSchemaVersion, Operation: "status", RootName: cfg.Host.Name, SessionUID: cfg.Host.SessionUID, PodUID: cfg.Host.PodUID, Fingerprint: cfg.fingerprint, Cycle: held.Cycle, Nonce: held.Nonce, HelperSHA: r.Remote.HelperSHA256(), ValidUntil: cfg.ValidUntil, Providers: []usagecollector.ProviderBinding{}}
	if capture {
		request.Operation = "capture"
		for _, selection := range cfg.trust.Contexts {
			request.Providers = append(request.Providers, usagecollector.ProviderBinding{Provider: selection.Provider, AccountRef: *selection.AccountRef, WorkspaceRef: selection.WorkspaceRef, SourceVersion: selection.SourceVersion})
		}
	}
	return request
}

func (r *Runner) slot(ctx context.Context) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	if r.Live.Get(ctx, types.NamespacedName{Namespace: apiserver.AggregateAllowanceNamespace, Name: apiserver.AggregateAllowanceName}, &cm) != nil || string(cm.UID) != r.Config.SlotUID || !cm.DeletionTimestamp.IsZero() || cm.Immutable != nil && *cm.Immutable || cm.ResourceVersion == "" || len(cm.BinaryData[StateKey]) != 0 || len(cm.BinaryData[apiserver.AggregateAllowanceDataKey]) != 0 {
		return nil, ErrCollector
	}
	return &cm, nil
}

func (r *Runner) finish(ctx context.Context, cm *corev1.ConfigMap, held journal, request usagecollector.RemoteRequest, receipt usagecollector.RemoteReceipt, now time.Time) error {
	captures := append([]Capture{}, held.Captures...)
	attempts := []providerallowance.CaptureAttempt{}
	selections := map[string]providerallowance.TrustedContext{}
	for _, selection := range r.Config.trust.Contexts {
		selections[selection.Provider] = selection
	}
	started, _ := time.Parse(time.RFC3339Nano, held.StartedAt)
	allSuccess, finished := true, started
	switch receipt.State {
	case "never_started":
		allSuccess = false
		finished = now
		for _, selection := range r.Config.trust.Contexts {
			attempts = append(attempts, providerallowance.CaptureAttempt{Provider: selection.Provider, ProducerRef: selection.ProducerRef, ProducerIncarnation: selection.ProducerIncarnation, StartedAt: now, CompletedAt: now})
		}
	case "complete":
		seen := map[string]bool{}
		for _, attempt := range receipt.Attempts {
			selection, ok := selections[attempt.Provider]
			if !ok || seen[attempt.Provider] || attempt.StartedAt.Before(started) || attempt.CompletedAt.Before(attempt.StartedAt) || attempt.CompletedAt.After(now) || attempt.CompletedAt.Sub(attempt.StartedAt) > 2*time.Minute {
				return ErrCollector
			}
			seen[attempt.Provider] = true
			if attempt.CompletedAt.After(finished) {
				finished = attempt.CompletedAt
			}
			allSuccess = allSuccess && attempt.Success
			attempts = append(attempts, providerallowance.CaptureAttempt{Provider: attempt.Provider, ProducerRef: selection.ProducerRef, ProducerIncarnation: selection.ProducerIncarnation, StartedAt: attempt.StartedAt, CompletedAt: attempt.CompletedAt, Success: attempt.Success})
		}
		if len(seen) != 3 {
			return ErrCollector
		}
		seen = map[string]bool{}
		for i, capture := range receipt.Captures {
			selection, ok := selections[capture.Provider]
			if !ok || seen[capture.Provider] || i >= len(receipt.Bindings) {
				return ErrCollector
			}
			seen[capture.Provider] = true
			binding := receipt.Bindings[i]
			if binding.Provider != selection.Provider || binding.AccountRef != *selection.AccountRef || binding.SourceVersion != selection.SourceVersion || !sameString(binding.WorkspaceRef, selection.WorkspaceRef) {
				return ErrCollector
			}
			found := false
			for _, attempt := range attempts {
				if attempt.Provider == capture.Provider && attempt.Success {
					found = true
					received, e := time.Parse(time.RFC3339Nano, capture.ReceivedAt)
					if e != nil || received.Before(attempt.StartedAt) || received.After(attempt.CompletedAt) {
						return ErrCollector
					}
				}
			}
			if !found {
				return ErrCollector
			}
			captures = appendCapture(captures, Capture{Provider: capture.Provider, ProducerRef: selection.ProducerRef, ProducerIncarnation: selection.ProducerIncarnation, Sequence: held.Cycle, CapturedAt: capture.CapturedAt, ReceivedAt: capture.ReceivedAt, Payload: capture.Payload})
		}
		for _, attempt := range attempts {
			if attempt.Success && !seen[attempt.Provider] {
				return ErrCollector
			}
		}
	default:
		return ErrCollector
	}
	encoded, err := bundle(captures)
	if err != nil {
		return ErrCollector
	}
	doc, err := providerallowance.AssembleCaptureAttempts(encoded, r.Config.trust, attempts, now)
	if err != nil {
		return ErrCollector
	}
	providerallowance.ProjectCachedRead(&doc, r.Config.trust.Contexts, r.Config.trust.Registry, time.Duration(r.Config.trust.MaxAge)*time.Second, true, now)
	for _, item := range doc.Items {
		if item.Binding.State != providerallowance.BindingVerified || item.Validation.State != providerallowance.ValidationSourceFields {
			return ErrCollector
		}
	}
	report, err := json.Marshal(doc)
	if err != nil {
		return ErrCollector
	}
	next := held
	next.Phase = "idle"
	next.Captures = captures
	next.ReportSHA = digest(report)
	delay := r.Config.CadenceSeconds
	if !allSuccess {
		delay = r.Config.BackoffSeconds
	}
	next.NotBefore = finished.Add(time.Duration(delay) * time.Second).UTC().Format(time.RFC3339Nano)
	nextRaw, err := json.Marshal(next)
	if err != nil || len(nextRaw) > journalMaxBytes {
		return ErrCollector
	}
	expected, _ := json.Marshal(held)
	writer := &atomicWriter{Client: r.Client, slotUID: r.Config.SlotUID, resourceVersion: cm.ResourceVersion, expected: string(expected), next: string(nextRaw)}
	store := apiserver.AggregateAllowanceReportStore{Writer: writer, Live: r.Live, Namespace: apiserver.AggregateAllowanceNamespace, Name: apiserver.AggregateAllowanceName, DataKey: apiserver.AggregateAllowanceDataKey, Contexts: r.Config.trust.Contexts, Registry: r.Config.trust.Registry, MaxAge: time.Duration(r.Config.trust.MaxAge) * time.Second}
	if store.Put(ctx, report, now) != nil {
		return ErrCollector
	}
	confirmed, err := r.slot(ctx)
	if err != nil || confirmed.Data[StateKey] != string(nextRaw) || confirmed.Data[apiserver.AggregateAllowanceDataKey] != string(report) {
		return ErrCollector
	}
	return nil
}

func sameString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// atomicWriter adds the journal to the existing store's validated report CAS.
// The store retains its provenance, watermark, last-good and conflict checks.
type atomicWriter struct {
	client.Client
	slotUID, resourceVersion, expected, next string
}

func (w *atomicWriter) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok || cm.Namespace != apiserver.AggregateAllowanceNamespace || cm.Name != apiserver.AggregateAllowanceName || string(cm.UID) != w.slotUID || w.resourceVersion == "" || cm.ResourceVersion != w.resourceVersion || cm.Data[StateKey] != w.expected {
		return ErrCollector
	}
	cm.Data[StateKey] = w.next
	return w.Client.Update(ctx, cm, opts...)
}
