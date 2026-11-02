package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
	"github.com/thaynes43/dev-env/web/portal/session"
)

func TestUsageReadCurrentSerializesHandlerSnapshots(t *testing.T) {
	if !strings.Contains(commandHelp["usage"], "--read-current") || !strings.Contains(commandHelp["usage"], usageAggregateAllowancePath) || !strings.Contains(commandHelp["usage"], "usage-source\n--read-current is unchanged") {
		t.Fatal("usage help does not describe the aggregate read")
	}
	if !strings.Contains(commandHelp["usage-source"], "/v1/provider-source-reports/codex-current-source") || strings.Contains(commandHelp["usage-source"], usageAggregateAllowancePath) {
		t.Fatal("usage-source help changed slot")
	}
	serving := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	snaps := []providerallowance.ReadResponse{remoteAllowance("a"), remoteAllowance("b"), remoteAllowance("a")}
	var gotNow []time.Time
	calls := 0
	handler, allow, foreign := remoteAllowanceHandler(t, func(ctx context.Context, now time.Time) (providerallowance.ReadResponse, error) {
		if err := ctx.Err(); err != nil {
			return providerallowance.ReadResponse{}, err
		}
		calls++
		gotNow = append(gotNow, now)
		return snaps[calls-1], nil
	}, &serving)
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	env := remoteAllowanceEnv(t, srv, allow)
	var decoded []providerallowance.ReadResponse
	for i := 0; i < 3; i++ {
		stdout, stderr := remoteRun(t, env, ExitOK, "usage", "-o", "json", "--read-current")
		if strings.Contains(stdout, serving.Format(time.RFC3339)) || strings.Contains(stderr, "SECRET") {
			t.Fatalf("serving time or secret in output %s %s", stdout, stderr)
		}
		got := remoteDecode(t, []byte(stdout))
		want := remoteRoundTrip(t, snaps[i])
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("read %d did not preserve the handler document", i)
		}
		decoded = append(decoded, got)
		serving = serving.Add(time.Hour)
	}
	if calls != 3 || hits != 3 || !gotNow[0].Equal(time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)) || !gotNow[2].Equal(time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("calls %d hits %d %#v", calls, hits, gotNow)
	}
	if !reflect.DeepEqual(decoded[0], decoded[2]) || reflect.DeepEqual(decoded[0], decoded[1]) {
		t.Fatal("replay changed the provider snapshot")
	}
	if decoded[0].Items[0].Observation.ProviderObservedAt == nil || *decoded[0].Items[0].Observation.ProviderObservedAt != "2026-10-28T10:00:00Z" || decoded[0].Items[0].Observation.CapturedAt != "2026-10-28T11:00:00Z" || decoded[0].Items[0].ReceivedAt != "2026-10-28T11:00:01Z" || decoded[0].Items[0].Freshness.State != providerallowance.FreshStale {
		t.Fatal("first snapshot times changed")
	}
	if decoded[1].Items[0].Observation.ProviderObservedAt == nil || *decoded[1].Items[0].Observation.ProviderObservedAt != "2099-11-04T00:00:00Z" || decoded[1].Items[0].Freshness.State != providerallowance.FreshUnknown {
		t.Fatal("future observation was refreshed")
	}
	if foreign == "" {
		t.Fatal("missing foreign token")
	}
	projectRemoteAllowance(t, decoded[0], remoteRunStdout(t, remoteAllowanceEnv(t, srv, allow), "usage", "-o", "json"))
}

