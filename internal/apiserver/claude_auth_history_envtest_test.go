package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestClaudeAuthHistoryHeartbeatAndShow(t *testing.T) {
	now := time.Date(2026, 11, 19, 20, 0, 0, 123456789, time.UTC)
	l := startLiveWith(t, func(s *Server) { s.Now = func() time.Time { return now } })
	human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)
	task := newOutcomeTask(t, l, human)
	heartbeatPath := protocol.HeartbeatPath(task.name)
	showPath := apiv1.SessionPath(task.name)
	stored := func() v1alpha1.AgentSessionStatus {
		t.Helper()
		var sess v1alpha1.AgentSession
		if err := l.k8s.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: task.name}, &sess); err != nil {
			t.Fatal(err)
		}
		return sess.Status
	}
	beat := func(raw json.RawMessage) {
		t.Helper()
		l.want(http.MethodPost, heartbeatPath, task.agent, raw, http.StatusOK)
	}
	// The old heartbeat wire fixture has no auth history key.
	legacy := json.RawMessage(`{"session":` + mustJSON(t, task.name) + `,"boot":"ready","agent":{"state":"busy"}}`)
	beat(legacy)
	if got := stored(); got.ClaudeAuthHistory != nil || got.Agent.Status != "busy" {
		t.Fatal("old heartbeat did not write liveness alone")
	}
	l.waitSession(t, task.name, human, "old heartbeat", func(v apiv1.Session) bool {
		return v.AgentStatus != nil && v.AgentStatus.State == "busy"
	})
	legacyView := l.want(http.MethodGet, showPath, human, nil, http.StatusOK)
	code, legacyOnce := l.show(human, task.name, "--json", "--once")
	if code != agentrun.ExitOK {
		t.Fatalf("strict CLI rejected the response before history arrived: exit %d", code)
	}
	code, legacyOrdinary := l.show(human, task.name, "-o", "json")
	if code != agentrun.ExitOK {
		t.Fatalf("ordinary CLI rejected the response before history arrived: exit %d", code)
	}

	at := protocol.ClaudeAuthTimestamp(now.Format(time.RFC3339Nano))
	h := &protocol.ClaudeAuthHistory{
		Version: 1, ObservationTiming: protocol.ClaudeAuthObservationTiming, ClockSkewUnknown: true,
		Launch: &protocol.ClaudeAuthLaunch{BootID: "012345abcdef", CreatedAt: at, Source: "projection"},
		Generations: []protocol.ClaudeAuthGeneration{
			{AppliedGeneration: "9", ConfirmedAt: at},
			{AppliedGeneration: "10", ConfirmedAt: at, CredentialWrittenAt: at},
			{AppliedGeneration: "18446744073709551615", ConfirmedAt: at, ErrorClass: protocol.ClaudeAuthErrorClass,
				FirstRejection: &protocol.ClaudeAuthTime{At: at, PrecisionNanos: 1}, Sidechain: true, RejectionCount: 1,
				SeenRejections: []protocol.ClaudeAuthRejectionKey{{At: at, Sidechain: true, Count: 1}}},
		},
	}
	withHistory := func(raw string) json.RawMessage {
		return json.RawMessage(strings.TrimSuffix(string(legacy), "}") + `,"claudeAuthHistory":` + raw + `}`)
	}
	beat(withHistory(mustJSON(t, h)))
	if got := stored(); !apiequality.Semantic.DeepEqual(got.ClaudeAuthHistory, h) {
		t.Fatal("heartbeat history was changed or pruned by Kubernetes")
	}

	optPath := showPath + "?" + apiv1.ClaudeAuthHistoryQuery + "=" + apiv1.ClaudeAuthHistoryV1
	deadline := time.Now().Add(cacheLagBound)
	for {
		v := unmarshal[apiv1.Session](t, l.want(http.MethodGet, optPath, human, nil, http.StatusOK))
		if apiequality.Semantic.DeepEqual(v.ClaudeAuthHistory, h) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("opt-in view did not show the stored history")
		}
		time.Sleep(25 * time.Millisecond)
	}
	// The response bytes and both CLI invocations are identical to the
	// legacy fixture, even though the named read can now see stored history.
	if got := l.want(http.MethodGet, showPath, human, nil, http.StatusOK); !bytes.Equal(legacyView, got) || bytes.Contains(got, []byte(`"claudeAuthHistory"`)) {
		t.Fatal("the legacy response changed when history was stored")
	}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{task.name, "-o", "json"}, legacyOrdinary},
		{[]string{task.name, "--json", "--once"}, legacyOnce},
	} {
		if code, got := l.show(human, check.args...); code != agentrun.ExitOK || got != check.want {
			t.Fatalf("legacy CLI arguments %v changed after history arrived: exit %d", check.args[1:], code)
		}
	}
	for _, flags := range [][]string{{"-o", "json"}, {"--json", "--once"}} {
		args := append([]string{task.name, "--claude-auth-history"}, flags...)
		code, out := l.show(human, args...)
		if code != agentrun.ExitOK || !apiequality.Semantic.DeepEqual(unmarshal[apiv1.Session](t, []byte(out)).ClaudeAuthHistory, h) {
			t.Fatalf("opt-in CLI arguments %v did not show the history: exit %d", flags, code)
		}
	}
	for _, path := range []string{showPath + "?" + apiv1.ClaudeAuthHistoryQuery + "=unknown", apiv1.SessionsPath + "?" + apiv1.ClaudeAuthHistoryQuery + "=v1"} {
		if got := l.want(http.MethodGet, path, human, nil, http.StatusOK); bytes.Contains(got, []byte(`"claudeAuthHistory"`)) {
			t.Fatal("history appeared outside the negotiated named view")
		}
	}

	// Older and malformed reports still refresh liveness without erasing
	// evidence. A full snapshot is replaced only by a usable one.
	for i, raw := range []json.RawMessage{
		legacy,
		withHistory(`"wrong-type"`),
		withHistory(`{"version":1,"observationTiming":"assistant-transcript-proxy","clockSkewUnknown":true,"unexpected":"fixture"}`),
		withHistory(`{"version":1,"observationTiming":"assistant-transcript-proxy","clockSkewUnknown":true` + strings.Repeat(" ", protocol.ClaudeAuthHistoryMaxBytes) + `}`),
	} {
		state := []string{"idle", "waiting"}[i%2]
		beat(bytes.Replace(raw, []byte(`"state":"busy"`), []byte(`"state":"`+state+`"`), 1))
		got := stored()
		if !apiequality.Semantic.DeepEqual(got.ClaudeAuthHistory, h) || got.Agent.Status != state || got.Agent.LastHeartbeat == nil || !got.Agent.LastHeartbeat.Time.Equal(now.Truncate(time.Second)) {
			t.Fatal("unusable or old heartbeat lost history or did not refresh liveness")
		}
	}
}
