package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// All credentials, bodies and servers in this module's tests are synthetic.
// No real config path is opened, no external API/provider is contacted.
const secretCanary = "SYNTHETIC-PRIVATE-RESPONSE-MUST-NOT-LEAK"

func pv(policy, rv string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "PersistentVolume",
		"metadata": map[string]any{"name": pvName, "uid": pvUID, "resourceVersion": rv, "annotations": map[string]string{"private": secretCanary}},
		"status":   map[string]any{"phase": "Bound"},
		"spec": map[string]any{"persistentVolumeReclaimPolicy": policy,
			"claimRef": map[string]any{"namespace": claimNamespace, "name": claimName, "uid": claimUID},
			"csi":      map[string]string{"volumeHandle": secretCanary}}}
}

func sendPV(w http.ResponseWriter, policy, rv string) { _ = json.NewEncoder(w).Encode(pv(policy, rv)) }

func offline(t *testing.T, handler http.HandlerFunc) *action {
	t.Helper()
	s := httptest.NewTLSServer(handler)
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Test-only private trust root. Production always uses OS roots.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return &action{ctx: ctx, base: u, token: "synthetic-token", tlsConfig: s.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
}

func prepared(a *action) report {
	return run(a.ctx, []string{"--owner-approved", "--owner-receipt-sha256", strings.Repeat("a", 64)},
		func(context.Context) (*action, string) { return a, "" },
		func() (provenance, string) {
			return provenance{Source: strings.Repeat("b", 40), Binary: strings.Repeat("c", 64)}, ""
		})
}

func assertSanitized(t *testing.T, r report) {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil || bytes.Contains(b, []byte(secretCanary)) || bytes.Contains(b, []byte("synthetic-token")) || bytes.Contains(b, []byte("volumeHandle")) {
		t.Fatal("report leaked or failed encoding")
	}
	if len(r.Requests) > 3 || r.PatchAttempts > 1 || r.TotalResponseBytes > maxTotal+1 {
		t.Fatal("report exceeded finite request/byte budget")
	}
}

func TestDefaultRefusesBeforeAccess(t *testing.T) {
	for _, args := range [][]string{nil, {"--owner-approved"}, {"--help"}, {"--owner-approved", "--owner-receipt-sha256", "private-invalid"},
		{"--owner-approved", "--owner-receipt-sha256", strings.Repeat("a", 64), "--target", "/private"}} {
		r := run(context.Background(), args, func(context.Context) (*action, string) { t.Fatal("config accessed"); return nil, "" },
			func() (provenance, string) { t.Fatal("binary accessed"); return provenance{}, "" })
		if r.Outcome != "NotAuthorized" || len(r.Requests) != 0 || r.OwnerReceiptSHA != "" {
			t.Fatal("default did not refuse")
		}
		assertSanitized(t, r)
	}
}

func TestFixedPatchAndVerifiedAction(t *testing.T) {
	h := sha256.Sum256([]byte(patch))
	if hex.EncodeToString(h[:]) != patchSHA {
		t.Fatal("fixed payload SHA mismatch")
	}
	var ops []map[string]string
	if json.Unmarshal([]byte(patch), &ops) != nil || len(ops) != 8 {
		t.Fatal("fixed payload shape")
	}
	expectedPaths := []string{"/metadata/uid", "/metadata/resourceVersion", "/status/phase", "/spec/claimRef/namespace", "/spec/claimRef/name", "/spec/claimRef/uid", "/spec/persistentVolumeReclaimPolicy", "/spec/persistentVolumeReclaimPolicy"}
	for i, op := range ops {
		want := "test"
		if i == 7 {
			want = "replace"
		}
		if len(op) != 3 || op["op"] != want || op["path"] != expectedPaths[i] {
			t.Fatal("patch authority changed")
		}
	}
	var mu sync.Mutex
	var methods, connections []string
	var violations []string
	a := offline(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		methods = append(methods, r.Method)
		connections = append(connections, r.RemoteAddr)
		if r.URL.RequestURI() != target || r.Header.Get("Authorization") != "Bearer synthetic-token" || r.ProtoMajor != 1 {
			violations = append(violations, "authority")
		}
		if r.Method == http.MethodPatch {
			b, _ := io.ReadAll(io.LimitReader(r.Body, int64(len(patch))+1))
			if string(b) != patch || r.Header.Get("Content-Type") != "application/json-patch+json" || r.ContentLength != int64(len(patch)) {
				violations = append(violations, "payload")
			}
			sendPV(w, "Retain", "828181554")
		} else if len(methods) == 1 {
			sendPV(w, "Delete", expectedRV)
		} else {
			sendPV(w, "Retain", "828181554")
		}
	})
	r := prepared(a)
	mu.Lock()
	defer mu.Unlock()
	if r.Outcome != "AppliedVerified" || r.PatchAttempts != 1 || !reflect.DeepEqual(methods, []string{"GET", "PATCH", "GET"}) || len(violations) != 0 {
		t.Fatalf("finite action failed: %s requests=%d", r.Outcome, len(methods))
	}
	if connections[0] == connections[1] || connections[0] == connections[2] || connections[1] == connections[2] {
		t.Fatal("HTTP connection was reused")
	}
	if r.Preflight == nil || r.Postflight == nil || r.Postflight.Policy != "Retain" {
		t.Fatal("safe proof missing")
	}
	assertSanitized(t, r)
	// Even an accidental second call on this action cannot perform a request.
	second := report{}
	a.perform(&second)
	if second.Code != "AlreadyAttempted" || a.attempts != 3 {
		t.Fatal("action could be restarted")
	}
}