func TestUsageReadCurrentRefusesWithoutIO(t *testing.T) {
	serving := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	calls := 0
	handler, allow, foreign := remoteAllowanceHandler(t, func(context.Context, time.Time) (providerallowance.ReadResponse, error) {
		calls++
		return remoteAllowance("a"), nil
	}, &serving)
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	env := remoteAllowanceEnv(t, srv, allow)
	before := hits
	if code := Run(context.Background(), []string{"usage", "-o", "json"}, env); code != ExitOK || hits != before || calls != 0 {
		t.Fatalf("default connected code %d hits %d calls %d", code, hits, calls)
	}
	for _, args := range [][]string{
		{"usage", "-o", "json", "--read-current", "--from-file", "-"},
		{"usage", "-o", "json", "--read-current", "--claude-statusline"},
		{"usage", "-o", "json", "--read-current", "--read-current"},
	} {
		if code := Run(context.Background(), args, env); code != ExitUsage || hits != before || calls != 0 {
			t.Fatalf("%v code %d hits %d calls %d", args, code, hits, calls)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := Run(ctx, []string{"usage", "-o", "json", "--read-current"}, env); code == ExitOK || hits != before || calls != 0 {
		t.Fatalf("cancelled code %d hits %d calls %d", code, hits, calls)
	}
	stdout, stderr := remoteRun(t, remoteAllowanceEnv(t, srv, "tok-foreign"), ExitFailed, "usage", "-o", "json", "--read-current")
	if stdout != "" || strings.Contains(stderr, "SECRET-TOKEN-CANARY") || calls != 0 {
		t.Fatalf("unauthorized output %s %s calls %d", stdout, stderr, calls)
	}
	stdout, stderr = remoteRun(t, remoteAllowanceEnv(t, srv, foreign), ExitAuth, "usage", "-o", "json", "--read-current")
	if stdout != "" || calls != 0 || hits == before {
		t.Fatalf("foreign output %s calls %d hits %d", stderr, calls, hits)
	}
	disabledHits := 0
	disabled := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disabledHits++
		handlerWithoutAllowance(t, &serving).ServeHTTP(w, r)
	}))
	t.Cleanup(disabled.Close)
	stdout, stderr = remoteRun(t, remoteAllowanceEnv(t, disabled, allow), ExitNotFound, "usage", "-o", "json", "--read-current")
	if stdout != "" || disabledHits != 1 || calls != 0 {
		t.Fatalf("disabled output %s hits %d %s", stdout, disabledHits, stderr)
	}
}

