package session

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/web/portal"
)

func fixtureIncarnation() Incarnation {
	return Incarnation{"123456789", "00000001", time.Date(2026, 10, 23, 20, 0, 0, 123000, time.UTC), "127.0.0.1", 5432}
}
func admissionDocument(i Incarnation) corev1.ConfigMap {
	return corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "dev-env-v2-portal-admission", Namespace: "dev-agents", UID: types.UID("synthetic-witness-uid"), ResourceVersion: "17"}, Data: map[string]string{
		"schema": "1", "state": "open", "generation": strings.Repeat("g", 32), "system_identifier": i.SystemIdentifier, "timeline": i.Timeline, "postmaster_started_at": i.PostmasterStartedAt.UTC().Format(time.RFC3339Nano), "server_address": i.ServerAddress, "server_port": fmt.Sprint(i.ServerPort),
	}}
}

type admissionFixture struct {
	mu                      sync.Mutex
	doc                     corev1.ConfigMap
	calls, changeAt, status int
	raw                     string
	delays                  []time.Duration
	apiURL, tokenFile       string
	ca                      []byte
}

func newAdmissionFixture(t *testing.T, i Incarnation) (*KubernetesFence, *admissionFixture) {
	t.Helper()
	perSecond, burst := witnessGetCeiling()
	return newBudgetAdmissionFixture(t, i, perSecond, burst)
}

func newBudgetAdmissionFixture(t *testing.T, i Incarnation, perSecond float64, burst int) (*KubernetesFence, *admissionFixture) {
	t.Helper()
	f := &admissionFixture{doc: admissionDocument(i), status: 200}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		f.mu.Lock()
		defer f.mu.Unlock()
		defer func() { f.delays = append(f.delays, time.Since(started)) }()
		f.calls++
		if r.Method != "GET" || r.URL.Path != admissionPath || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer SYNTHETIC-KUBE-TOKEN" {
			t.Error("admission request escaped fixed GET/no-resourceVersion/token boundary")
			w.WriteHeader(403)
			return
		}
		if f.changeAt > 0 && f.calls >= f.changeAt {
			f.doc.ResourceVersion = "18"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		if f.raw != "" {
			_, _ = w.Write([]byte(f.raw))
			return
		}
		_ = json.NewEncoder(w).Encode(f.doc)
	}))
	t.Cleanup(server.Close)
	token := filepath.Join(t.TempDir(), "token")
	if os.WriteFile(token, []byte("SYNTHETIC-KUBE-TOKEN"), 0600) != nil {
		t.Fatal("fixture token failed")
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	f.apiURL, f.ca, f.tokenFile = server.URL, ca, token
	fence, err := NewKubernetesFence(AdmissionConfig{APIURL: server.URL, CA: ca, TokenFile: token, UID: "synthetic-witness-uid", Timeout: time.Second, WitnessGetsPerSecond: perSecond, WitnessGetBurst: burst})
	if err != nil {
		t.Fatal("fixture admission config failed")
	}
	t.Cleanup(func() { fence.client.CloseIdleConnections() })
	return fence, f
}

func TestAdmissionStrictRecord(t *testing.T) {
	for _, mode := range []string{"valid", "closed", "missing", "extra", "wrong-type", "duplicate", "recreated", "deleting", "no-version", "zero-version", "bad-schema", "old-generation", "bad-system", "checkpoint-not-active", "bad-time", "bad-address", "bad-port", "trailing", "too-deep"} {
		t.Run(mode, func(t *testing.T) {
			cm := admissionDocument(fixtureIncarnation())
			switch mode {
			case "closed":
				cm.Data["state"] = "closed"
			case "missing":
				delete(cm.Data, "timeline")
			case "extra":
				cm.Data["unreviewed"] = "value"
			case "recreated":
				cm.UID = "recreated"
			case "deleting":
				now := metav1.Now()
				cm.DeletionTimestamp = &now
			case "no-version":
				cm.ResourceVersion = ""
			case "zero-version":
				cm.ResourceVersion = "0"
			case "bad-schema":
				cm.Data["schema"] = "2"
			case "old-generation":
				cm.Data["generation"] = "old"
			case "bad-system":
				cm.Data["system_identifier"] = "0123456789"
			case "checkpoint-not-active":
				cm.Data["timeline"] = "1"
			case "bad-time":
				cm.Data["postmaster_started_at"] = "2026-10-23T20:00:00+00:00"
			case "bad-address":
				cm.Data["server_address"] = "primary.example"
			case "bad-port":
				cm.Data["server_port"] = "05432"
			}
			body, _ := json.Marshal(cm)
			switch mode {
			case "wrong-type":
				body = []byte(strings.Replace(string(body), `"schema":"1"`, `"schema":1`, 1))
			case "duplicate":
				body = []byte(strings.Replace(string(body), `"state":"open"`, `"state":"closed","state":"open"`, 1))
			case "trailing":
				body = append(body, []byte(`{}`)...)
			case "too-deep":
				body = []byte(strings.Repeat("[", 20) + strings.Repeat("]", 20))
			}
			_, err := parseAdmission(body, "synthetic-witness-uid")
			if (err == nil) != (mode == "valid") {
				t.Fatal("strict witness validation mismatch")
			}
		})
	}
}

