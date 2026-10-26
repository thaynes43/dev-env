package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

func TestPortalBootstrapCreatorNeverReplaysRetryAfterOrRedirect(t *testing.T) {
	for _, status := range []int{201, 307, 429, 503} {
		t.Run(itoa(status), func(t *testing.T) {
			calls := new(atomic.Int32)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/api/v1/namespaces/dev-agents/configmaps" || r.URL.RawQuery != "" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				var target corev1.ConfigMap
				if err := json.NewDecoder(r.Body).Decode(&target); err != nil {
					t.Error(err)
				}
				if target.Name != portalBootstrapTargetName || target.Namespace != portalBootstrapTargetNamespace || len(target.Data) != 2 || target.Data["schema"] != "1" || target.Data["state"] != "closed" {
					t.Errorf("unexpected target %#v", target)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "0")
				w.Header().Set("Location", "/unexpected-redirect")
				w.WriteHeader(status)
				if status == 201 {
					target.UID = "real-receipt-uid"
					target.ResourceVersion = "42"
					_ = json.NewEncoder(w).Encode(target)
				} else {
					_ = json.NewEncoder(w).Encode(metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonServiceUnavailable, Code: int32(status), Message: "synthetic refusal"})
				}
			}))
			defer server.Close()
			create, err := NewPortalBootstrapCreator(&rest.Config{Host: server.URL})
			mustBootstrap(t, err)
			target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: portalBootstrapTargetNamespace, Name: portalBootstrapTargetName}, Data: map[string]string{"schema": "1", "state": "closed"}}
			err = create(context.Background(), target)
			if calls.Load() != 1 || (status == 201) != (err == nil) {
				t.Fatalf("status=%d calls=%d error=%v", status, calls.Load(), err)
			}
			if status == 201 && (target.UID != "real-receipt-uid" || target.ResourceVersion != "42") {
				t.Fatalf("receipt lost: %#v", target)
			}
		})
	}
}