func TestChangedPreflightNeverPatches(t *testing.T) {
	for _, name := range []string{"uid", "rv", "name", "phase", "namespace", "claimName", "claimUID", "retained", "deletion", "missing", "null", "wrongType"} {
		t.Run(name, func(t *testing.T) {
			obj := pv("Delete", expectedRV)
			m := obj["metadata"].(map[string]any)
			s := obj["spec"].(map[string]any)
			c := s["claimRef"].(map[string]any)
			switch name {
			case "uid":
				m["uid"] = secretCanary
			case "rv":
				m["resourceVersion"] = "828181554"
			case "name":
				m["name"] = secretCanary
			case "phase":
				obj["status"].(map[string]any)["phase"] = "Released"
			case "namespace":
				c["namespace"] = secretCanary
			case "claimName":
				c["name"] = secretCanary
			case "claimUID":
				c["uid"] = secretCanary
			case "retained":
				s["persistentVolumeReclaimPolicy"] = "Retain"
			case "deletion":
				m["deletionTimestamp"] = "2026-10-13T00:00:00Z"
			case "missing":
				delete(m, "resourceVersion")
			case "null":
				s["claimRef"] = nil
			case "wrongType":
				m["uid"] = map[string]any{"private": secretCanary}
			}
			count := 0
			a := offline(t, func(w http.ResponseWriter, r *http.Request) { count++; _ = json.NewEncoder(w).Encode(obj) })
			r := prepared(a)
			if r.Outcome != "PreflightRefused" || r.PatchAttempts != 0 || count != 1 {
				t.Fatal("changed preflight allowed mutation")
			}
			assertSanitized(t, r)
		})
	}
}

