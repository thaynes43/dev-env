package allowancecollector

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	uc "github.com/thaynes43/dev-env/internal/usagecollector"
)

const hostUID = "11111111-1111-1111-1111-111111111111"
const podUID = "22222222-2222-2222-2222-222222222222"
const slotUID = "33333333-3333-3333-3333-333333333333"

func collectorConfig(t *testing.T, now time.Time) *Config {
	t.Helper()
	parts := pa.RegistryParts{}
	trust := pa.AggregateTrust{MaxAge: 1800}
	min, max := "0", "100"
	for _, provider := range []string{"openai", "anthropic", "cursor"} {
		account := provider + "-email-sha256:" + strings.Repeat("a", 64)
		ctx := pa.TrustedContext{Provider: provider, ProducerRef: "collector-" + provider, ProducerIncarnation: "operator-v1", BindingGeneration: 1, AccountRef: &account, BindingState: pa.BindingVerified, SourceKind: pa.SourceNativeRPC}
		capability := ""
		switch provider {
		case "openai":
			ctx.SourceVersion, capability = pa.CodexRPCSourceVersion, pa.CodexRPCCapability
		case "anthropic":
			ctx.SourceVersion, capability = pa.ClaudeSDKSourceVersion, "claude-sdk:usage-report"
			workspace := "anthropic-org-sha256:" + strings.Repeat("b", 64)
			ctx.WorkspaceRef = &workspace
		case "cursor":
			ctx.SourceKind, ctx.SourceVersion, capability = pa.SourceNativeDisplay, pa.CursorCLISourceVersion, pa.CursorCLICapability
		}
		fields := map[string]pa.DirectFieldRule{}
		if provider == "cursor" {
			for _, name := range []string{"plan", "accountLabel", "billingResetDisplayText", "rendererVersion", "percentPrecision", "captureTimeBasis", "period", "includedPercentDisplay"} {
				fields[name] = pa.DirectFieldRule{OriginalType: pa.TypeString, Class: pa.ClassDisplay}
			}
		}
		if provider == "anthropic" {
			for _, name := range []string{"kind", "group", "severity"} {
				fields["claude-sdk-0-"+name] = pa.DirectFieldRule{OriginalType: pa.TypeString, Class: pa.ClassDisplay}
			}
			fields["claude-sdk-0-active"] = pa.DirectFieldRule{OriginalType: pa.TypeBoolean, Class: pa.ClassDisplay}
		}
		parts.Sources = append(parts.Sources, pa.SourceRule{Provider: provider, Kind: ctx.SourceKind, Version: ctx.SourceVersion, CapabilityRef: capability, DirectFields: fields, AllowNormalizedMeasures: true, AllowDerivations: true})
		workspace := ""
		if ctx.WorkspaceRef != nil {
			workspace = *ctx.WorkspaceRef
		}
		parts.Accounts = append(parts.Accounts, pa.AccountRule{AccountRef: account, WorkspaceRef: workspace, Generation: 1, ProducerRef: ctx.ProducerRef, ProducerIncarnation: ctx.ProducerIncarnation, AccountScopeOnly: ctx.WorkspaceRef == nil})
		metrics := map[string]string{}
		switch provider {
		case "openai":
			metrics = map[string]string{"codex-app-server:primary-used-percent": "codex-primary", "codex-app-server:secondary-used-percent": "codex-secondary"}
		case "anthropic":
			metrics = map[string]string{"claude-sdk:claude-sdk-0-used-percent": "claude-sdk-0-weekly_all"}
		case "cursor":
			metrics = map[string]string{pa.CursorDisplayIncludedValidation: pa.CursorDisplayIncludedWindow}
		}
		for ref, window := range metrics {
			parts.Metrics = append(parts.Metrics, pa.MetricRule{ValidationRef: ref, Provider: provider, SourceKind: ctx.SourceKind, SourceVersion: ctx.SourceVersion, Category: pa.CategoryIncluded, Unit: pa.UnitPercent, Direction: pa.DirectionUsed, Windows: []string{window}, Min: &min, Max: &max, ComplementOf: "100", HistoricalOnly: true})
		}
		trust.Contexts = append(trust.Contexts, ctx)
	}
	trust.Registry = pa.NewStaticRegistry(parts)
	raw, _ := json.Marshal(Config{Version: 1, Host: Host{Name: "fixture-owner", SessionUID: hostUID, Generation: 1, PodUID: podUID, ServiceAccount: "dev-env-workbench", Image: "ghcr.io/thaynes43/dev-env:2.45.0@sha256:" + strings.Repeat("a", 64)}, SlotUID: slotUID, ValidUntil: now.Add(time.Hour).UTC().Format(time.RFC3339Nano), CadenceSeconds: 300, BackoffSeconds: 600})
	cfg, err := ParseConfig(raw, trust, hostUID, "dev-env-workbench", now)
	if err != nil {
		t.Fatal("valid config refused", err)
	}
	return cfg
}