// Embedded pgx.Tx deliberately has no usable pool or alternate connection.
// The only implemented QueryRow returns the identity of THIS transaction.
type measuredTx struct {
	pgx.Tx
	identity                           Incarnation
	queryErr, commitErr                error
	recovery                           bool
	readOnly                           string
	queries, execs, commits, rollbacks int
}
type measuredRow struct{ tx *measuredTx }

func (r measuredRow) Scan(dest ...any) error {
	t := r.tx
	if t.queryErr != nil {
		return t.queryErr
	}
	*dest[0].(*string) = t.identity.SystemIdentifier
	*dest[1].(*string) = t.identity.Timeline
	*dest[2].(*time.Time) = t.identity.PostmasterStartedAt
	*dest[3].(*string) = t.identity.ServerAddress
	*dest[4].(*int) = t.identity.ServerPort
	*dest[5].(*bool) = t.recovery
	*dest[6].(*string) = t.readOnly
	return nil
}
func (t *measuredTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	t.queries++
	if sql != incarnationSQL {
		return measuredRow{&measuredTx{queryErr: errors.New("unexpected measurement SQL")}}
	}
	return measuredRow{t}
}
func (t *measuredTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	t.execs++
	return pgconn.CommandTag{}, nil
}
func (t *measuredTx) Commit(context.Context) error   { t.commits++; return t.commitErr }
func (t *measuredTx) Rollback(context.Context) error { t.rollbacks++; return nil }

func TestSameTransactionFenceBoundaries(t *testing.T) {
	for _, mode := range []string{"allow", "different-connection", "system", "timeline", "postmaster", "address", "port", "recovery", "read-only", "measurement-error", "closed-before-SQL", "change-before-commit", "change-after-commit", "uncertain-commit", "cancel-after-SQL"} {
		t.Run(mode, func(t *testing.T) {
			identity := fixtureIncarnation()
			fence, f := newAdmissionFixture(t, identity)
			tx := &measuredTx{identity: identity, readOnly: "off"}
			switch mode {
			case "different-connection", "system":
				tx.identity.SystemIdentifier = "987654321"
			case "timeline":
				tx.identity.Timeline = "00000002"
			case "postmaster":
				tx.identity.PostmasterStartedAt = identity.PostmasterStartedAt.Add(time.Second)
			case "address":
				tx.identity.ServerAddress = "127.0.0.2"
			case "port":
				tx.identity.ServerPort = 5433
			case "recovery":
				tx.recovery = true
			case "read-only":
				tx.readOnly = "on"
			case "measurement-error":
				tx.queryErr = errors.New("SECRET-SQL-CANARY")
			case "closed-before-SQL":
				f.doc.Data["state"] = "closed"
			case "change-before-commit":
				f.changeAt = 2
			case "change-after-commit":
				f.changeAt = 3
			case "uncertain-commit":
				tx.commitErr = errors.New("SECRET-COMMIT-CANARY")
			}
			s := fixtureStore(t)
			s.fence = fence
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sessionSQL := 0
			err := s.runAcquired(ctx, tx, func(_ context.Context, actual pgx.Tx) error {
				if actual != tx {
					t.Fatal("session SQL selected a different connection")
				}
				sessionSQL++
				if mode == "cancel-after-SQL" {
					cancel()
				}
				return nil
			})
			if mode == "allow" {
				if err != nil || tx.queries != 1 || tx.commits != 1 || sessionSQL != 1 || f.calls != 3 {
					t.Fatal("fresh three-GET transaction failed")
				}
			} else if !errors.Is(err, portal.ErrUnavailable) {
				t.Fatal("uncertain or mismatched transaction admitted")
			}
			if strings.Contains(fmt.Sprint(err), "SECRET") {
				t.Fatal("measurement/commit error leaked")
			}
			if (mode == "different-connection" || mode == "postmaster" || mode == "closed-before-SQL") && sessionSQL != 0 {
				t.Fatal("session SQL ran before identity admission")
			}
			if mode == "change-before-commit" && tx.commits != 0 {
				t.Fatal("changed witness committed")
			}
			if mode == "change-after-commit" && tx.commits != 1 {
				t.Fatal("postcommit fixture missed commit")
			}
			if mode == "uncertain-commit" && (tx.commits != 1 || sessionSQL != 1) {
				t.Fatal("uncertain write retried")
			}
		})
	}
}

