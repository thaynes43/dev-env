package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

func TestClaudeAuthHistoryHeartbeatAndShow(t *testing.T) {
	now := time.Date(2026, 11, 19, 20, 0, 0, 123456789, time.UTC)
	clock := clocktesting.NewFakeClock(now)
	l := startLiveWith(t, func(s *Server) { s.Now = clock.Now })
	human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)
	task := newOutcomeTask(t, l, human)
	key := client.ObjectKey{Namespace: sessionNS, Name: task.name}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := l.k8s.Delete(ctx, task.pod, client.GracePeriodSeconds(0)); err != nil {
			t.Error(err)
		}
		var sess v1alpha1.AgentSession
		if err := l.k8s.Get(ctx, key, &sess); err != nil {
			t.Fatal(err)
		}
		controllerutil.RemoveFinalizer(&sess, controller.Finalizer)
		if err := l.k8s.Update(ctx, &sess); err != nil {
			t.Fatal(err)
		}
		if err := l.k8s.Delete(ctx, &sess); err != nil {
			t.Error(err)
		}
	})
	heartbeatPath := protocol.HeartbeatPath(task.name)
	showPath := apiv1.SessionPath(task.name)
	stored := func() v1alpha1.AgentSessionStatus {
		t.Helper()
		var sess v1alpha1.AgentSession
		if err := l.k8s.Get(context.Background(), key, &sess); err != nil {
			t.Fatal(err)
		}
		return sess.Status
	}
	beat := func(raw json.RawMessage) {
		t.Helper()
		// Preserve the actual payload size. Marshaling a RawMessage compacts JSON.
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, l.base+heartbeatPath, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+task.agent)
		resp, err := l.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("heartbeat returned HTTP %d", resp.StatusCode)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
	}
	// The old heartbeat wire fixture has no auth history key.
	legacy := json.RawMessage(`{"session":` + mustJSON(t, task.name) + `,"boot":"ready","agent":{"state":"busy"}}`)
	beat(legacy)
	if got := stored(); got.ClaudeAuthHistory != nil || got.ClaudeAuthHistoryReportedAt != nil || got.Agent.Status != "busy" {
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
	code, emptyOnce := l.show(human, task.name, "--json", "--once", "--claude-auth-history")
	if code != agentrun.ExitOK || emptyOnce != legacyOnce {
		t.Fatalf("strict CLI did not accept an acknowledged view without history: exit %d", code)
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
	reportedAt := clock.Now().Truncate(time.Second)
	if got := stored(); !apiequality.Semantic.DeepEqual(got.ClaudeAuthHistory, h) ||
		got.ClaudeAuthHistoryReportedAt == nil || !got.ClaudeAuthHistoryReportedAt.Time.Equal(reportedAt) {
		t.Fatal("heartbeat history or report time was changed or pruned by Kubernetes")
	}

	optPath := showPath + "?" + apiv1.ClaudeAuthHistoryQuery + "=" + apiv1.ClaudeAuthHistoryV1
	deadline := time.Now().Add(cacheLagBound)
	for {
		v := unmarshal[apiv1.Session](t, l.want(http.MethodGet, optPath, human, nil, http.StatusOK))
		if apiequality.Semantic.DeepEqual(v.ClaudeAuthHistory, h) &&
			v.ClaudeAuthHistoryReportedAt != nil && v.ClaudeAuthHistoryReportedAt.Equal(reportedAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("opt-in view did not show the stored history")
		}
		time.Sleep(25 * time.Millisecond)
	}
	// The response bytes and both CLI invocations are identical to the
	// legacy fixture, even though the named read can now see stored history.
	if got := l.want(http.MethodGet, showPath, human, nil, http.StatusOK); !bytes.Equal(legacyView, got) || bytes.Contains(got, []byte(`"claudeAuthHistory`)) {
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
	checkPreSliceClaudeAuthHistoryCLI(t, l, human, task.name)
	for _, flags := range [][]string{{"-o", "json"}, {"--json", "--once"}} {
		args := append([]string{task.name, "--claude-auth-history"}, flags...)
		code, out := l.show(human, args...)
		if code != agentrun.ExitOK {
			t.Fatalf("opt-in CLI arguments %v did not show the history: exit %d", flags, code)
		}
		got := unmarshal[apiv1.Session](t, []byte(out))
		if !apiequality.Semantic.DeepEqual(got.ClaudeAuthHistory, h) ||
			got.ClaudeAuthHistoryReportedAt == nil || !got.ClaudeAuthHistoryReportedAt.Equal(reportedAt) {
			t.Fatalf("opt-in CLI arguments %v did not show history and its report time", flags)
		}
	}
	for _, path := range []string{showPath + "?" + apiv1.ClaudeAuthHistoryQuery + "=unknown", apiv1.SessionsPath} {
		if got := l.want(http.MethodGet, path, human, nil, http.StatusOK); bytes.Contains(got, []byte(`"claudeAuthHistory`)) {
			t.Fatal("history appeared outside the negotiated named view")
		}
	}
	l.want(http.MethodGet, apiv1.SessionsPath+"?"+apiv1.ClaudeAuthHistoryQuery+"=v1", human, nil, http.StatusBadRequest)

	// Re-reporting the same snapshot still advances its receipt time.
	clock.Step(time.Minute)
	beat(withHistory(mustJSON(t, h)))
	reportedAt = clock.Now().Truncate(time.Second)
	if got := stored(); !apiequality.Semantic.DeepEqual(got.ClaudeAuthHistory, h) ||
		got.ClaudeAuthHistoryReportedAt == nil || !got.ClaudeAuthHistoryReportedAt.Time.Equal(reportedAt) {
		t.Fatal("unchanged snapshot did not advance the report time")
	}

	// Older and malformed reports still refresh liveness without erasing
	// evidence. A full snapshot is replaced only by a usable one.
	for i, raw := range []json.RawMessage{
		legacy,
		withHistory(`"wrong-type"`),
		withHistory(`{"version":2,"observationTiming":"assistant-transcript-proxy","clockSkewUnknown":true}`),
		withHistory(`{"version":1,"observationTiming":"assistant-transcript-proxy","clockSkewUnknown":true,"unexpected":"fixture"}`),
		withHistory(`{"version":1,"observationTiming":"assistant-transcript-proxy","clockSkewUnknown":true` + strings.Repeat(" ", protocol.ClaudeAuthHistoryMaxBytes) + `}`),
	} {
		state := []string{"idle", "waiting"}[i%2]
		clock.Step(time.Minute)
		beat(bytes.Replace(raw, []byte(`"state":"busy"`), []byte(`"state":"`+state+`"`), 1))
		got := stored()
		if !apiequality.Semantic.DeepEqual(got.ClaudeAuthHistory, h) ||
			got.ClaudeAuthHistoryReportedAt == nil || !got.ClaudeAuthHistoryReportedAt.Time.Equal(reportedAt) ||
			got.Agent.Status != state || got.Agent.LastHeartbeat == nil || !got.Agent.LastHeartbeat.Time.Equal(clock.Now().Truncate(time.Second)) {
			t.Fatalf("unusable or old heartbeat fixture %d changed history or did not refresh liveness", i)
		}
	}
}

// For the mixed-version fixture, build agent-run from pre-slice 05a6b847
// and pass its absolute path. Ordinary CI checks the unchanged response
// bytes above and needs only the source and binary under test.
func checkPreSliceClaudeAuthHistoryCLI(t *testing.T, l *live, token, name string) {
	t.Helper()
	binary := os.Getenv("DEV_ENV_TEST_LEGACY_AGENT_RUN")
	if binary == "" {
		return
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("DEV_ENV_TEST_LEGACY_AGENT_RUN must be an absolute path")
	}
	tokenFile := filepath.Join(t.TempDir(), "fixture-token")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{{"-o", "json"}, {"--json", "--once"}} {
		args := append([]string{"show", name}, flags...)
		args = append(args, "--api-url", l.base, "--token-file", tokenFile, "--ca-file", l.caFile)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		out, err := cmd.Output()
		cancel()
		if err != nil || bytes.Contains(out, []byte(`"claudeAuthHistory`)) {
			t.Fatalf("pre-slice agent-run failed %v: %v", flags, err)
		}
		if got := unmarshal[apiv1.Session](t, out); got.Name != name {
			t.Fatal("pre-slice agent-run did not decode the named Session")
		}
		t.Logf("pre-slice agent-run accepted %v", flags)
	}
}