func TestWriteFailureAndAmbiguityNeverReplay(t *testing.T) {
	for _, tc := range []struct {
		name, postPolicy, want string
		status                 int
		close, malformed       bool
	}{
		{"forbidden", "Delete", "DefinitiveWriteError", 403, false, false},
		{"cas-conflict", "Delete", "DefinitiveWriteError", 409, false, false},
		{"eof-delete", "Delete", "UncertainAfterWrite", 0, true, false},
		{"eof-retain", "Retain", "RetainObservedAfterAmbiguousWrite", 0, true, false},
		{"server-error-delete", "Delete", "UncertainAfterWrite", 500, false, false},
		{"server-error-retain", "Retain", "RetainObservedAfterAmbiguousWrite", 500, false, false},
		{"malformed-ack-retain", "Retain", "RetainObservedAfterAmbiguousWrite", 200, false, true},
		{"ack-then-delete", "Delete", "UncertainAfterWrite", 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			count, patches := 0, 0
			a := offline(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				count++
				if r.Method == "PATCH" {
					patches++
					if tc.close {
						c, _, e := w.(http.Hijacker).Hijack()
						if e == nil {
							_ = c.Close()
						}
						return
					}
					w.WriteHeader(tc.status)
					if tc.malformed {
						_, _ = io.WriteString(w, "{private:"+secretCanary)
						return
					}
					if tc.status != 200 {
						_ = json.NewEncoder(w).Encode(map[string]string{"message": secretCanary})
						return
					}
					sendPV(w, "Retain", "828181554")
					return
				}
				if count == 1 {
					sendPV(w, "Delete", expectedRV)
				} else {
					sendPV(w, tc.postPolicy, "828181554")
				}
			})
			r := prepared(a)
			mu.Lock()
			defer mu.Unlock()
			wantCount := 3
			if tc.want == "DefinitiveWriteError" {
				wantCount = 2
			}
			if r.Outcome != tc.want || patches != 1 || count != wantCount || r.PatchAttempts != 1 {
				t.Fatalf("outcome=%s count=%d patches=%d", r.Outcome, count, patches)
			}
			assertSanitized(t, r)
		})
	}
}

func TestPostconditionMustMatch(t *testing.T) {
	for _, name := range []string{"wrongUID", "changedClaim", "released", "deleting", "httpError"} {
		t.Run(name, func(t *testing.T) {
			count := 0
			a := offline(t, func(w http.ResponseWriter, r *http.Request) {
				count++
				if count == 1 {
					sendPV(w, "Delete", expectedRV)
					return
				}
				if count == 2 {
					sendPV(w, "Retain", "828181554")
					return
				}
				obj := pv("Retain", "828181554")
				switch name {
				case "wrongUID":
					obj["metadata"].(map[string]any)["uid"] = secretCanary
				case "changedClaim":
					obj["spec"].(map[string]any)["claimRef"].(map[string]any)["uid"] = secretCanary
				case "released":
					obj["status"].(map[string]any)["phase"] = "Released"
				case "deleting":
					obj["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-13T00:00:00Z"
				case "httpError":
					w.WriteHeader(403)
				}
				_ = json.NewEncoder(w).Encode(obj)
			})
			r := prepared(a)
			if r.Outcome != "UncertainAfterWrite" || count != 3 || r.PatchAttempts != 1 {
				t.Fatal("postcondition weakened")
			}
			assertSanitized(t, r)
		})
	}
}

func TestPatchCancellationConsumesWrite(t *testing.T) {
	var mu sync.Mutex
	patches := 0
	var cancel context.CancelFunc
	a := offline(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			mu.Lock()
			patches++
			mu.Unlock()
			cancel()
			return
		}
		sendPV(w, "Delete", expectedRV)
	})
	// The server cancels only after observing PATCH. No timing race, sleeps,
	// timeout inflation or production timeout/retry changes are needed.
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	a.ctx = ctx
	r := prepared(a)
	mu.Lock()
	defer mu.Unlock()
	if r.Outcome != "UncertainAfterWrite" || r.PatchAttempts != 1 || patches != 1 || a.attempts != 2 {
		t.Fatal("cancellation replayed or lost ambiguous write")
	}
	assertSanitized(t, r)
}

func TestOversizedPatchReplyIsAmbiguous(t *testing.T) {
	count := 0
	a := offline(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 1 {
			sendPV(w, "Delete", expectedRV)
			return
		}
		if count == 2 {
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, strings.Repeat("x", maxResponse+2))
			return
		}
		sendPV(w, "Retain", "828181554")
	})
	r := prepared(a)
	if r.Outcome != "RetainObservedAfterAmbiguousWrite" || r.PatchAttempts != 1 || count != 3 || r.Requests[1].ResponseBytes != maxResponse+1 {
		t.Fatal("oversized write acknowledgement lost ambiguity or byte bound")
	}
	assertSanitized(t, r)
}

