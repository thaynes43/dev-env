package apiserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

// The actor is an authenticated configured agent delegate, never a human
// identity. The reference identifies persistent authority, not a new Tom message.
func TestReservedPersistentDelegateAuthorityReference(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, mode := range []string{"eligible", "missing-reference", "invalid-reference", "unauthorized-actor"} {
			t.Run(map[bool]string{false: "tree/", true: "legacy/"}[legacy]+mode, func(t *testing.T) {
				f, token, g, req := reservedAPICase(t, grantattest.ClassRD2, legacy)
				req.TomRef = "authority:tom/delegation/admin-v1"
				want := http.StatusAccepted
				switch mode {
				case "missing-reference":
					req.TomRef = ""
					want = http.StatusUnprocessableEntity
				case "invalid-reference":
					req.TomRef = "not a valid reference"
					want = http.StatusUnprocessableEntity
				case "unauthorized-actor":
					token = f.sessionPod("unlisted-actor", "full", 0)
					want = http.StatusForbidden
				}
				w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), token, req)
				if w.Code != want {
					t.Fatal("persistent authority decision status", w.Code, w.Body.String())
				}
				if mode == "eligible" {
					rec, ok := f.decisionAnnotation(g.Name)
					if !ok || !strings.HasPrefix(rec.By, broker.DelegateApprover) || rec.Delegate == nil || rec.Reserved == nil || rec.Reserved.TomRef != req.TomRef {
						t.Fatal("accepted decision lost honest delegate provenance")
					}
				} else if f.grant(g.Name).Annotations[broker.MaintenanceDecisionAnnotation] != "" {
					t.Fatal("refusal consumed the decision slot")
				}
			})
		}
	}
}
