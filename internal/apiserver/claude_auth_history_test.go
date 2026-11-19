package apiserver

import (
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestClaudeAuthHistoryCodexHeartbeatOnlyWritesLiveness(t *testing.T) {
	f := newFixture(t)
	const name = "codex-fixture"
	tok := f.sessionPod(name, "full", 0)
	sess := f.session(name)
	sess.Spec.Agent = v1alpha1.AgentCodex
	if err := f.c.Update(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	st := status(name)
	st.ClaudeAuthHistory = &protocol.ClaudeAuthHistory{
		Version: 1, ObservationTiming: protocol.ClaudeAuthObservationTiming, ClockSkewUnknown: true,
	}
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, st); w.Code != http.StatusOK {
		t.Fatalf("Codex heartbeat returned HTTP %d", w.Code)
	}
	got := f.session(name).Status
	if got.Agent == nil || got.Agent.LastHeartbeat == nil || !got.Agent.LastHeartbeat.Time.Equal(f.now) ||
		got.ClaudeAuthHistory != nil || got.ClaudeAuthHistoryReportedAt != nil {
		t.Fatal("Codex heartbeat did not write liveness alone")
	}
}

func TestClaudeAuthHistoryNamedViewFiltersStoredEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		agent   v1alpha1.AgentKind
		version int32
	}{
		{"codex", v1alpha1.AgentCodex, 1},
		{"invalid", v1alpha1.AgentClaude, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			const name = "history-fixture"
			f.sessionPod(name, "full", 0)
			sess := f.session(name)
			sess.Spec.Agent = tc.agent
			if err := f.c.Update(t.Context(), sess); err != nil {
				t.Fatal(err)
			}
			sess.Status.ClaudeAuthHistory = &protocol.ClaudeAuthHistory{
				Version: tc.version, ObservationTiming: protocol.ClaudeAuthObservationTiming, ClockSkewUnknown: true,
			}
			at := metav1.NewTime(f.now)
			sess.Status.ClaudeAuthHistoryReportedAt = &at
			// The fake client lets the read exercise stored data that the
			// current heartbeat or CRD would refuse.
			if err := f.c.Status().Update(t.Context(), sess); err != nil {
				t.Fatal(err)
			}
			path := apiv1.SessionPath(name)
			w := f.do(http.MethodGet, path+"?"+apiv1.ClaudeAuthHistoryQuery+"="+apiv1.ClaudeAuthHistoryV1, tokHuman, nil)
			if w.Code != http.StatusOK || w.Header().Get(apiv1.ClaudeAuthHistoryHeader) != apiv1.ClaudeAuthHistoryV1 {
				t.Fatalf("the operator did not acknowledge the view: HTTP %d", w.Code)
			}
			if strings.Contains(w.Body.String(), "claudeAuthHistory") {
				t.Fatal("the view exposed invalid or non-Claude history")
			}
			w = f.do(http.MethodGet, path, tokHuman, nil)
			if w.Code != http.StatusOK || w.Header().Get(apiv1.ClaudeAuthHistoryHeader) != "" {
				t.Fatal("the default view carried an opt-in acknowledgement")
			}
		})
	}
}