type fixtureRemote struct {
	calls []string
	sha   string
	run   func(uc.RemoteRequest) ([]byte, error)
}

func (f *fixtureRemote) HelperSHA256() string {
	if f.sha != "" {
		return f.sha
	}
	return strings.Repeat("c", 64)
}
func (f *fixtureRemote) Run(_ context.Context, _ Host, request uc.RemoteRequest) ([]byte, error) {
	f.calls = append(f.calls, request.Operation)
	return f.run(request)
}

func collectorFixture(t *testing.T) (*Runner, *fixtureRemote, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	cfg := collectorConfig(t, now)
	scheme := runtime.NewScheme()
	if corev1.AddToScheme(scheme) != nil || v1alpha1.AddToScheme(scheme) != nil {
		t.Fatal("scheme")
	}
	session := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: cfg.Host.Name, UID: types.UID(hostUID), Generation: 1}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, OperatingMode: v1alpha1.OperatingModeRunning}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning, PodName: cfg.Host.Name, Agent: &v1alpha1.AgentStatus{LastHeartbeat: &metav1.Time{Time: now}}}}
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: cfg.Host.Name, UID: types.UID(podUID), OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: cfg.Host.Name, UID: types.UID(hostUID), Controller: &controller}}}, Spec: corev1.PodSpec{ServiceAccountName: cfg.Host.ServiceAccount, Containers: []corev1.Container{{Name: "agent", Image: cfg.Host.Image, VolumeMounts: []corev1.VolumeMount{{Name: "claude", MountPath: "/opt/dev-env/claude-access", ReadOnly: true}, {Name: "codex", MountPath: "/opt/dev-env/codex-access", ReadOnly: true}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	pod.Spec.Volumes = []corev1.Volume{{Name: "claude", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "dev-env-claude-live"}}}, {Name: "codex", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "dev-env-codex-live"}}}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: apiserver.AggregateAllowanceNamespace, Name: apiserver.AggregateAllowanceName, UID: types.UID(slotUID)}, Data: map[string]string{"unrelated": "preserved"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session, pod, cm).Build()
	remote := &fixtureRemote{}
	r := &Runner{Config: cfg, Client: c, Live: c, Remote: remote, Now: func() time.Time { return now }}
	remote.run = func(request uc.RemoteRequest) ([]byte, error) {
		receipt := successfulReceipt(t, request, now)
		now = now.Add(2 * time.Second)
		return json.Marshal(receipt)
	}
	return r, remote, &now
}

