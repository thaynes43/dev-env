package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexhostpool"
)

// Review probes P2/P3 and mutant X2: reuse/report survive each ledger outage;
// counted paths fail with one typed reason and never repair or overwrite it.
func TestCapacityLedgerUnavailable(t *testing.T) {
	for _, state := range []string{"missing", "forbidden", "malformed"} {
		for _, action := range []string{"use", "report", "resume", "create", "direct create"} {
			t.Run(state+"/"+action, func(t *testing.T) {
				armed, writes := false, 0
				f := newCodexFixture(t, interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if armed && key.Name == codexCapacityName && state == "forbidden" {
							return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, key.Name, errors.New("denied"))
						}
						return c.Get(ctx, key, obj, opts...)
					},
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if armed {
							writes++
						}
						return c.Create(ctx, obj, opts...)
					},
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						if armed {
							writes++
						}
						return c.Update(ctx, obj, opts...)
					},
				})
				switch action {
				case "use":
					f.addCodexHost("old", codexHostOpt{})
				case "report":
					f.addCodexHost("old", codexHostOpt{notReady: true})
				case "resume":
					f.addCodexHost("old", codexHostOpt{phase: v1alpha1.PhaseSuspended, mode: v1alpha1.OperatingModeSuspended, rescue: v1alpha1.RescueCleanAndPushed})
				case "direct create":
					f.srv.CodexHosts = nil
					f.h = f.srv.Handler()
				}
				key := types.NamespacedName{Namespace: codexCapacityNamespace, Name: codexCapacityName}
				var cm corev1.ConfigMap
				if err := f.c.Get(context.Background(), key, &cm); err != nil {
					t.Fatal(err)
				}
				if state == "missing" {
					if err := f.c.Delete(context.Background(), &cm); err != nil {
						t.Fatal(err)
					}
				}
				if state == "malformed" {
					cm.Data = map[string]string{"pending": "not-json"}
					if err := f.c.Update(context.Background(), &cm); err != nil {
						t.Fatal(err)
					}
				}
				armed = true
				path := apiv1.CodexHostsSelectPath + "?check=true"
				var req any = apiv1.CodexHostSelectRequest{Repo: "dev-env", Profile: "dev", LaunchID: codexTestLaunch}
				if action == "create" || action == "direct create" {
					path = apiv1.SessionsPath
					req = capacityCreate(false)
				}
				w := f.do(http.MethodPost, path, tokClient, req)
				if action == "use" || action == "report" {
					var result codexhostpool.Result
					if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Action != action || result.Host != "old" {
						t.Fatalf("reuse/report blocked: %d %s", w.Code, w.Body.String())
					}
				} else {
					var result apiv1.ErrorResponse
					if w.Code != 503 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Error.Reason != "capacity_ledger_unavailable" || !strings.Contains(result.Error.Message, codexCapacityName) || !strings.Contains(result.Error.Message, state) {
						t.Fatalf("untyped ledger refusal: %d %s", w.Code, w.Body.String())
					}
				}
				if writes != 0 {
					t.Fatalf("ledger outage made %d writes", writes)
				}
				armed = false
				if state == "malformed" {
					var after corev1.ConfigMap
					if err := f.c.Get(context.Background(), key, &after); err != nil || after.Data["pending"] != "not-json" {
						t.Fatalf("malformed ledger overwritten: %+v %v", after.Data, err)
					}
				}
			})
		}
	}
}