func TestFreshAdmissionFailureAndTokenRotation(t *testing.T) {
	fence, f := newAdmissionFixture(t, fixtureIncarnation())
	snapshot, err := fence.read(context.Background())
	if err != nil {
		t.Fatal("fixture admission failed")
	}
	// No cached allow after partition, malformed response, recreation or API error.
	for _, mode := range []string{"503", "404", "403", "redirect", "malformed", "oversized", "recreated", "closed", "rotated-token"} {
		t.Run(mode, func(t *testing.T) {
			f.mu.Lock()
			f.status = 200
			f.raw = ""
			f.doc = admissionDocument(fixtureIncarnation())
			switch mode {
			case "503":
				f.status = 503
			case "404":
				f.status = 404
			case "403":
				f.status = 403
			case "redirect":
				f.status = 302
			case "malformed":
				f.raw = `{"data":null}`
			case "oversized":
				f.raw = strings.Repeat("x", maximumAdmission+1)
			case "recreated":
				f.doc.UID = "another-uid"
			case "closed":
				f.doc.Data["state"] = "closed"
			case "rotated-token":
				if os.WriteFile(fence.tokenFile, []byte(""), 0600) != nil {
					t.Fatal("fixture rotation failed")
				}
			}
			f.mu.Unlock()
			if !errors.Is(fence.Recheck(context.Background(), snapshot), portal.ErrUnavailable) {
				t.Fatal("stale allow survived failed fresh witness")
			}
		})
	}
}

func TestAdmissionTransportHasNoAmbientProxyOrUntrustedCA(t *testing.T) {
	fence, _ := newAdmissionFixture(t, fixtureIncarnation())
	transport := fence.client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig.RootCAs == nil || transport.TLSClientConfig.InsecureSkipVerify || transport.MaxConnsPerHost != 2 {
		t.Fatal("unsafe admission transport")
	}
	transport.TLSClientConfig.RootCAs = x509.NewCertPool()
	fence.client.CloseIdleConnections()
	if _, err := fence.read(context.Background()); !errors.Is(err, portal.ErrUnavailable) {
		t.Fatal("untrusted Kubernetes TLS accepted")
	}
}

func TestExactAdmissionSnapshotCannotChangeAnyField(t *testing.T) {
	for _, field := range []string{"uid", "resourceVersion", "generation", "system_identifier", "timeline", "postmaster_started_at", "server_address", "server_port"} {
		t.Run(field, func(t *testing.T) {
			fence, f := newAdmissionFixture(t, fixtureIncarnation())
			snapshot, err := fence.read(context.Background())
			if err != nil {
				t.Fatal("fixture snapshot failed")
			}
			f.mu.Lock()
			switch field {
			case "uid":
				f.doc.UID = "new-object"
			case "resourceVersion":
				f.doc.ResourceVersion = "18"
			case "generation":
				f.doc.Data[field] = strings.Repeat("h", 32)
			case "system_identifier":
				f.doc.Data[field] = "987654321"
			case "timeline":
				f.doc.Data[field] = "00000002"
			case "postmaster_started_at":
				f.doc.Data[field] = "2026-10-23T20:00:01Z"
			case "server_address":
				f.doc.Data[field] = "127.0.0.2"
			case "server_port":
				f.doc.Data[field] = "5433"
			}
			f.mu.Unlock()
			if !errors.Is(fence.Recheck(context.Background(), snapshot), portal.ErrUnavailable) {
				t.Fatal("changed admission snapshot accepted")
			}
		})
	}
}

