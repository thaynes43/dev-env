package agentrun

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestMCPGrantClaimBindingConsistency(t *testing.T) {
	for _, tc := range grantClaimBindingCases() {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			body, err := json.Marshal(tc.grant)
			if err != nil {
				t.Fatal(err)
			}
			var headers []string
			serveRaw(h, map[string]func(*http.Request) (int, string){
				apiv1.GrantPath("grant-maint"): func(r *http.Request) (int, string) {
					recordGrantRead(t, r, &headers)
					return http.StatusOK, string(body)
				},
			})
			text, isErr := mcpToolText(t, h, "grant_status", `{"name":"grant-maint","includeTaskClaims":true}`)
			if isErr || len(h.api.requests()) != 1 || headers[0] != apiv1.GrantTaskClaimsV1 {
				t.Fatalf("error %v requests %d header %v\n%s", isErr, len(h.api.requests()), headers, text)
			}
			var got apiv1.Grant
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatal(err)
			}
			assertClaimBindingCase(t, tc, got.TaskClaims)
			if got.Maintenance == nil || got.Maintenance.Coordinator != tc.grant.Maintenance.Coordinator || (tc.grant.Maintenance.TaskClaim == nil) != (got.Maintenance.TaskClaim == nil) || (tc.grant.Maintenance.TaskClaim != nil && *got.Maintenance.TaskClaim != *tc.grant.Maintenance.TaskClaim) {
				t.Fatal("maintenance envelope changed", got.Maintenance)
			}
			if got.Phase != "Pending" || got.Session != claimBindSession || got.Type != "maintenance" {
				t.Fatalf("grant identity changed: %+v", got)
			}
			if tc.clearItems {
				for _, leaked := range []string{"issue:secret", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"} {
					if strings.Contains(text, leaked) {
						t.Fatalf("leaked %s\n%s", leaked, text)
					}
				}
			}
			shown := grantShowClaims(t, h, "grant-maint")
			if !reflect.DeepEqual(got.TaskClaims, shown.TaskClaims) || len(h.api.requests()) != 2 || headers[1] != apiv1.GrantTaskClaimsV1 {
				t.Fatalf("grant show %#v mcp %#v headers %v", shown.TaskClaims, got.TaskClaims, headers)
			}
		})
	}
}