// P1: name unknown outcomes in the immediate refusal, later bound refusal,
// ledger-level fence and logs. A phantom must never be offered as a host.
func TestCreateUnconfirmedIsNamed(t *testing.T) {
	f := newCodexFixture(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			return errors.New("unknown outcome")
		}
		return c.Create(ctx, obj, opts...)
	}})
	var logs strings.Builder
	f.srv.Log = funcr.New(func(prefix, args string) { logs.WriteString(prefix + args + "\n") }, funcr.Options{})
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true))
	_, pending, err := f.srv.codexCapacity(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending %+v %v", pending, err)
	}
	p := pending[0]
	assertNamed := func(raw []byte, wantWording string) {
		t.Helper()
		var refusal apiv1.ErrorResponse
		if json.Unmarshal(raw, &refusal) != nil || refusal.Error.Reason != "create_unconfirmed" || !strings.Contains(refusal.Error.Message, p.Name) || !strings.Contains(refusal.Error.Message, p.Attempt) || !strings.Contains(refusal.Error.Message, wantWording) {
			t.Fatalf("unnamed refusal: %s", raw)
		}
		assertLegacyCapacityJSON(t, raw, false)
	}
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertNamed(w.Body.Bytes(), "create response was unconfirmed")
	for _, value := range []string{p.Name, p.Attempt, "requester", "client"} {
		if !strings.Contains(logs.String(), value) {
			t.Fatalf("unknown outcome log missing %q: %s", value, logs.String())
		}
	}
	// One phantom alone used to be returned as report(repo_not_ready).
	req := apiv1.CodexHostSelectRequest{Repo: "dev-env", Profile: "dev", LaunchID: codexTestLaunch}
	w = f.do(http.MethodPost, apiv1.CodexHostsSelectPath+"?check=true", tokClient, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("bound refusal lost attempt details: %d %s", w.Code, w.Body.String())
	}
	assertNamed(w.Body.Bytes(), "pending or unconfirmed")
	// One real host plus one unknown fills the bound.
	g := newCodexFixture(t)
	g.addCodexHost("old", codexHostOpt{})
	if err := g.srv.reserveCodexCapacity(context.Background(), p, "fixture"); err != nil {
		t.Fatal(err)
	}
	w = g.do(http.MethodPost, apiv1.CodexHostsSelectPath+"?check=true", tokClient, req)
	var res codexhostpool.Result
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Host != "old" || res.Action != "use" || len(res.Skipped) != 1 || res.Skipped[0].Name != p.Name || res.Skipped[0].Reason != "create_unconfirmed" {
		t.Fatalf("phantom not safely skipped: %d %s", w.Code, w.Body.String())
	}
	assertLegacyCapacityJSON(t, w.Body.Bytes(), true)
	req.FreshHost = true
	w = g.do(http.MethodPost, apiv1.CodexHostsSelectPath+"?check=true", tokClient, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("fresh bound refusal lost attempt details: %d %s", w.Code, w.Body.String())
	}
	assertNamed(w.Body.Bytes(), "pending or unconfirmed")
	for _, off := range []bool{false, true} {
		if off {
			g.srv.CodexHosts = nil
			g.h = g.srv.Handler()
		}
		w = g.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(!off))
		if w.Code != 409 {
			t.Fatal(w.Code, w.Body.String())
		}
		assertNamed(w.Body.Bytes(), "pending or unconfirmed")
		if strings.Contains(w.Body.String(), "two retained") {
			t.Fatal("claimed a phantom exists")
		}
	}
	if err := g.srv.reserveCodexCapacity(context.Background(), codexCapacityPending{Name: "other-unknown", Profile: "dev", Attempt: "other-attempt"}, "fixture"); err != nil {
		t.Fatal(err)
	}
	err = g.srv.reserveCodexCapacity(context.Background(), codexCapacityPending{Name: "third", Profile: "dev", Attempt: "third-attempt"}, "fixture")
	var refusal *apiError
	if !errors.As(err, &refusal) {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(apiv1.ErrorResponse{Error: refusal.body})
	assertNamed(raw, "pending or unconfirmed")
}