func TestUsageReadCurrentClosedClient(t *testing.T) {
	canary := "SECRET-REDIRECT-CANARY"
	hits := map[string]int{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		if r.URL.Path == "/v1/provider-allowances" {
			w.Header().Set("Location", "/secret")
			w.WriteHeader(http.StatusFound)
			_, _ = io.WriteString(w, canary)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, canary)
	}))
	t.Cleanup(srv.Close)
	env := remoteAllowanceEnv(t, srv, "any-token")
	stdout, stderr := remoteRun(t, env, ExitFailed, "usage", "-o", "json", "--read-current")
	if hits["/v1/provider-allowances"] != 1 || hits["/secret"] != 0 || stdout != "" || strings.Contains(stderr, canary) {
		t.Fatalf("redirect followed %#v %s %s", hits, stdout, stderr)
	}
	hits = map[string]int{}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.RequestURI()]++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, canary)
	})
	stdout, stderr = remoteRun(t, env, ExitFailed, "usage", "-o", "json", "--read-current")
	if hits["/v1/provider-allowances"] != 1 || stdout != "" || strings.Contains(stderr, canary) {
		t.Fatalf("retry or echo %#v %s %s", hits, stdout, stderr)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "SECRET-DOC-CANARY", http.StatusOK)
	})
	stdout, stderr = remoteRun(t, env, ExitFailed, "usage", "-o", "json", "--read-current")
	if stdout != "" || strings.Contains(stderr, "SECRET-DOC-CANARY") {
		t.Fatalf("invalid document echoed %s %s", stdout, stderr)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("SECRET-SIZE-CANARY", 20000))
	})
	stdout, stderr = remoteRun(t, env, ExitFailed, "usage", "-o", "json", "--read-current")
	if stdout != "" || strings.Contains(stderr, "SECRET-SIZE-CANARY") {
		t.Fatalf("oversized body echoed %s %s", stdout, stderr)
	}
	started := make(chan struct{})
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"usage", "-o", "json", "--read-current"}, env)
	}()
	select {
	case <-started:
		cancel()
	case code := <-done:
		t.Fatal("request returned before the handler", code)
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach the handler")
	}
	select {
	case code := <-done:
		if code == ExitOK || env.Stdout.(*bytes.Buffer).Len() != 0 || strings.Contains(env.Stderr.(*bytes.Buffer).String(), canary) {
			t.Fatal("cancelled exchange printed a document", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled exchange did not return")
	}
}

func projectRemoteAllowance(t *testing.T, doc providerallowance.ReadResponse, local string) {
	t.Helper()
	if strings.Contains(local, usageAggregateAllowancePath) || !strings.Contains(local, "local_unconfigured_cursor_no_provider_request") {
		t.Fatal("local preflight changed")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	trust, reg := remoteTrust()
	// Direct projection of the wire document does not receive caller trust.
	// NewCachedAllowanceReader applies only the trust passed here. Neither call
	// is installed on a portal SessionSource; production assembly does not call
	// SetAllowanceReader.
	direct := session.MapProductAllowance(doc)
	reader := session.NewCachedAllowanceReader(raw, providerallowance.FixtureParseLimits, []providerallowance.TrustedContext{trust}, reg, session.AllowanceReadPolicy{MaxAge: 1000 * time.Hour, Set: true})
	projected := reader.Allowances(time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC))
	again := reader.Allowances(time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC))
	if !reflect.DeepEqual(projected, again) || doc.Items[0].Freshness.State != providerallowance.FreshStale || doc.Items[0].Observation.ProviderObservedAt == nil || *doc.Items[0].Observation.ProviderObservedAt != "2026-10-28T10:00:00Z" {
		t.Fatal("reader refreshed provider freshness or mutated the caller document")
	}
	assertUnknownProvider(t, direct, "Cursor")
	assertUnknownProvider(t, direct, "Codex")
	assertUnknownProvider(t, projected, "Cursor")
	assertUnknownProvider(t, projected, "Codex")
	claude := allowanceRow(t, projected, "Claude")
	if claude.Value != portal.AllowanceStale || len(claude.Windows) != 2 {
		t.Fatalf("trusted projection %+v", claude)
	}
	if claude.Windows[0].Percent == claude.Windows[1].Percent || strings.Contains(claude.Windows[0].Percent, "96") || strings.Contains(claude.Windows[1].Percent, "96") {
		t.Fatalf("windows were summed %+v", claude.Windows)
	}
	var display, instant bool
	for _, window := range claude.Windows {
		if window.Percent == "0% left" || window.Percent == "0% used" {
			t.Fatalf("missing value became zero %+v", window)
		}
		switch window.ResetPrecision {
		case "display_only":
			display = window.ResetAt == "" && strings.Contains(window.Reset, "display only")
		case "instant":
			instant = window.ResetAt == "2026-10-28T17:00:00Z"
		}
		if window.ObservedAt != "2026-10-28T10:00:00Z" {
			t.Fatalf("projection refreshed provider time %+v", window)
		}
	}
	if !display || !instant {
		t.Fatalf("reset precision dropped %+v", claude.Windows)
	}
	wire := allowanceRow(t, direct, "Claude")
	if wire.Value == portal.AllowanceMeasured || wire.Value == portal.AllowanceStale {
		t.Fatalf("untrusted wire became an entitlement %+v", wire)
	}
}

func remoteAllowanceHandler(t *testing.T, source func(context.Context, time.Time) (providerallowance.ReadResponse, error), now *time.Time) (http.Handler, string, string) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	allowUID := types.UID("allow-session")
	foreignUID := types.UID("foreign-session")
	allowPod := types.UID("allow-pod")
	foreignPod := types.UID("foreign-pod")
	allowSess := remoteSession("reader", allowUID)
	foreignSess := remoteSession("other", foreignUID)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(allowSess, foreignSess, remotePod(allowSess, allowPod), remotePod(foreignSess, foreignPod)).Build()
	allowToken := "tok-allow"
	foreignToken := "tok-foreign-session"
	auth := remoteAuth{tokens: map[string]apiserver.Identity{
		allowToken:    remoteIdentity(allowPod),
		foreignToken:  remoteForeignIdentity(foreignPod),
		"tok-foreign": {},
	}, reject: map[string]string{"tok-foreign": "SECRET-TOKEN-CANARY"}}
	srv := &apiserver.Server{
		Client: c, Live: c, Auth: auth, Log: logr.Discard(),
		Now: func() time.Time { return *now },
		Policy: apiserver.Policy{
			Human: "dev-env-system/dev-env-human", Clients: []string{"dev/dev-env"},
			SessionNamespace: "dev-agents", SessionServiceAccount: controller.ServiceAccountName,
		},
		AggregateAllowanceReports: &apiserver.AggregateAllowanceReports{
			Enabled: true, Source: source, Now: func() time.Time { return *now },
			SessionNamespace: "dev-agents", SessionServiceAccount: controller.ServiceAccountName,
			SessionUIDs: []types.UID{allowUID},
		},
	}
	return srv.Handler(), allowToken, foreignToken
}