func successfulReceipt(t *testing.T, request uc.RemoteRequest, at time.Time) uc.RemoteReceipt {
	t.Helper()
	receipt := uc.RemoteReceipt{Version: 1, State: "complete", RootName: request.RootName, SessionUID: request.SessionUID, PodUID: request.PodUID, Fingerprint: request.Fingerprint, Cycle: request.Cycle, Nonce: request.Nonce, HelperSHA: request.IntentHelperSHA, CleanupProven: true, Captures: []uc.RemoteCapture{}, Attempts: []uc.RemoteAttempt{}, Bindings: []uc.ProviderBinding{}}
	stamp := at.Add(time.Second).UTC().Format(time.RFC3339Nano)
	for _, binding := range request.Providers {
		var payload any
		captured := stamp
		switch binding.Provider {
		case "openai":
			payload = pa.CodexAccountCapture{Schema: pa.CodexRPCSchema, AccountRef: binding.AccountRef, IdentityBasis: "provider_email_sha256", PlanType: "pro", ReceivedAt: stamp, Primary: &pa.CodexWindow{UsedPercent: 14}}
		case "anthropic":
			payload = pa.ClaudeUsageCapture{Schema: pa.ClaudeSDKCaptureSchema, ReceivedAt: stamp, Limits: []pa.ClaudeUsageLimit{{Kind: "weekly_all", Group: "weekly", Percent: json.Number("5"), Severity: "normal", IsActive: true}}}
		case "cursor":
			used := 61
			reset := "Nov 6"
			captured = at.Add(500 * time.Millisecond).UTC().Format(time.RFC3339Nano)
			payload = pa.CursorCLIUsageCapture{Schema: pa.CursorCLICaptureSchema, RendererVersion: pa.CursorCLIQualifiedVersion, CollectionStartedAt: captured, ReceivedAt: stamp, AccountRef: &binding.AccountRef, Plan: "fixture-tier", IncludedPercentDisplay: &used, PercentPrecision: "rounded_integer_display", ResetMonthDay: &reset, Period: "not_exposed"}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		receipt.Captures = append(receipt.Captures, uc.RemoteCapture{Provider: binding.Provider, CapturedAt: captured, ReceivedAt: stamp, Payload: raw})
		receipt.Attempts = append(receipt.Attempts, uc.RemoteAttempt{Provider: binding.Provider, StartedAt: at, CompletedAt: at.Add(2 * time.Second), Success: true})
		receipt.Bindings = append(receipt.Bindings, binding)
	}
	return receipt
}

func TestOperatorCollectorPublishesAtomicallyAndRestartRetainsOriginalClocks(t *testing.T) {
	r, remote, now := collectorFixture(t)
	if err := r.Step(context.Background()); err != nil {
		t.Fatal("initial step", err)
	}
	cm, err := r.slot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cm.Data["unrelated"] != "preserved" || cm.Data[StateKey] == "" || cm.Data[apiserver.AggregateAllowanceDataKey] == "" {
		t.Fatal("atomic state missing")
	}
	held, err := readJournal([]byte(cm.Data[StateKey]), []byte(cm.Data[apiserver.AggregateAllowanceDataKey]), r.Config, *now)
	if err != nil || held.Phase != "idle" || held.Cycle != 1 || len(held.Captures) != 3 {
		var diagnostic journal
		_ = json.Unmarshal([]byte(cm.Data[StateKey]), &diagnostic)
		encoded, _ := bundle(diagnostic.Captures)
		doc, decodeErr := pa.AssembleCaptureBundle(encoded, r.Config.trust, *now)
		for _, item := range doc.Items {
			t.Log(item.Observation.Provider, item.Binding.State, item.Validation.State, item.Validation)
		}
		t.Fatal("journal", err, decodeErr, diagnostic.Phase, diagnostic.ReportSHA == digest([]byte(cm.Data[apiserver.AggregateAllowanceDataKey])))
	}
	var before pa.ReadResponse
	if json.Unmarshal([]byte(cm.Data[apiserver.AggregateAllowanceDataKey]), &before) != nil {
		t.Fatal("decode")
	}
	if len(before.Items) != 3 {
		t.Fatal("three source report missing")
	}
	for _, item := range before.Items {
		if item.Binding.State != pa.BindingVerified || item.Validation.State != pa.ValidationSourceFields || item.Observation.ProviderObservedAt != nil || item.CurrentMeasures[0].Value.Decimal != nil {
			t.Fatal("untrusted/promoted observation", item.Observation.Provider)
		}
	}
	// A newly constructed leader must read the durable cadence and not replay.
	restarted := *r
	if err = restarted.Step(context.Background()); err != nil || len(remote.calls) != 1 {
		t.Fatal("restart replayed", err, remote.calls)
	}
	*now = now.Add(10 * time.Minute)
	refreshHeartbeat(t, r, *now)
	remote.run = func(request uc.RemoteRequest) ([]byte, error) {
		receipt := successfulReceipt(t, request, *now)
		receipt.Captures = []uc.RemoteCapture{}
		receipt.Bindings = []uc.ProviderBinding{}
		for i := range receipt.Attempts {
			receipt.Attempts[i].Success = false
		}
		*now = now.Add(2 * time.Second)
		return json.Marshal(receipt)
	}
	if err = restarted.Step(context.Background()); err != nil {
		t.Fatal("failed provider checks", err)
	}
	cm, _ = r.slot(context.Background())
	var after pa.ReadResponse
	if json.Unmarshal([]byte(cm.Data[apiserver.AggregateAllowanceDataKey]), &after) != nil {
		t.Fatal("decode")
	}
	for i, item := range after.Items {
		if item.Observation.ObservationID != before.Items[i].Observation.ObservationID || item.ReceivedAt != before.Items[i].ReceivedAt {
			t.Fatal("error refreshed/replaced last good")
		}
	}
	for _, status := range after.SourceStatuses {
		if status.LastAttempt.Result != pa.AttemptError || status.LastGood == nil {
			t.Fatal("error/lastgood missing")
		}
	}
}

func refreshHeartbeat(t *testing.T, r *Runner, now time.Time) {
	t.Helper()
	var s v1alpha1.AgentSession
	if r.Live.Get(context.Background(), types.NamespacedName{Namespace: Namespace, Name: r.Config.Host.Name}, &s) != nil {
		t.Fatal("session")
	}
	s.Status.Agent.LastHeartbeat = &metav1.Time{Time: now}
	if r.Client.Update(context.Background(), &s) != nil {
		t.Fatal("heartbeat")
	}
}

func TestOperatorCollectorLostTransportReconcilesSameNonceWithoutRecollection(t *testing.T) {
	r, remote, now := collectorFixture(t)
	var completed []byte
	captureCalls := 0
	remote.run = func(request uc.RemoteRequest) ([]byte, error) {
		if request.Operation == "capture" {
			captureCalls++
			receipt := successfulReceipt(t, request, *now)
			completed, _ = json.Marshal(receipt)
			*now = now.Add(2 * time.Second)
			return nil, errors.New("lost transport")
		}
		return completed, nil
	}
	if r.Step(context.Background()) == nil {
		t.Fatal("lost transport accepted")
	}
	cm, _ := r.slot(context.Background())
	if cm.Data[apiserver.AggregateAllowanceDataKey] != "" {
		t.Fatal("published unknown result")
	}
	*now = now.Add(10 * time.Second)
	restarted := *r
	if err := restarted.Step(context.Background()); err != nil || captureCalls != 1 || strings.Join(remote.calls, ",") != "capture,status" {
		t.Fatal("recovery recollected", err, remote.calls)
	}
	cm, _ = r.slot(context.Background())
	held, err := readJournal([]byte(cm.Data[StateKey]), []byte(cm.Data[apiserver.AggregateAllowanceDataKey]), r.Config, *now)
	if err != nil || held.Cycle != 1 || held.Captures[0].ReceivedAt != time.Date(2026, 11, 2, 21, 0, 1, 0, time.UTC).Format(time.RFC3339Nano) {
		t.Fatal("recovery refreshed original receipt", err)
	}
}

func TestOperatorCollectorInitialFailureAndNeverStartedRemainRetryableWithoutFakeQuota(t *testing.T) {
	for _, state := range []string{"complete", "never_started", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			r, remote, now := collectorFixture(t)
			remote.run = func(request uc.RemoteRequest) ([]byte, error) {
				receipt := successfulReceipt(t, request, *now)
				receipt.State = state
				receipt.Captures = []uc.RemoteCapture{}
				receipt.Bindings = []uc.ProviderBinding{}
				if state != "complete" {
					receipt.Attempts = []uc.RemoteAttempt{}
				} else {
					for i := range receipt.Attempts {
						receipt.Attempts[i].Success = false
					}
				}
				if state == "uncertain" {
					receipt.CleanupProven = false
				}
				*now = now.Add(2 * time.Second)
				return json.Marshal(receipt)
			}
			err := r.Step(context.Background())
			cm, _ := r.slot(context.Background())
			if state == "uncertain" {
				if err == nil || cm.Data[apiserver.AggregateAllowanceDataKey] != "" {
					t.Fatal("uncertain result published")
				}
				return
			}
			if err != nil {
				t.Fatal("failed check cannot be recorded", err)
			}
			var doc pa.ReadResponse
			if json.Unmarshal([]byte(cm.Data[apiserver.AggregateAllowanceDataKey]), &doc) != nil || len(doc.Items) != 0 || len(doc.SourceStatuses) != 3 {
				t.Fatal("failure invented quota")
			}
			for _, status := range doc.SourceStatuses {
				if status.LastGood != nil || status.LastAttempt.Result != pa.AttemptError || status.Binding.State == pa.BindingVerified {
					t.Fatal("failed acquisition invented last good/binding")
				}
			}
			if err = r.Step(context.Background()); err != nil || len(remote.calls) != 1 {
				t.Fatal("backoff replay", err)
			}
		})
	}
}