func TestNoRedirectOrProxy(t *testing.T) {
	redirected := false
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer other.Close()
	t.Setenv("HTTPS_PROXY", other.URL)
	t.Setenv("HTTP_PROXY", other.URL)
	t.Setenv("ALL_PROXY", other.URL)
	t.Setenv("NO_PROXY", "")
	count := 0
	a := offline(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 1 {
			sendPV(w, "Delete", expectedRV)
			return
		}
		if count == 2 {
			w.Header().Set("Location", other.URL+"/private")
			w.WriteHeader(307)
			_, _ = io.WriteString(w, `{}`)
			return
		}
		sendPV(w, "Delete", expectedRV)
	})
	r := prepared(a)
	if r.Outcome != "UncertainAfterWrite" || count != 3 || redirected {
		t.Fatal("redirect/proxy or ambiguous-delete contract violated")
	}
	assertSanitized(t, r)
}

func TestResponseAndHeaderCaps(t *testing.T) {
	for _, name := range []string{"declared", "stream", "headers", "encoding", "aggregate"} {
		t.Run(name, func(t *testing.T) {
			a := offline(t, func(w http.ResponseWriter, r *http.Request) {
				switch name {
				case "declared":
					w.Header().Set("Content-Length", "524289")
				case "stream", "aggregate":
					w.(http.Flusher).Flush()
					_, _ = io.WriteString(w, strings.Repeat("x", maxResponse+2))
				case "headers":
					w.Header().Set("X-Synthetic", strings.Repeat("x", maxHeader+1))
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				}
			})
			if name == "aggregate" {
				a.total = maxTotal - 8
			}
			r := prepared(a)
			if r.Outcome != "PreflightRefused" || r.PatchAttempts != 0 || a.attempts != 1 {
				t.Fatal("caps did not refuse before patch")
			}
			if name == "stream" && r.Requests[0].ResponseBytes != maxResponse+1 {
				t.Fatal("stream sentinel accounting")
			}
			if name == "aggregate" && a.total != maxTotal+1 {
				t.Fatal("aggregate sentinel accounting")
			}
			assertSanitized(t, r)
		})
	}
}

func TestStrictJSONAndTargetTypes(t *testing.T) {
	for _, body := range [][]byte{[]byte(`{"metadata":{},"metadata":{}}`), []byte(`{} {}`), []byte(`null`), []byte(`[]`), []byte(`{"x":`), []byte("{\"x\":\"\xff\"}"), []byte(strings.Repeat(`{"x":`, 34) + `null` + strings.Repeat(`}`, 34))} {
		if _, ok := strictObject(body); ok {
			t.Fatal("malformed JSON accepted")
		}
	}
	if _, ok := validatedPV(nil); ok {
		t.Fatal("missing PV accepted")
	}
}

func TestCanceledContextCannotDispatch(t *testing.T) {
	count := 0
	a := offline(t, func(http.ResponseWriter, *http.Request) { count++ })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.ctx = ctx
	r := prepared(a)
	if r.Code != "RequestBudgetExhausted" || count != 0 || a.attempts != 0 || a.patches != 0 {
		t.Fatal("canceled request dispatched")
	}
}

// Ensure test CA substitution does not accidentally change the production TLS
// profile or make an untrusted fixture certificate acceptable.
func TestUntrustedTLSRefuses(t *testing.T) {
	count := 0
	a := offline(t, func(http.ResponseWriter, *http.Request) { count++ })
	a.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	r := prepared(a)
	if r.Outcome != "PreflightRefused" || count != 0 || a.patches != 0 {
		t.Fatal("untrusted TLS accepted")
	}
}
