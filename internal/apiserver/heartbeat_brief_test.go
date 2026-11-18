package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestHeartbeatBriefDeliveryCompatibility(t *testing.T) {
	for _, kind := range []string{"summoned-remote", "ordinary-remote", "summoned-task", "remote-without-prompt"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			const name = "esc-brief-1116-120000"
			tok := f.sessionPod(name, "ops", 0)
			sess := f.session(name)
			summonedTaskSession(sess)
			sess.Spec.Mode = v1alpha1.ModeRemote
			sess.Spec.Prompt = "Follow the runbook."
			switch kind {
			case "ordinary-remote":
				sess.Spec.Caller = ""
				sess.Spec.Prompt = ""
			case "summoned-task":
				sess.Spec.Mode = v1alpha1.ModeTask
			case "remote-without-prompt":
				sess.Spec.Prompt = ""
			}
			if err := f.c.Update(context.Background(), sess); err != nil {
				t.Fatal(err)
			}
			for _, raw := range []string{`"pending"`, `"launched"`, `"unconfirmed"`, `"not-launched"`, "", `"other"`, `""`, `17`, `{}`, `null`} {
				body := `{"session":"` + name + `","boot":"ready","agent":{"state":"busy"}`
				if raw != "" {
					body += `,"brief":` + raw
				}
				body += `}`
				w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, body)
				if w.Code != http.StatusOK {
					t.Fatalf("heartbeat brief %s: %d %s", raw, w.Code, w.Body.String())
				}
				stored := f.session(name)
				if stored.Status.Agent == nil || stored.Status.Agent.LastHeartbeat == nil || stored.Status.Agent.Status != "busy" {
					t.Fatal("optional evidence blocked liveness")
				}
				data, err := json.Marshal(stored.Status.Agent)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				want := ""
				if kind == "summoned-remote" && (raw == `"pending"` || raw == `"launched"` || raw == `"unconfirmed"` || raw == `"not-launched"`) {
					want = raw
				}
				if got := string(fields["brief"]); got != want {
					t.Fatalf("heartbeat %s stored brief=%q, want %q", raw, got, want)
				}
				// The view is deliberately unchanged, including for a summoned session.
				view := f.do(http.MethodGet, apiv1.SessionPath(name), tok, nil)
				if view.Code != http.StatusOK || strings.Contains(view.Body.String(), `"brief"`) {
					t.Fatalf("client view changed: %d %s", view.Code, view.Body.String())
				}
			}
		})
	}
}