// Frozen v2.111.0 client shapes: DisallowUnknownFields catches added keys,
// including nested skipped rows. All reason fields remain ordinary strings.
func assertLegacyCapacityJSON(t *testing.T, raw []byte, selection bool) {
	t.Helper()
	var dst any
	if selection {
		dst = &struct {
			Host     string `json:"host"`
			Action   string `json:"action"`
			Ready    bool   `json:"ready"`
			Outdated bool   `json:"outdated"`
			Reason   string `json:"reason"`
			Skipped  []struct {
				Name   string `json:"name"`
				Reason string `json:"reason"`
			} `json:"skipped"`
		}{}
	} else {
		dst = &struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Reason  string `json:"reason"`
				Fields  []struct {
					Field   string `json:"field"`
					Message string `json:"message"`
					Reason  string `json:"reason"`
				} `json:"fields"`
			} `json:"error"`
		}{}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		t.Fatalf("released client rejects response: %v", err)
	}
}

// P5/X3: every definite Kubernetes refusal releases; other error classes keep
// their reservation even when one authoritative name read returns NotFound.
func TestKubernetesCreateOutcomeClasses(t *testing.T) {
	for _, code := range []int{400, 403, 404, 405, 406, 409, 413, 415, 422, 429, 401, 408, 500, 502, 503, 504} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			f := newCodexFixture(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*v1alpha1.AgentSession); ok {
					if code == 409 {
						return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "agentsessions"}, obj.GetName())
					}
					return &apierrors.StatusError{ErrStatus: metav1.Status{Code: int32(code), Message: "test refusal"}}
				}
				return c.Create(ctx, obj, opts...)
			}})
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true))
			if w.Code == 201 {
				t.Fatal("unexpected create success")
			}
			_, pending, err := f.srv.codexCapacity(context.Background())
			want := 0
			if code == 401 || code == 408 || code >= 500 {
				want = 1
			}
			if err != nil || len(pending) != want {
				t.Fatalf("HTTP %d kept %d slots, want %d (%v): %s", code, len(pending), want, err, w.Body.String())
			}
		})
	}
}

func TestCodexCreateIgnoresClientCancellation(t *testing.T) {
	type contextKey struct{}
	var cancel context.CancelFunc
	called := false
	f := newCodexFixture(t, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.AgentSession); ok {
				called = true
				cancel()
				deadline, bounded := ctx.Deadline()
				if ctx.Err() != nil || !bounded || time.Until(deadline) > codexHostWriteTimeout || ctx.Value(contextKey{}) != "request-value" {
					t.Fatalf("create inherited cancellation or lost deadline/value: %v %v", ctx.Err(), bounded)
				}
			}
			return c.Create(ctx, obj, opts...)
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok && key.Name == codexCapacityName && ctx.Err() != nil {
				return ctx.Err()
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	ctx, cancelRequest := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "request-value"))
	cancel = cancelRequest
	defer cancelRequest()
	ctx = withCodexHostAdmission(ctx)
	c := &caller{kind: kindClient, parent: clientSA, identity: saIdentity(clientSA)}
	req := capacityCreate(true)
	sess, err := f.srv.newSession(ctx, req, c)
	if err != nil {
		t.Fatal(err)
	}
	sess.Name = "detached-create"
	if err = f.srv.createOrdinaryCodexHost(ctx, sess, c, req); err != nil || !called {
		t.Fatalf("cancelled create: %v", err)
	}
	_, pending, err := f.srv.codexCapacity(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("cancelled cleanup: %+v %v", pending, err)
	}
}