func TestOperatorCollectorAmbiguousAtomicCommitIsConfirmedWithoutRecollection(t *testing.T) {
	r, remote, _ := collectorFixture(t)
	base := r.Client.(client.WithWatch)
	updates := 0
	r.Client = interceptor.NewClient(base, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		updates++
		if err := c.Update(ctx, obj, opts...); err != nil {
			return err
		}
		if updates == 2 {
			return errors.New("response lost after atomic commit")
		}
		return nil
	}})
	if err := r.Step(context.Background()); err != nil || updates != 2 || len(remote.calls) != 1 {
		t.Fatal("ambiguous commit not confirmed", err, updates)
	}
	cm, _ := r.slot(context.Background())
	var held journal
	if json.Unmarshal([]byte(cm.Data[StateKey]), &held) != nil || held.Phase != "idle" || held.ReportSHA != digest([]byte(cm.Data[apiserver.AggregateAllowanceDataKey])) {
		t.Fatal("report and journal not atomic")
	}
	if err := r.Step(context.Background()); err != nil || updates != 2 || len(remote.calls) != 1 {
		t.Fatal("confirmed result recollected", err)
	}
	aliased := strings.Replace(cm.Data[StateKey], `"capturedAt"`, `"CapturedAt"`, 1)
	if _, err := readJournal([]byte(aliased), []byte(cm.Data[apiserver.AggregateAllowanceDataKey]), r.Config, r.now()); err == nil {
		t.Fatal("case-alias journal field bypassed closed decoder")
	}
}

