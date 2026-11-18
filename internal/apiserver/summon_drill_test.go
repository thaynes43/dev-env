package apiserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

func TestSummonDrillCallerDerivedFromAccount(t *testing.T) {
	const account = "dev-env-drill/summon-drill"
	p := summonTestPolicy()
	p.Spec.Summon.ServiceAccount = account
	// Supply the account's policy, independently of the keeper's reserved
	// value. The real request path must derive its name and write spec.caller.
	p.Name = strings.ReplaceAll(account, "/", ".")
	f := summonFixture(t, p)
	f.auth[tokStranger] = saIdentity(account)
	w := summonPost(t, f, map[string]any{"name": "esc-drill", "mode": "remote", "lane": "escalation"})
	wantStatus(t, w, http.StatusCreated)
	s := f.session(decode[apiv1.Session](t, w).Name)
	if s.Spec.Caller != summonoutcome.DrillCaller {
		t.Fatalf("API-derived caller = %q, keeper reserves %q", s.Spec.Caller, summonoutcome.DrillCaller)
	}
}
