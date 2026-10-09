package keeper

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

func syntheticCodexAccess(t *testing.T, now time.Time, generation uint64, account string, life time.Duration) codexauth.Access {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"exp": now.Add(life).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account}})
	if err != nil {
		t.Fatal(err)
	}
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"synthetic"}`)) + "." + base64.RawURLEncoding.EncodeToString(raw) + ".synthetic-signature"
	return codexauth.Access{Generation: generation, AccountID: account, IDToken: jwt, AccessToken: jwt, ExpiresAt: now.Add(life), LastRefresh: now}
}

type codexTestTransport struct {
	Calls int
	Run   func(context.Context, secretValue) (secretValue, secretValue, secretValue, error)
}

func (t *codexTestTransport) refresh(ctx context.Context, r secretValue) (secretValue, secretValue, secretValue, error) {
	t.Calls++
	return t.Run(ctx, r)
}

type codexTestPublisher struct {
	Calls int
	Run   func(codexauth.Access) error
}

func (p *codexTestPublisher) publish(_ context.Context, a codexauth.Access, _ time.Time) error {
	p.Calls++
	if p.Run != nil {
		return p.Run(a)
	}
	return nil
}

func codexFixture(t *testing.T) (*codexWorker, *codexTestTransport, *codexTestPublisher) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	jname := types.NamespacedName{Namespace: "system", Name: DefaultCodexJournalSecret}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: jname.Namespace, Name: jname.Name}}).Build()
	now := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	w := &codexWorker{Journal: &codexJournal{Client: c, Secret: jname}, Clock: clocktesting.NewFakeClock(now), Fence: func(context.Context, time.Duration) error { return nil }, LoginDir: t.TempDir(), Identity: "keeper-synthetic", Log: logr.Discard()}
	x := &codexTestTransport{}
	p := &codexTestPublisher{}
	w.Transport, w.Publisher = x, p
	return w, x, p
}

func saveCodexFixture(t *testing.T, w *codexWorker, r codexRecord) {
	t.Helper()
	d, err := w.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Journal.save(context.Background(), d.Secret, r); err != nil {
		t.Fatal(err)
	}
}

func dueCodexRecord(t *testing.T, w *codexWorker) codexRecord {
	return codexRecord{Access: syntheticCodexAccess(t, w.Clock.Now().Add(-9*24*time.Hour), 1, "synthetic-account", 10*24*time.Hour), Refresh: newSecretValue("old-refresh-synthetic-canary"), Stage: codexReady}
}

func TestCodexRefreshIntentOrderingAndPublicationRetry(t *testing.T) {
	w, x, p := codexFixture(t)
	saveCodexFixture(t, w, dueCodexRecord(t, w))
	next := syntheticCodexAccess(t, w.Clock.Now(), 2, "synthetic-account", 10*24*time.Hour)
	x.Run = func(ctx context.Context, r secretValue) (secretValue, secretValue, secretValue, error) {
		d, err := w.load(ctx)
		if err != nil || d.Record.Stage != codexIntent || d.Record.AttemptUID == "" || r.Reveal() != "old-refresh-synthetic-canary" {
			t.Fatal("POST preceded durable intent")
		}
		return newSecretValue(next.IDToken), newSecretValue(next.AccessToken), newSecretValue("next-refresh-synthetic-canary"), nil
	}
	p.Run = func(a codexauth.Access) error {
		d, err := w.load(context.Background())
		if err != nil || d.Record.Stage != codexReady || d.Record.Refresh.Reveal() != "next-refresh-synthetic-canary" || a.Generation != 2 {
			t.Fatal("publication preceded confirmed replacement")
		}
		raw, err := codexauth.Encode(a, w.Clock.Now())
		if err != nil || bytes.Contains(raw, []byte("refresh_token")) || bytes.Contains(raw, []byte("refresh-synthetic-canary")) {
			t.Fatal("refresh escaped public boundary")
		}
		if p.Calls == 1 {
			return errors.New("synthetic publication unavailable")
		}
		return nil
	}
	if w.tick(context.Background()) == nil {
		t.Fatal("failed publication accepted")
	}
	if w.tick(context.Background()) != nil || x.Calls != 1 || p.Calls != 2 || !w.ready.Load() {
		t.Fatal("publication retry repeated rotating refresh")
	}
}

func TestCodexRefreshBudgetAndAmbiguousResponse(t *testing.T) {
	for _, kind := range []string{"no-budget", "lost-before-post", "response", "account", "expiry", "missing-refresh"} {
		t.Run(kind, func(t *testing.T) {
			w, x, p := codexFixture(t)
			saveCodexFixture(t, w, dueCodexRecord(t, w))
			fences := 0
			w.Fence = func(_ context.Context, budget time.Duration) error {
				fences++
				if kind == "no-budget" || (kind == "lost-before-post" && fences > 1 && budget == codexRequestBudget) {
					return errors.New("synthetic leadership loss")
				}
				return nil
			}
			x.Run = func(context.Context, secretValue) (secretValue, secretValue, secretValue, error) {
				if kind == "response" {
					return secretValue{}, secretValue{}, secretValue{}, errors.New("secret-echo-synthetic-canary")
				}
				account, life := "synthetic-account", 10*24*time.Hour
				if kind == "account" {
					account = "other-synthetic-account"
				}
				if kind == "expiry" {
					life = time.Minute
				}
				n := syntheticCodexAccess(t, w.Clock.Now(), 2, account, life)
				refresh := newSecretValue("next-refresh-synthetic-canary")
				if kind == "missing-refresh" {
					refresh = secretValue{}
				}
				return newSecretValue(n.IDToken), newSecretValue(n.AccessToken), refresh, nil
			}
			err := w.tick(context.Background())
			if err == nil || strings.Contains(err.Error(), "canary") || p.Calls != 0 {
				t.Fatal("unsafe refresh result published")
			}
			_ = w.tick(context.Background())
			want := 1
			if kind == "no-budget" || kind == "lost-before-post" {
				want = 0
			}
			if x.Calls != want {
				t.Fatal("ambiguous request was replayed")
			}
		})
	}
}

func TestCodexLoginCancellationRestoresOnlyKnownReady(t *testing.T) {
	for _, kind := range []string{"ready", "expired", "recovered-intent", "halted", "cas-error", "budget-loss"} {
		t.Run(kind, func(t *testing.T) {
			w, x, p := codexFixture(t)
			old := dueCodexRecord(t, w)
			if kind == "recovered-intent" {
				old.Stage, old.AttemptUID = codexIntent, "00000000-0000-0000-0000-000000000001"
			}
			saveCodexFixture(t, w, old)
			w.halted = kind == "halted"
			r := w.begin(context.Background())
			if !r.OK {
				t.Fatal("reservation failed")
			}
			_ = w.tick(context.Background())
			if x.Calls != 0 || p.Calls != 0 {
				t.Fatal("reserved ceremony did not pause refresh")
			}
			if kind == "expired" {
				w.Clock.(*clocktesting.FakeClock).Step(2 * 24 * time.Hour)
			}
			if kind == "cas-error" {
				w.Journal.Client = &codexAmbiguousClient{Client: w.Journal.Client, Failed: true, BlockTombstone: true}
			}
			if kind == "budget-loss" {
				w.Fence = func(context.Context, time.Duration) error { return errors.New("synthetic loss") }
			}
			result := w.cancelLogin(context.Background(), r.AttemptUID)
			d, err := w.load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if kind == "ready" {
				if !result.OK || d.Record.Stage != codexReady || !sameCodexMaterial(old, d.Record) || w.halted {
					t.Fatal("known unused credential was not restored")
				}
			} else if d.Record.Stage == codexReady || !w.halted {
				t.Fatal("uncertain, expired or unfenced credential restored")
			}
		})
	}
}

type codexRollbackAmbiguousClient struct {
	client.Client
	Failed bool
}

func (c *codexRollbackAmbiguousClient) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
	if c.Failed {
		return errors.New("synthetic offline journal")
	}
	raw, _ := p.Data(obj)
	var patch struct {
		Data map[string][]byte `json:"data"`
	}
	_ = json.Unmarshal(raw, &patch)
	var state codexRecordWire
	_ = json.Unmarshal(patch.Data[codexJournalKey], &state)
	if err := c.Client.Patch(ctx, obj, p, opts...); err != nil {
		return err
	}
	if state.Stage == codexReady {
		c.Failed = true
		return errors.New("synthetic lost rollback acknowledgement")
	}
	return nil
}

func TestCodexPrePOSTRollbackRequiresConfirmedCAS(t *testing.T) {
	w, x, p := codexFixture(t)
	saveCodexFixture(t, w, dueCodexRecord(t, w))
	w.Journal.Client = &codexRollbackAmbiguousClient{Client: w.Journal.Client}
	calls := 0
	w.Fence = func(_ context.Context, budget time.Duration) error {
		calls++
		if calls > 1 && budget == codexRequestBudget {
			return errors.New("synthetic short budget")
		}
		return nil
	}
	if w.tick(context.Background()) == nil || !w.halted {
		t.Fatal("unacknowledged rollback resumed worker")
	}
	_ = w.tick(context.Background())
	if x.Calls != 0 || p.Calls != 0 {
		t.Fatal("pre-POST rollback ambiguity dispatched or published")
	}
}

type codexAmbiguousClient struct {
	client.Client
	Stage          string
	BlockTombstone bool
	Failed         bool
}

func (c *codexAmbiguousClient) Patch(ctx context.Context, obj client.Object, p client.Patch, options ...client.PatchOption) error {
	raw, err := p.Data(obj)
	if err != nil {
		return err
	}
	var patch struct {
		Data map[string][]byte `json:"data"`
	}
	_ = json.Unmarshal(raw, &patch)
	var state codexRecordWire
	_ = json.Unmarshal(patch.Data[codexJournalKey], &state)
	if c.Failed && c.BlockTombstone {
		return errors.New("synthetic journal offline")
	}
	if state.Generation == 2 && state.Stage == c.Stage && !c.Failed {
		if err := c.Client.Patch(ctx, obj, p, options...); err != nil {
			return err
		}
		c.Failed = true
		return errors.New("synthetic applied write lost response")
	}
	return c.Client.Patch(ctx, obj, p, options...)
}

func TestCodexAppliedButUnacknowledgedCASRecovery(t *testing.T) {
	for _, stage := range []string{codexIntent, codexReady} {
		t.Run(stage, func(t *testing.T) {
			w, x, p := codexFixture(t)
			saveCodexFixture(t, w, dueCodexRecord(t, w))
			c := &codexAmbiguousClient{Client: w.Journal.Client, Stage: stage, BlockTombstone: true}
			w.Journal.Client = c
			n := syntheticCodexAccess(t, w.Clock.Now(), 2, "synthetic-account", 10*24*time.Hour)
			x.Run = func(context.Context, secretValue) (secretValue, secretValue, secretValue, error) {
				return newSecretValue(n.IDToken), newSecretValue(n.AccessToken), newSecretValue("next-refresh-synthetic-canary"), nil
			}
			if w.tick(context.Background()) == nil || x.Calls != 1 || p.Calls != 0 {
				t.Fatal("ambiguous CAS was published or ignored")
			}
			if w.tick(context.Background()) == nil || x.Calls != 1 {
				t.Fatal("halted keeper retried refresh")
			}
			// New leader reads durable state. Intent is never replayed. Ready is
			// safe because only the acknowledged material CAS can precede it, and
			// the observed future expiry prevents another refresh on leader start.
			c.BlockTombstone = false
			newLeader := &codexWorker{Journal: w.Journal, Transport: x, Publisher: p, Fence: w.Fence, Clock: w.Clock}
			err := newLeader.tick(context.Background())
			if x.Calls != 1 {
				t.Fatal("recovery repeated a POST")
			}
			if stage == codexIntent && (err == nil || p.Calls != 0) {
				t.Fatal("unconfirmed material escaped recovery")
			}
			if stage == codexReady && (err != nil || p.Calls != 1) {
				t.Fatal("confirmed replacement was needlessly refreshed")
			}
		})
	}
}

func TestCodexFreshAttemptAdoptionAndOwnerFence(t *testing.T) {
	w, x, p := codexFixture(t)
	r := w.begin(context.Background())
	if !r.OK {
		t.Fatal("fresh attempt failed")
	}
	if w.begin(context.Background()).Code != "Busy" {
		t.Fatal("overlapping login accepted")
	}
	n := syntheticCodexAccess(t, w.Clock.Now(), 1, "synthetic-account", 10*24*time.Hour)
	raw, err := codexauth.NativeJSON(n, w.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	var native map[string]any
	_ = json.Unmarshal(raw, &native)
	native["tokens"].(map[string]any)["refresh_token"] = "fresh-refresh-synthetic-canary"
	raw, _ = json.Marshal(native)
	path := filepath.Join(w.LoginDir, r.AttemptUID, "auth.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	w.Identity = "different-keeper"
	if w.adopt(context.Background(), r.AttemptUID).OK {
		t.Fatal("foreign keeper adopted ceremony")
	}
	w.Identity = "keeper-synthetic"
	if !w.adopt(context.Background(), r.AttemptUID).OK || x.Calls != 0 || p.Calls != 1 {
		t.Fatal("fresh login adoption failed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("private staging survived adoption")
	}
	d, err := w.load(context.Background())
	if err != nil || d.Record.Stage != codexReady || d.Record.Access.Generation != 1 || d.Record.Refresh.Reveal() != "fresh-refresh-synthetic-canary" {
		t.Fatal("adopted login is not private and durable")
	}
}

func TestCodexAdoptionAccountMismatchIsActionableAndPrivate(t *testing.T) {
	w, x, p := codexFixture(t)
	old := dueCodexRecord(t, w)
	saveCodexFixture(t, w, old)
	r := w.begin(context.Background())
	if !r.OK {
		t.Fatal("reservation failed")
	}
	next := syntheticCodexAccess(t, w.Clock.Now(), 2, "other-synthetic-account", 10*24*time.Hour)
	raw, err := codexauth.NativeJSON(next, w.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	var native map[string]any
	_ = json.Unmarshal(raw, &native)
	native["tokens"].(map[string]any)["refresh_token"] = "fresh-refresh-synthetic-canary"
	raw, _ = json.Marshal(native)
	if err := os.WriteFile(filepath.Join(w.LoginDir, r.AttemptUID, "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	result := w.adopt(context.Background(), r.AttemptUID)
	if result.OK || result.Code != "AccountMismatch" || x.Calls != 0 || p.Calls != 0 {
		t.Fatal("different account was adopted or lacked fixed status")
	}
	status, _ := json.Marshal(result)
	if bytes.Contains(status, []byte("synthetic-account")) || bytes.Contains(status, []byte("canary")) {
		t.Fatal("account status leaked identity or material")
	}
	if !w.cancelLogin(context.Background(), r.AttemptUID).OK {
		t.Fatal("cancel failed")
	}
	d, err := w.load(context.Background())
	if err != nil || d.Record.Stage != codexReady || !sameCodexMaterial(old, d.Record) {
		t.Fatal("account mismatch replaced the previous login")
	}
}

func TestCodexLeaseBudgetUsesEarlierDeadlineAndClockSkew(t *testing.T) {
	now := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		Name     string
		Age      time.Duration
		Holder   string
		Duration int32
		Want     bool
	}{{"fresh", 0, "ours", 15, true}, {"local-renewal-deadline", 3 * time.Second, "ours", 15, false}, {"lease-expiry", 0, "ours", 6, false}, {"foreign", 0, "other", 15, false}, {"future-skew", -2 * time.Second, "ours", 15, false}} {
		t.Run(tc.Name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := coordinationv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			renew := metav1.NewMicroTime(now.Add(-tc.Age))
			holder := tc.Holder
			duration := tc.Duration
			key := types.NamespacedName{Namespace: "system", Name: "lease"}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}, Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, RenewTime: &renew, LeaseDurationSeconds: &duration}}).Build()
			f := &LeaseFence{Reader: c, Lease: key, Identity: "ours", Clock: clocktesting.NewFakeClock(now), MaxAge: 10 * time.Second}
			if (f.CheckBudget(context.Background(), codexRequestBudget) == nil) != tc.Want {
				t.Fatal("incorrect remaining leadership budget")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if f.CheckBudget(ctx, codexRequestBudget) == nil {
				t.Fatal("cancelled leadership accepted")
			}
		})
	}
}

func TestCodexHTTPRefreshIsBoundedOneShotAndRedacted(t *testing.T) {
	for _, kind := range []string{"ok", "redirect", "echo-error", "oversized", "truncated", "nonrotating"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
					t.Error("wrong native request contract")
				}
				data, _ := io.ReadAll(req.Body)
				var body map[string]string
				_ = json.Unmarshal(data, &body)
				if len(body) != 3 || body["grant_type"] != "refresh_token" || body["client_id"] != codexClientID || body["refresh_token"] != "synthetic-input-canary" {
					t.Error("wrong refresh request")
				}
				switch kind {
				case "redirect":
					out.Header().Set("Location", "/again")
					out.WriteHeader(302)
				case "echo-error":
					out.WriteHeader(400)
					_, _ = io.WriteString(out, "synthetic-input-canary")
				case "oversized":
					_, _ = io.WriteString(out, strings.Repeat("a", codexauth.MaxBytes+1))
				case "truncated":
					_, _ = io.WriteString(out, `{"refresh_token":`)
				default:
					refresh := "synthetic-next-canary"
					if kind == "nonrotating" {
						refresh = "synthetic-input-canary"
					}
					_ = json.NewEncoder(out).Encode(map[string]string{"id_token": "synthetic-id", "access_token": "synthetic-access", "refresh_token": refresh})
				}
			}))
			defer s.Close()
			x := newCodexHTTPRefresh()
			x.endpoint = s.URL
			_, _, _, err := x.refresh(context.Background(), newSecretValue("synthetic-input-canary"))
			if calls != 1 || (err == nil) != (kind == "ok") || (err != nil && strings.Contains(err.Error(), "canary")) {
				t.Fatal("transport retried or leaked provider body")
			}
			if x.Client.Timeout != codexRequestTimeout {
				t.Fatal("request deadline drift")
			}
		})
	}
}

func TestCodexPrivateMaterialAndNativePromptAreRedacted(t *testing.T) {
	w, _, _ := codexFixture(t)
	r := dueCodexRecord(t, w)
	jsonText, _ := json.Marshal(r)
	for _, s := range []string{fmt.Sprintf("%+v", r), fmt.Sprintf("%#v", r.Refresh), string(jsonText)} {
		if strings.Contains(s, "canary") || strings.Contains(s, "synthetic-account") {
			t.Fatal("generic private formatting leaked")
		}
	}
	var out bytes.Buffer
	prompt := &codexPromptWriter{Out: &out}
	_, _ = prompt.Write([]byte("provider response: secret-synthetic-canary\nhttps://auth.openai.com/codex/device\n2. Enter this one-time code (expires in 15 minutes)\n\x1b[94mTEST-CODE\x1b[0m\nraw refresh_token secret-synthetic-canary\n"))
	if strings.Contains(out.String(), "canary") || !strings.Contains(out.String(), "TEST-CODE") || !strings.Contains(out.String(), "verificationUrl") {
		t.Fatal("challenge filter leaked diagnostics or lost challenge")
	}
	if validCodexChallenge("https://auth.openai.com/oauth/authorize?refresh_token=synthetic-canary") {
		t.Fatal("token-bearing URL accepted")
	}
}

func TestCodexPrivateControlSocketAndCancellation(t *testing.T) {
	w, x, p := codexFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := w.serveControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fi, err := os.Stat(filepath.Join(w.LoginDir, CodexControlSocket))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("control socket is not private")
	}
	r, err := CodexControl(ctx, w.LoginDir, "begin", "")
	if err != nil || !r.OK {
		t.Fatal("private begin failed")
	}
	if result, err := CodexControl(ctx, w.LoginDir, "adopt", r.AttemptUID); err != nil || result.OK || result.Code != "NeedsLogin" {
		t.Fatal("incomplete ceremony adopted")
	}
	if result, err := CodexControl(ctx, w.LoginDir, "cancel", r.AttemptUID); err != nil || !result.OK || result.Code != "Cancelled" {
		t.Fatal("private cancellation failed")
	}
	if _, err := os.Stat(filepath.Join(w.LoginDir, r.AttemptUID)); !os.IsNotExist(err) {
		t.Fatal("cancelled staging remained")
	}
	h := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(w.LoginDir, CodexControlSocket))
	}}}
	resp, err := h.Post("http://keeper/adopt", "application/json", strings.NewReader(`{"refresh_token":"synthetic-control-canary"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || bytes.Contains(raw, []byte("canary")) || !bytes.Contains(raw, []byte("InvalidRequest")) {
		t.Fatal("control echoed token payload")
	}
	if x.Calls != 0 || p.Calls != 0 {
		t.Fatal("control invoked an auth provider")
	}
}

func TestCodexHTTPRefreshCancellationIsNotRetried(t *testing.T) {
	received := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		close(received)
		<-req.Context().Done()
	}))
	defer s.Close()
	x := newCodexHTTPRefresh()
	x.endpoint = s.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, _, err := x.refresh(ctx, newSecretValue("synthetic-cancel-canary")); done <- err }()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("synthetic POST did not dispatch")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || strings.Contains(err.Error(), "canary") {
			t.Fatal("cancelled refresh succeeded or leaked")
		}
	case <-time.After(time.Second):
		t.Fatal("leadership cancellation did not stop request")
	}
}