func TestWitnessBudgetMatchesMeasuredReadAndLogin(t *testing.T) {
	perSecond, burst := witnessGetCeiling()
	if measuredGetsPerProtectedRead != 21 || measuredGetsPerLogin != 9 || perSecond != 30 || burst != 57 {
		t.Fatalf("witness budget drifted from 21/read and 9/login: rate=%v burst=%d", perSecond, burst)
	}
}

func TestWitnessBudgetRejectsUntrustedValuesWithoutHTTP(t *testing.T) {
	_, f := newAdmissionFixture(t, fixtureIncarnation())
	ceiling, burst := witnessGetCeiling()
	base := AdmissionConfig{APIURL: f.apiURL, CA: f.ca, TokenFile: f.tokenFile, UID: "synthetic-witness-uid", Timeout: time.Second, WitnessGetsPerSecond: ceiling, WitnessGetBurst: burst}
	accepted, err := NewKubernetesFence(base)
	if err != nil || accepted == nil {
		t.Fatal("derived witness ceiling rejected")
	}
	bad := []AdmissionConfig{base, base, base, base, base, base, base}
	bad[0].WitnessGetsPerSecond = 0
	bad[1].WitnessGetsPerSecond = -1
	bad[2].WitnessGetsPerSecond = math.NaN()
	bad[3].WitnessGetsPerSecond = math.Inf(1)
	bad[4].WitnessGetsPerSecond = ceiling + 1
	bad[5].WitnessGetBurst = 0
	bad[6].WitnessGetBurst = burst + 1
	for _, cfg := range bad {
		got, err := NewKubernetesFence(cfg)
		if err == nil || got != nil {
			t.Fatal("untrusted witness budget accepted")
		}
	}
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	if calls != 0 {
		t.Fatal("rejected witness budget sent HTTP")
	}
}

func TestOverBudgetWitnessGetSendsNoHTTP(t *testing.T) {
	fence, f := newBudgetAdmissionFixture(t, fixtureIncarnation(), 1.0/3600, 1)
	if _, err := fence.read(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("budgeted GET count %d", calls)
	}
	_, err := fence.read(context.Background())
	f.mu.Lock()
	calls = f.calls
	f.mu.Unlock()
	if !errors.Is(err, portal.ErrUnavailable) || calls != 1 {
		t.Fatalf("over-budget GET admitted: err=%v calls=%d", err, calls)
	}
}

func TestOverBudgetWitnessGetReturnsBeforeOccupiedSlots(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	var mu sync.Mutex
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	var closeOnce sync.Once
	unlock := func() { closeOnce.Do(func() { close(release) }) }
	t.Cleanup(unlock)
	token := filepath.Join(t.TempDir(), "token")
	if os.WriteFile(token, []byte("SYNTHETIC-KUBE-TOKEN"), 0o600) != nil {
		t.Fatal("fixture token failed")
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	fence, err := NewKubernetesFence(AdmissionConfig{APIURL: server.URL, CA: ca, TokenFile: token, UID: "synthetic-witness-uid", Timeout: 5 * time.Second, WitnessGetsPerSecond: 1.0 / 3600, WitnessGetBurst: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fence.client.CloseIdleConnections() })
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := fence.read(context.Background())
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("in-flight witness GET did not reach the fixture")
		}
	}
	over := make(chan error, 1)
	go func() {
		_, err := fence.read(context.Background())
		over <- err
	}()
	select {
	case err := <-over:
		if !errors.Is(err, portal.ErrUnavailable) {
			t.Fatal("over-budget GET was admitted while slots were occupied")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("over-budget GET waited for a slot")
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 2 {
		t.Fatalf("over-budget GET sent HTTP, calls=%d", got)
	}
	unlock()
	for range 2 {
		<-done
	}
}