func handlerWithoutAllowance(t *testing.T, now *time.Time) http.Handler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &apiserver.Server{Client: c, Live: c, Auth: remoteAuth{}, Log: logr.Discard(), Now: func() time.Time { return *now }}
	if srv.AggregateAllowanceReports != nil {
		t.Fatal("disabled server mounted aggregate reports")
	}
	return srv.Handler()
}

func remoteSession(name string, uid types.UID) *v1alpha1.AgentSession {
	return &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dev-agents", UID: uid}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: "dev-env-system/dev-env-human"}}
}

func remotePod(sess *v1alpha1.AgentSession, uid types.UID) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: sess.Name, Namespace: sess.Namespace, UID: uid, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sess, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Spec:       corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "x"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func remoteIdentity(pod types.UID) apiserver.Identity {
	return apiserver.Identity{Username: "system:serviceaccount:dev-agents:" + controller.ServiceAccountName, Namespace: "dev-agents", ServiceAccount: controller.ServiceAccountName, PodName: "reader", PodUID: string(pod)}
}

func remoteForeignIdentity(pod types.UID) apiserver.Identity {
	id := remoteIdentity(pod)
	id.PodName = "other"
	return id
}

type remoteAuth struct {
	tokens map[string]apiserver.Identity
	reject map[string]string
}

func (a remoteAuth) Authenticate(_ context.Context, token string) (apiserver.Identity, error) {
	if a.reject != nil && a.reject[token] != "" {
		return apiserver.Identity{}, errors.New(a.reject[token])
	}
	id, ok := a.tokens[token]
	if !ok || id.ServiceAccount == "" {
		return apiserver.Identity{}, errors.New("SECRET-TOKEN-CANARY")
	}
	return id, nil
}

func remoteAllowanceEnv(t *testing.T, srv *httptest.Server, token string) Env {
	t.Helper()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(dir, "token")
	if err := os.WriteFile(tok, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	return Env{
		Stdout: &stdout, Stderr: &stderr, Stdin: failReader{},
		Getenv: func(k string) string {
			switch k {
			case envAPIURL:
				return srv.URL
			case envTokenFile:
				return tok
			case envCAFile:
				return ca
			default:
				return ""
			}
		},
		Now:               func() time.Time { return time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC) },
		Sleep:             func(context.Context, time.Duration) error { return errors.New("retry") },
		SessionTokenFile:  filepath.Join(dir, "no-session-token"),
		ServiceAccountDir: filepath.Join(dir, "no-serviceaccount"),
		LookPath:          func(string) (string, error) { return "", errors.New("not found") },
	}
}

func remoteRun(t *testing.T, env Env, want int, args ...string) (string, string) {
	t.Helper()
	env.Stdout.(*bytes.Buffer).Reset()
	env.Stderr.(*bytes.Buffer).Reset()
	if code := Run(context.Background(), args, env); code != want {
		t.Fatalf("%v exit %d want %d\n%s\n%s", args, code, want, env.Stdout.(*bytes.Buffer).String(), env.Stderr.(*bytes.Buffer).String())
	}
	return env.Stdout.(*bytes.Buffer).String(), env.Stderr.(*bytes.Buffer).String()
}

func remoteRunStdout(t *testing.T, env Env, args ...string) string {
	t.Helper()
	out, _ := remoteRun(t, env, ExitOK, args...)
	return out
}