func TestOperatorCollectorReportOnlyMutationDuringAcquisitionIsNotOverwritten(t *testing.T) {
	r, remote, now := collectorFixture(t)
	if err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(6 * time.Minute)
	refreshHeartbeat(t, r, *now)
	var foreignState string
	remote.run = func(request uc.RemoteRequest) ([]byte, error) {
		receipt := successfulReceipt(t, request, *now)
		cm, err := r.slot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		delete(cm.Data, apiserver.AggregateAllowanceDataKey)
		foreignState = cm.Data[StateKey]
		if err = r.Client.Update(context.Background(), cm); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(2 * time.Second)
		return json.Marshal(receipt)
	}
	if err := r.Step(context.Background()); err == nil {
		t.Fatal("contradictory in-flight report was accepted")
	}
	cm, _ := r.slot(context.Background())
	if cm.Data[StateKey] != foreignState || cm.Data[apiserver.AggregateAllowanceDataKey] != "" {
		t.Fatal("contradictory state overwritten")
	}
	calls := len(remote.calls)
	if r.Step(context.Background()) == nil || len(remote.calls) != calls {
		t.Fatal("contradiction recollected/reset")
	}
}

func TestExpiredCollectorConfigurationSurvivesRestartWithoutAnyHostOrSlotIO(t *testing.T) {
	r, remote, now := collectorFixture(t)
	raw, _ := json.Marshal(r.Config)
	raw = []byte(strings.Replace(string(raw), r.Config.ValidUntil, now.Add(-time.Second).Format(time.RFC3339Nano), 1))
	expired, err := ParseConfig(raw, r.Config.trust, hostUID, "dev-env-workbench", *now)
	if err != nil || expired == nil || !expired.until.Before(*now) {
		t.Fatal("expired collector would prevent core API startup", err)
	}
	r.Config = expired
	reads := 0
	r.Live = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		reads++
		return errors.New("must not read")
	}})
	if r.Step(context.Background()) == nil || reads != 0 || len(remote.calls) != 0 {
		t.Fatal("expired configuration performed IO", reads, remote.calls)
	}
}