func TestLostCodexReservationResponseReleasesSlot(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "request-value"))
	defer cancel()
	var lost codexCapacityPending
	f := newCodexFixture(t, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			cm, ok := obj.(*corev1.ConfigMap)
			if !ok || cm.Name != codexCapacityName || lost.Attempt != "" {
				return c.Update(ctx, obj, opts...)
			}
			var entries []codexCapacityPending
			if err := json.Unmarshal([]byte(cm.Data["pending"]), &entries); err != nil || len(entries) != 1 {
				t.Fatalf("expected one reservation write: %+v %v", entries, err)
			}
			if err := c.Update(ctx, obj, opts...); err != nil {
				t.Fatal(err)
			}
			lost = entries[0]
			cancel()
			deadline, bounded := ctx.Deadline()
			if ctx.Err() != nil || !bounded || time.Until(deadline) > codexHostWriteTimeout || ctx.Value(contextKey{}) != "request-value" {
				t.Errorf("reservation inherited cancellation or lost deadline/value: %v %v", ctx.Err(), bounded)
			}
			return errors.New("reservation response lost")
		},
	})
	var logs strings.Builder
	f.srv.Log = funcr.New(func(prefix, args string) { logs.WriteString(prefix + args + "\n") }, funcr.Options{})
	ctx = withCodexHostAdmission(ctx)
	c := &caller{kind: kindClient, parent: clientSA, identity: saIdentity(clientSA)}
	req := capacityCreate(false)
	sess, err := f.srv.newSession(ctx, req, c)
	if err != nil {
		t.Fatal(err)
	}
	sess.Name = "lost-reservation"
	if err := f.srv.createOrdinaryCodexHost(ctx, sess, c, req); err == nil || err.Error() != "reservation response lost" {
		t.Fatalf("expected reservation response error, got %v", err)
	}
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(sess), &v1alpha1.AgentSession{}); !apierrors.IsNotFound(err) {
		t.Fatalf("reservation failure sent a session write: %v", err)
	}
	_, pending, err := f.srv.codexCapacity(context.Background())
	if err != nil || len(pending) != 0 {
		t.Errorf("lost response leaked a reservation: %+v %v", pending, err)
	}
	for _, value := range []string{"Codex host reservation outcome unknown", lost.Name, lost.Attempt, "requester", c.String()} {
		if !strings.Contains(logs.String(), value) {
			t.Errorf("reservation log missing %q: %s", value, logs.String())
		}
	}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, req); w.Code != http.StatusCreated {
		t.Fatalf("next create was refused: %d %s", w.Code, w.Body.String())
	}
}

func TestCodexReservationEnforcesProfileCapBelowFleetCap(t *testing.T) {
	f := newCodexFixture(t)
	f.srv.CodexHosts.MaxRunning = codexhostpool.MaxHostsPerProfile + 1
	ctx := context.Background()
	for _, name := range []string{"first", "second"} {
		if err := f.srv.reserveCodexCapacity(ctx, codexCapacityPending{Name: name, Profile: "dev", Attempt: name}, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	err := f.srv.reserveCodexCapacity(ctx, codexCapacityPending{Name: "third", Profile: "dev", Attempt: "third"}, "fixture")
	var refusal *apiError
	if !errors.As(err, &refusal) || refusal.body.Reason != codexhostpool.ReasonCreateUnconfirmed || !strings.Contains(refusal.body.Message, "pending or unconfirmed") {
		t.Errorf("expected pending or unconfirmed profile-cap refusal, got %v", err)
	}
	_, pending, err := f.srv.codexCapacity(ctx)
	if err != nil || len(pending) != codexhostpool.MaxHostsPerProfile {
		t.Errorf("profile reservation count changed: %+v %v", pending, err)
	}
}

func TestUnknownCreateReadsOwnAttemptOnce(t *testing.T) {
	for _, outcome := range []string{"own attempt", "different attempt", "absent", "unreadable"} {
		t.Run(outcome, func(t *testing.T) {
			created, reads := false, 0
			f := newCodexFixture(t, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if sess, ok := obj.(*v1alpha1.AgentSession); ok {
						if outcome != "absent" && outcome != "unreadable" {
							stored := sess.DeepCopy()
							stored.UID = "stored-session-uid"
							if outcome == "different attempt" {
								stored.Annotations[codexCapacityAttempt] = "different"
							}
							if err := c.Create(ctx, stored, opts...); err != nil {
								return err
							}
						}
						created = true
						return errors.New("response lost")
					}
					return c.Create(ctx, obj, opts...)
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*v1alpha1.AgentSession); ok && created {
						reads++
						if outcome == "unreadable" {
							return errors.New("read lost")
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true))
			if outcome == "own attempt" {
				var made apiv1.Session
				if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &made) != nil || made.UID != "stored-session-uid" || made.Name == "" || w.Header().Get("Location") != apiv1.SessionPath(made.Name) {
					t.Errorf("confirmed session not returned: %d %s location=%q", w.Code, w.Body.String(), w.Header().Get("Location"))
				}
			} else if w.Code != http.StatusServiceUnavailable {
				t.Fatal(w.Code, w.Body.String())
			}
			_, pending, err := f.srv.codexCapacity(context.Background())
			want := 1
			if outcome == "own attempt" {
				want = 0
			}
			if err != nil || len(pending) != want || reads != 1 {
				t.Fatalf("pending=%+v err=%v reads=%d want slots=%d", pending, err, reads, want)
			}
		})
	}
}

// P4: rescue failure/removal holds keep volumes and must fill both slots.
func TestRescueBlockedHostsKeepRetainedBound(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("rescue-failed", codexHostOpt{rescueFailed: true})
	f.addCodexHost("removal-blocked", codexHostOpt{phase: v1alpha1.PhaseSuspended, mode: v1alpha1.OperatingModeSuspended, removalBlocked: true})
	for _, fresh := range []bool{false, true} {
		w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(fresh))
		if w.Code != 409 || !strings.Contains(w.Body.String(), "at_capacity") {
			t.Fatalf("retained bound bypass: %d %s", w.Code, w.Body.String())
		}
	}
	var list v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &list, client.InNamespace(sessionNS)); err != nil || len(list.Items) != 2 {
		t.Fatalf("profile ended with %d hosts: %v", len(list.Items), err)
	}
}

