package controller_test

// Exercise the real HTTP create writer against the leader, not a copy of
// its charge algorithm. Request barriers interleave both CAS decisions on
// one fake API server; no goroutine timing or real sleeps are involved.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/templates"
)

type filingHTTPAuth struct{}

func (filingHTTPAuth) Authenticate(context.Context, string) (apiserver.Identity, error) {
	return apiserver.Identity{Username: "system:serviceaccount:fixture-ns:responder", Namespace: "fixture-ns", ServiceAccount: "responder"}, nil
}

type filingHTTPWorld struct {
	c   client.WithWatch
	r   *controller.Reconciler
	api *apiserver.Server
	rem *v1alpha1.AgentSession
	now time.Time
}

func newFilingHTTPWorld(t *testing.T, perHour int32) *filingHTTPWorld {
	t.Helper()
	w := &filingHTTPWorld{now: time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC)}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	at := metav1.NewTime(w.now)
	w.rem = &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "rem-http", Namespace: "dev-agents", UID: "00000000-0000-0000-0000-000000000801", CreationTimestamp: at,
		Labels: map[string]string{v1alpha1.LabelIdempotencyKey: "same-alert"}}, Spec: v1alpha1.AgentSessionSpec{
		Caller: "fixture-ns.responder", Parent: "fixture-ns/responder", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask, Lane: v1alpha1.LaneRemediation, Profile: "ops", Repo: "haynes-ops", Model: "claude-opus-5-5", Effort: "xhigh", Prompt: "test", IdempotencyKey: "same-alert",
		Summon: &v1alpha1.SessionSummon{DispatchID: "rem-request", Priority: v1alpha1.SummonPriorityUrgent, FallbackModel: "claude-opus-5", OnUnreported: v1alpha1.SummonOnUnreportedEscalated, MaxHold: "20m", Escalation: &v1alpha1.SessionSummonEscalation{Kind: v1alpha1.SummonKindRemote, Lane: v1alpha1.LaneEscalation, Repo: "haynes-ops", Profile: "ops", Model: "claude-opus-5-5", Fallback: "claude-opus-5", Effort: "xhigh", OnUnreported: v1alpha1.SummonOnUnreportedFailed}}},
		Status: v1alpha1.AgentSessionStatus{Outcome: &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated, At: &at}, Summon: &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{At: at, Source: "report", Reason: "escalate", Released: true}, Escalation: &v1alpha1.SummonEscalationStatus{State: v1alpha1.SummonEscalationPending, Name: "esc-rem-http"}}}}
	p := &v1alpha1.CallerPolicy{ObjectMeta: metav1.ObjectMeta{Name: w.rem.Spec.Caller, Namespace: summonledger.Namespace, UID: "policy-http"}, Spec: v1alpha1.CallerPolicySpec{Summon: &v1alpha1.CallerPolicySummon{ServiceAccount: w.rem.Spec.Parent,
		Limits: v1alpha1.CallerPolicySummonLimits{Concurrent: 1, Escalations: ptr.To(int32(2)), CreatesPerHour: perHour, QueuedUrgent: ptr.To(int32(4))},
		Sessions: []v1alpha1.CallerPolicySummonSession{
			{Kind: v1alpha1.SummonKindRemote, Lane: v1alpha1.LaneEscalation, Priority: v1alpha1.SummonPriorityUrgent, NamePrefixes: []string{"esc-"}, Repo: "haynes-ops", Profile: "ops", Model: v1alpha1.CallerPolicySummonModel{Default: "claude-opus-5-5", Fallback: "claude-opus-5"}, Effort: "xhigh", OnUnreported: v1alpha1.SummonOnUnreportedFailed, Brief: v1alpha1.CallerPolicySummonBrief{Template: "Fixture escalation."}},
			{Kind: v1alpha1.SummonKindTask, Lane: v1alpha1.LaneRemediation, Priority: v1alpha1.SummonPriorityUrgent, NamePrefixes: []string{"rem-"}, Repo: "haynes-ops", Profile: "ops", Model: v1alpha1.CallerPolicySummonModel{Default: "claude-opus-5-5", Fallback: "claude-opus-5"}, Effort: "xhigh", Timeout: "40m", MaxTurns: ptr.To(int32(120)), MaxHold: "20m", OnUnreported: v1alpha1.SummonOnUnreportedEscalated, Brief: v1alpha1.CallerPolicySummonBrief{Template: "Fixture remediation."}},
		}}}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: summonledger.Namespace, Name: summonledger.Name}}
	n := 0
	w.c = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}).WithObjects(w.rem, p, cm).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if s, ok := o.(*v1alpha1.AgentSession); ok {
				n++
				s.UID = types.UID(fmt.Sprintf("00000000-0000-0000-0000-%012d", 900+n))
				s.CreationTimestamp = metav1.NewTime(w.now)
				var cm corev1.ConfigMap
				if err := c.Get(ctx, client.ObjectKey{Namespace: summonledger.Namespace, Name: summonledger.Name}, &cm); err != nil {
					return err
				}
				raw, ok := cm.Data[s.Spec.Caller]
				entry, err := summonledger.Decode(raw, ok)
				if err != nil {
					return err
				}
				if !entry.Binds(s.Spec.Summon.DispatchID, s.Name) {
					return fmt.Errorf("create of %s has no committed binding", s.Name)
				}
			}
			return c.Create(ctx, o, opts...)
		},
	}).Build()
	doc, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := templates.Parse(map[string]string{templates.Key: string(doc)})
	if err != nil {
		t.Fatal(err)
	}
	w.r = &controller.Reconciler{Client: w.c, APIReader: w.c, SessionNamespace: w.rem.Namespace, SummonedLanes: true, SummonCreateKinds: []v1alpha1.SummonKind{v1alpha1.SummonKindRemote}, Now: func() time.Time { return w.now }}
	w.api = &apiserver.Server{Client: w.c, Live: w.c, Auth: filingHTTPAuth{}, Log: logr.Discard(), Policy: apiserver.Policy{SessionNamespace: w.rem.Namespace, SessionServiceAccount: controller.ServiceAccountName},
		SummonCreateKinds: []v1alpha1.SummonKind{v1alpha1.SummonKindTask, v1alpha1.SummonKindRemote}, SummonedLanes: true, Now: func() time.Time { return w.now }, Templates: func(context.Context) (*templates.Templates, error) { return tmpl, nil }}
	return w
}

