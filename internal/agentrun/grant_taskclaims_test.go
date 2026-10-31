package agentrun

import (
	"net/http"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestGrantShowTaskClaimsNegotiation(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			h := newHarness(t)
			serveRaw(h, map[string]func(*http.Request) (int, string){apiv1.GrantPath("grant-maint"): func(r *http.Request) (int, string) {
				if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "v1" {
					t.Error("grant show did not negotiate claims")
				}
				return http.StatusOK, `{"name":"grant-maint","type":"maintenance","taskClaims":{"state":"Available","reason":"Complete","items":[{"name":"claim-one","uid":"canonical-uid","identity":{"project":"sample","repository":"dev-env","taskKey":"issue:322"}}]}}`
			}})
			args := []string{"grant", "show", "grant-maint"}
			if format == "json" {
				args = append(args, "-o", "json")
			}
			h.mustRun(ExitOK, args...)
			contains(t, "stdout", h.stdout.String(), "canonical-uid")
			if format == "text" {
				contains(t, "stdout", h.stdout.String(), "not approval", "transfer permission")
			}
		})
	}
}

func TestGrantShowOldOperatorTaskClaimsUnknown(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){apiv1.GrantPath("grant-maint"): func(r *http.Request) (int, string) {
		return http.StatusOK, `{"name":"grant-maint","type":"maintenance"}`
	}})
	h.mustRun(ExitOK, "grant", "show", "grant-maint")
	contains(t, "stdout", h.stdout.String(), "Unknown (NotReported)", "not approval")
	h.stdout.Reset()
	h.mustRun(ExitOK, "grant", "show", "grant-maint", "-o", "json")
	contains(t, "stdout", h.stdout.String(), `"reason": "NotReported"`)
}