func remoteDecode(t *testing.T, raw []byte) providerallowance.ReadResponse {
	t.Helper()
	doc, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func remoteRoundTrip(t *testing.T, doc providerallowance.ReadResponse) providerallowance.ReadResponse {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func allowanceRow(t *testing.T, rows []portal.ProductAllowance, provider string) portal.ProductAllowance {
	t.Helper()
	for _, row := range rows {
		if row.Provider == provider {
			return row
		}
	}
	t.Fatal("missing", provider)
	return portal.ProductAllowance{}
}

func assertUnknownProvider(t *testing.T, rows []portal.ProductAllowance, provider string) {
	t.Helper()
	row := allowanceRow(t, rows, provider)
	if row.Value != portal.AllowanceUnknown || len(row.Windows) != 0 || strings.Contains(row.Reason, "0%") {
		t.Fatalf("%s status %+v", provider, row)
	}
}

func remoteTrust() (providerallowance.TrustedContext, *providerallowance.StaticRegistry) {
	account := "fixture-account"
	workspace := "fixture-workspace"
	min, max := "0", "100"
	trust := providerallowance.TrustedContext{
		ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation", BindingGeneration: 7,
		AccountRef: &account, WorkspaceRef: &workspace, BindingState: providerallowance.BindingVerified,
		Provider: "anthropic", SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
	}
	reg := providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources:  []providerallowance.SourceRule{{Provider: "anthropic", Kind: providerallowance.SourceNativeRPC, Version: "fixture-v1", CapabilityRef: "fixture:synthetic", AllowNormalizedMeasures: true}},
		Metrics:  []providerallowance.MetricRule{{ValidationRef: "fixture:synthetic-allowance", Provider: "anthropic", SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1", Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionRemaining, Windows: []string{"thirty-minute", "one-hour"}, Min: &min, Max: &max}},
		Accounts: []providerallowance.AccountRule{{AccountRef: account, WorkspaceRef: workspace, Generation: 7, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation"}},
	})
	return trust, reg
}

func remoteAllowance(kind string) providerallowance.ReadResponse {
	account := "fixture-account"
	workspace := "fixture-workspace"
	if kind == "b" {
		observed := "2099-11-04T00:00:00Z"
		return providerallowance.ReadResponse{
			Schema: providerallowance.SchemaRead, SnapshotRevision: "remote-b", ServedAt: "2026-10-29T00:00:00Z",
			Items: []providerallowance.ReadItem{{
				Observation: providerallowance.Observation{
					Schema: providerallowance.SchemaObservation, ObservationID: "remote-b", ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation",
					Sequence: 1, BindingGeneration: 7, Provider: "anthropic",
					Source:     providerallowance.Source{Kind: providerallowance.SourceNativeDisplay, Version: "fixture-v1", CapabilityRef: "fixture:synthetic"},
					Scope:      providerallowance.Scope{AccountRef: &account, WorkspaceRef: &workspace, BindingState: providerallowance.BindingUnknown},
					CapturedAt: "2026-10-29T00:00:01Z", ProviderObservedAt: &observed, Availability: providerallowance.AvailabilityUnsupported, Completeness: providerallowance.CompletenessUnknown,
				},
				ReceivedAt: "2026-10-29T00:00:02Z",
				Validation: providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified},
				Binding:    providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
				Freshness:  providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: observed, Basis: providerallowance.BasisNone, Reason: "future provider time"},
			}},
			SourceStatuses: []providerallowance.SourceStatus{
				remoteStatus("anthropic", providerallowance.AvailabilityUnsupported, providerallowance.FreshUnknown, providerallowance.BasisNone, observed, false),
				remoteStatus("openai", providerallowance.AvailabilityUnavailable, providerallowance.FreshUnknown, providerallowance.BasisNone, "2026-10-29T00:00:02Z", false),
				remoteStatus("cursor", providerallowance.AvailabilityUnsupported, providerallowance.FreshUnknown, providerallowance.BasisNone, "2026-10-29T00:00:02Z", false),
			},
		}
	}
	observed := "2026-10-28T10:00:00Z"
	displayAt := "2026-11-04T00:00:00Z"
	instantAt := "2026-10-28T17:00:00Z"
	weekly := "56"
	sessionPercent := "40"
	week := providerallowance.Count(30)
	hour := providerallowance.Count(60)
	ref := "fixture:synthetic-allowance"
	measures := []providerallowance.Measure{
		{ID: "weekly-display", Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionRemaining, Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: &weekly}, Window: providerallowance.Window{Ref: "thirty-minute", DurationMinutes: &week, ResetAt: &displayAt, ResetPrecision: providerallowance.ResetDisplayOnly}, ValidationRef: &ref},
		{ID: "session-window", Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionRemaining, Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: &sessionPercent}, Window: providerallowance.Window{Ref: "one-hour", DurationMinutes: &hour, ResetAt: &instantAt, ResetPrecision: providerallowance.ResetInstant}, ValidationRef: &ref},
	}
	currents := []providerallowance.CurrentMeasure{
		{MeasureID: "weekly-display", State: providerallowance.CurrentKnown, Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: &weekly}},
		{MeasureID: "session-window", State: providerallowance.CurrentKnown, Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: &sessionPercent}},
	}
	return providerallowance.ReadResponse{
		Schema: providerallowance.SchemaRead, SnapshotRevision: "remote-a", ServedAt: "2026-10-28T12:00:00Z",
		Items: []providerallowance.ReadItem{{
			Observation: providerallowance.Observation{
				Schema: providerallowance.SchemaObservation, ObservationID: "remote-a", ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation",
				Sequence: 1, BindingGeneration: 7, Provider: "anthropic",
				Source:     providerallowance.Source{Kind: providerallowance.SourceNativeRPC, Version: "fixture-v1", CapabilityRef: "fixture:synthetic"},
				Scope:      providerallowance.Scope{AccountRef: &account, WorkspaceRef: &workspace, BindingState: providerallowance.BindingVerified},
				CapturedAt: "2026-10-28T11:00:00Z", ProviderObservedAt: &observed, Availability: providerallowance.AvailabilityPartial, Completeness: providerallowance.CompletenessPartial,
				Measures: measures,
			},
			ReceivedAt: "2026-10-28T11:00:01Z", Validation: providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified},
			Binding:         providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
			Freshness:       providerallowance.Freshness{State: providerallowance.FreshStale, AsOf: observed, Basis: providerallowance.BasisProvider, Reason: "stale capture"},
			CurrentMeasures: currents,
		}},
		SourceStatuses: []providerallowance.SourceStatus{
			remoteTrustedStatus(observed),
			remoteStatus("openai", providerallowance.AvailabilityUnavailable, providerallowance.FreshUnknown, providerallowance.BasisNone, "2026-10-28T11:00:01Z", false),
			remoteStatus("cursor", providerallowance.AvailabilityUnsupported, providerallowance.FreshUnknown, providerallowance.BasisNone, "2026-10-28T11:00:01Z", false),
		},
	}
}