func TestOperatorCollectorRefusesForeignExpiredHostAndReservationConflict(t *testing.T) {
	if err := (*Runner)(nil).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"expired", "wrong-pod", "stale-heartbeat", "future-heartbeat", "replaced-slot", "corrupt-journal", "reservation-conflict"} {
		t.Run(mode, func(t *testing.T) {
			r, remote, now := collectorFixture(t)
			switch mode {
			case "expired":
				*now = r.Config.until
			case "wrong-pod":
				r.Config.Host.PodUID = "44444444-4444-4444-4444-444444444444"
			case "stale-heartbeat":
				*now = now.Add(3 * time.Minute)
			case "future-heartbeat":
				*now = now.Add(-time.Second)
			case "replaced-slot":
				r.Config.SlotUID = "44444444-4444-4444-4444-444444444444"
			case "corrupt-journal":
				cm, _ := r.slot(context.Background())
				cm.Data[StateKey] = "{}"
				if r.Client.Update(context.Background(), cm) != nil {
					t.Fatal("update")
				}
			case "reservation-conflict":
				r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
					return errors.New("conflict")
				}})
			}
			if r.Step(context.Background()) == nil || len(remote.calls) != 0 {
				t.Fatal("unsafe dispatch", remote.calls)
			}
		})
	}
}

func TestOperatorUpgradeAndApprovedRenewalRecoverOriginalIntentBeforeNewCapture(t *testing.T) {
	r, remote, now := collectorFixture(t)
	var original []byte
	remote.run = func(request uc.RemoteRequest) ([]byte, error) {
		original, _ = json.Marshal(successfulReceipt(t, request, *now))
		*now = now.Add(2 * time.Second)
		return nil, errors.New("lost original output")
	}
	if err := r.Step(context.Background()); err == nil {
		t.Fatal("expected lost result")
	}
	next := *r.Config
	next.ValidUntil = now.Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
	raw, _ := json.Marshal(next)
	renewed, err := ParseConfig(raw, r.Config.trust, hostUID, "dev-env-workbench", *now)
	if err != nil {
		t.Fatal(err)
	}
	r.Config = renewed
	remote.sha = strings.Repeat("d", 64)
	remote.run = func(request uc.RemoteRequest) ([]byte, error) {
		if request.Operation != "status" || request.HelperSHA != remote.sha || request.IntentHelperSHA != strings.Repeat("c", 64) {
			t.Fatal("upgrade replayed or relabelled intent")
		}
		return original, nil
	}
	if err = r.Step(context.Background()); err != nil {
		t.Fatal("original receipt not recoverable", err)
	}
	cm, err := r.slot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	held, err := readJournal([]byte(cm.Data[StateKey]), []byte(cm.Data[apiserver.AggregateAllowanceDataKey]), r.Config, *now)
	if err != nil || held.Cycle != 1 || held.HelperSHA != strings.Repeat("c", 64) || len(held.Captures) != 3 {
		t.Fatal("original journal lost", err)
	}
	*now = now.Add(301 * time.Second)
	var session v1alpha1.AgentSession
	key := types.NamespacedName{Namespace: Namespace, Name: r.Config.Host.Name}
	if r.Client.Get(context.Background(), key, &session) != nil {
		t.Fatal("session")
	}
	session.Status.Agent.LastHeartbeat = &metav1.Time{Time: *now}
	if r.Client.Update(context.Background(), &session) != nil {
		t.Fatal("heartbeat")
	}
	remote.run = func(request uc.RemoteRequest) ([]byte, error) {
		if request.Operation != "capture" || request.Cycle != 2 || request.IntentHelperSHA != remote.sha {
			t.Fatal("next cycle did not bind new helper")
		}
		receipt := successfulReceipt(t, request, *now)
		*now = now.Add(2 * time.Second)
		return json.Marshal(receipt)
	}
	if err = r.Step(context.Background()); err != nil {
		t.Fatal("next clean cycle blocked", err)
	}
}