func (w *filingHTTPWorld) post(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, apiv1.SessionsPath, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer fixture-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	w.api.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSummonFilingLeaderAndHTTPReplicaLastCharge(t *testing.T) {
	for _, winner := range []string{"http", "leader"} {
		t.Run(winner, func(t *testing.T) {
			w := newFilingHTTPWorld(t, 1)
			fired := false
			body := map[string]any{"name": "esc-http-caller", "mode": "remote", "lane": "escalation"}
			var response *httptest.ResponseRecorder
			barrier := func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
				if cm, ok := o.(*corev1.ConfigMap); ok && cm.Name == summonledger.Name && !fired {
					fired = true
					if winner == "http" {
						response = w.post(t, body)
					} else if err := w.r.SummonAdmissionPass(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				return c.Update(ctx, o, opts...)
			}
			if winner == "http" {
				w.r.Client = interceptor.NewClient(w.c, interceptor.Funcs{Update: barrier})
				if err := w.r.SummonAdmissionPass(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				w.api.Client = interceptor.NewClient(w.c, interceptor.Funcs{Update: barrier})
				response = w.post(t, body)
			}
			if !fired {
				t.Fatal("neither writer reached the CAS barrier")
			}
			want := http.StatusCreated
			if winner == "leader" {
				want = http.StatusTooManyRequests
			}
			if response == nil || response.Code != want {
				t.Fatalf("HTTP outcome=%+v, want %d", response, want)
			}
			var cm corev1.ConfigMap
			if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: summonledger.Namespace, Name: summonledger.Name}, &cm); err != nil {
				t.Fatal(err)
			}
			raw, ok := cm.Data[w.rem.Spec.Caller]
			entry, err := summonledger.Decode(raw, ok)
			if err != nil {
				t.Fatal(err)
			}
			var list v1alpha1.AgentSessionList
			if err := w.c.List(context.Background(), &list); err != nil {
				t.Fatal(err)
			}
			if len(entry.Charges) != 1 || len(list.Items) != 2 {
				t.Fatalf("last slot: charges=%d sessions=%d", len(entry.Charges), len(list.Items))
			}
			if (entry.Charges[0].ID == summonledger.FilingID(w.rem.UID)) != (winner == "leader") {
				t.Fatal("the losing writer charged")
			}
		})
	}
}

func TestSummonFilingRefiredRemediationKeyIgnoresOpenEscalation(t *testing.T) {
	w := newFilingHTTPWorld(t, 6)
	if err := w.r.SummonAdmissionPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := w.post(t, map[string]any{"name": w.rem.Name, "mode": "task", "lane": "remediation", "idempotencyKey": "same-alert"})
	if response.Code != http.StatusCreated {
		t.Fatalf("re-fire: %d %s", response.Code, response.Body.String())
	}
	var list v1alpha1.AgentSessionList
	if err := w.c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	filings, refires := 0, 0
	for _, s := range list.Items {
		if s.Spec.Summon == nil {
			continue
		}
		if s.Spec.Summon.DispatchID == summonledger.FilingID(w.rem.UID) {
			filings++
			if s.Spec.IdempotencyKey != "" {
				t.Fatal("filing acquired caller key")
			}
		}
		if s.Name == w.rem.Name+"-2" && s.Spec.Lane == v1alpha1.LaneRemediation && s.Spec.IdempotencyKey == "same-alert" {
			refires++
		}
	}
	if filings != 1 || refires != 1 {
		t.Fatalf("filings=%d refired remediations=%d", filings, refires)
	}
}