// P6/X1: the default profile passes the earlier workbench profile rule. Assert
// the newSession guard itself, rather than the selector's second defense.
func TestFreshHostWorkbenchDefaultGuard(t *testing.T) {
	f := newCodexFixture(t)
	tok := workbenchSessionToken(t, f, "workbench-parent")
	req := capacityCreate(true)
	req.Profile = "" // Omission selects the default profile; workbench may not name one.
	w := f.do(http.MethodPost, apiv1.SessionsPath, tok, req)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "freshHost requires") {
		t.Fatalf("newSession guard did not refuse default-profile workbench: %d %s", w.Code, w.Body.String())
	}
	// Prove the profile is eligible when no fresh intent is requested.
	req.FreshHost = false
	if w = f.do(http.MethodPost, apiv1.SessionsPath, tok, req); w.Code != 201 {
		t.Fatalf("earlier profile rule still refuses fixture: %d %s", w.Code, w.Body.String())
	}
}

func TestCodexCapacityReleaseRetries(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovers", true: "bounded exhaustion"}[permanent], func(t *testing.T) {
			releasing, attempts := false, 0
			f := newCodexFixture(t, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					err := c.Create(ctx, obj, opts...)
					if _, ok := obj.(*v1alpha1.AgentSession); ok && err == nil {
						releasing = true
					}
					return err
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == codexCapacityName && releasing {
						attempts++
						if permanent || attempts <= codexHostLeaseTries {
							return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, cm.Name, errors.New("competing update"))
						}
					}
					return c.Update(ctx, obj, opts...)
				},
			})
			var logs strings.Builder
			f.srv.Log = funcr.New(func(prefix, args string) { logs.WriteString(prefix + args) }, funcr.Options{})
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true))
			if w.Code != 201 {
				t.Fatal(w.Code, w.Body.String())
			}
			_, pending, err := f.srv.codexCapacity(context.Background())
			want := 0
			if permanent {
				want = 1
			}
			if err != nil || len(pending) != want || attempts <= codexHostLeaseTries || attempts > codexHostLeaseTries*codexHostLeaseTries {
				t.Fatalf("release retry attempts=%d pending=%+v err=%v", attempts, pending, err)
			}
			if strings.Contains(logs.String(), "release Codex capacity reservation") != permanent {
				t.Fatalf("release exhaustion logging: %s", logs.String())
			}
		})
	}
}