func remoteTrustedStatus(observed string) providerallowance.SourceStatus {
	account := "fixture-account"
	workspace := "fixture-workspace"
	observedCopy := observed
	code := "partial_capture"
	completed := "2026-10-28T11:00:01Z"
	return providerallowance.SourceStatus{
		Provider: "anthropic", ProducerRef: "fixture-producer", Availability: providerallowance.AvailabilityPartial,
		LastGood:    &providerallowance.LastGood{ObservationID: "remote-a", CapturedAt: "2026-10-28T11:00:00Z", ProviderObservedAt: &observedCopy, ReceivedAt: completed},
		LastAttempt: providerallowance.LastAttempt{AttemptedAt: "2026-10-28T11:00:00Z", CompletedAt: &completed, Result: providerallowance.AttemptError, Code: &code},
		Freshness:   providerallowance.Freshness{State: providerallowance.FreshStale, AsOf: observed, Basis: providerallowance.BasisProvider, Reason: "stale status"},
		Validation:  providerallowance.ValidationRecord{State: providerallowance.ValidationSourceFields},
		Binding:     providerallowance.BindingRecord{State: providerallowance.BindingVerified, Generation: 7, AccountRef: &account, WorkspaceRef: &workspace},
	}
}

func remoteStatus(provider string, availability providerallowance.Availability, fresh providerallowance.FreshnessState, basis providerallowance.FreshnessBasis, at string, good bool) providerallowance.SourceStatus {
	code := "source_not_reported"
	status := providerallowance.SourceStatus{
		Provider: provider, ProducerRef: "fixture-producer", Availability: availability,
		LastAttempt: providerallowance.LastAttempt{AttemptedAt: at, Result: providerallowance.AttemptUnknown, Code: &code},
		Freshness:   providerallowance.Freshness{State: fresh, AsOf: at, Basis: basis, Reason: "explicit missing measurement"},
		Validation:  providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified},
		Binding:     providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
	}
	if good {
		status.LastGood = &providerallowance.LastGood{ObservationID: provider, CapturedAt: at, ReceivedAt: at}
	}
	return status
}