func TestCapacitySelectionRefusalMessage(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("busy", codexHostOpt{lease: formatCodexHostLease(codexHostLeasePurpose, f.now.Add(time.Minute), "client/other", codexOtherLaunch)})
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(false))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "host_busy") || strings.Contains(w.Body.String(), "returned  for") {
		t.Fatalf("empty refusal fields: %d %s", w.Code, w.Body.String())
	}
}

func TestMalformedCapacityLedgerPreserved(t *testing.T) {
	for _, raw := range []string{"null", "{}", "[", `[{"name":"missing-fields"}]`, strings.Repeat("x", (64<<10)+1)} {
		t.Run(raw[:min(len(raw), 24)], func(t *testing.T) {
			f := newCodexFixture(t)
			key := types.NamespacedName{Namespace: codexCapacityNamespace, Name: codexCapacityName}
			var cm corev1.ConfigMap
			if err := f.c.Get(context.Background(), key, &cm); err != nil {
				t.Fatal(err)
			}
			cm.Data = map[string]string{"pending": raw}
			if err := f.c.Update(context.Background(), &cm); err != nil {
				t.Fatal(err)
			}
			err := f.srv.reserveCodexCapacity(context.Background(), codexCapacityPending{Name: "new", Profile: "dev", Attempt: "attempt-new"}, "fixture")
			var refusal *apiError
			if !errors.As(err, &refusal) || refusal.status != 503 || refusal.body.Reason != "capacity_ledger_unavailable" {
				t.Errorf("malformed ledger admitted: %v", err)
			}
			var after corev1.ConfigMap
			if err := f.c.Get(context.Background(), key, &after); err != nil || after.Data["pending"] != raw || after.ResourceVersion != cm.ResourceVersion {
				t.Fatalf("malformed ledger overwritten: %v", err)
			}
		})
	}
}

func TestCapacityLedgerWriteUnavailable(t *testing.T) {
	for _, state := range []string{"missing", "forbidden"} {
		t.Run(state, func(t *testing.T) {
			f := newCodexFixture(t, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == codexCapacityName {
					resource := schema.GroupResource{Resource: "configmaps"}
					if state == "missing" {
						return apierrors.NewNotFound(resource, cm.Name)
					}
					return apierrors.NewForbidden(resource, cm.Name, errors.New("update denied"))
				}
				return c.Update(ctx, obj, opts...)
			}})
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, capacityCreate(true))
			var refusal apiv1.ErrorResponse
			if w.Code != 503 || json.Unmarshal(w.Body.Bytes(), &refusal) != nil || refusal.Error.Reason != "capacity_ledger_unavailable" || !strings.Contains(refusal.Error.Message, state) || !strings.Contains(refusal.Error.Message, codexCapacityName) {
				t.Fatalf("untyped ledger write refusal: %d %s", w.Code, w.Body.String())
			}
			_, pending, err := f.srv.codexCapacity(context.Background())
			if err != nil || len(pending) != 0 {
				t.Fatalf("refusal wrote reservation: %+v %v", pending, err)
			}
			var sessions v1alpha1.AgentSessionList
			if err := f.c.List(context.Background(), &sessions, client.InNamespace(sessionNS)); err != nil || len(sessions.Items) != 0 {
				t.Fatalf("refusal created session: %+v %v", sessions.Items, err)
			}
		})
	}
}

func TestCapacityLedgerUnavailableWithoutReader(t *testing.T) {
	s := &Server{}
	_, _, err := s.codexCapacity(context.Background())
	var refusal *apiError
	if !errors.As(err, &refusal) || refusal.status != 503 || refusal.body.Reason != "capacity_ledger_unavailable" {
		t.Fatalf("untyped missing reader: %v", err)
	}
}
