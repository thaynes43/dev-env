package agentd

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestTaskBudgetDeadlineArmedBeforeWorkAndCannotReset(t *testing.T) {
	now := time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC)
	b := protocol.TaskBudgetDeadline{Version: 1, TaskUID: "campaign:step", Epoch: 1, Deadline: now.Add(time.Minute), HostID: "host", RootPodUID: "root-pod", SessionUID: "session"}
	raw, _ := json.Marshal(b)
	var callback func()
	var delay time.Duration
	stopped := false
	exitCode := 0
	after := func(d time.Duration, f func()) func() { delay = d; callback = f; return func() { stopped = true } }
	stop, err := armTaskBudgetDeadline(string(raw), "session", "worker-pod", now, after, func(c int) { exitCode = c })
	if err != nil || delay != time.Minute || callback == nil {
		t.Fatalf("deadline=%v err=%v", delay, err)
	}
	// Heartbeats and activity cannot change the armed deadline.
	callback()
	if exitCode != 124 {
		t.Fatalf("exit=%d", exitCode)
	}
	stop()
	if !stopped {
		t.Fatal("timer not stopped")
	}
	for _, tc := range []struct {
		raw, session, pod string
		now               time.Time
	}{{string(raw), "other", "worker-pod", now}, {string(raw), "session", "", now}, {string(raw), "session", "worker-pod", b.Deadline}, {string(raw) + " {}", "session", "worker-pod", now}, {`{"version":1,"verified":true}`, "session", "worker-pod", now}} {
		if _, err := armTaskBudgetDeadline(tc.raw, tc.session, tc.pod, tc.now, after, func(int) {}); err == nil {
			t.Fatal("invalid or expired binding accepted")
		}
	}
	callback = nil
	if stop, err := armTaskBudgetDeadline("", "", "", now, after, func(int) {}); err != nil || stop == nil || callback != nil {
		t.Fatal("legacy timer armed")
	}
}

func TestPrivateCampaignDeadlinePreservesEarlierLocalStopAndSharedWire(t *testing.T) {
	now := time.Now().UTC()
	b := protocol.TaskBudgetDeadline{Version: 1, TaskUID: "campaign", Epoch: 1, Deadline: now.Add(time.Minute), HostID: "root", RootPodUID: "root-pod", SessionUID: "session"}
	raw, _ := json.Marshal(b)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["campaignDeadline"]; ok {
		t.Fatal("changed legacy shared wire")
	}
	for _, delta := range []time.Duration{-time.Second, 0, time.Minute} {
		b.CampaignDeadline = b.Deadline.Add(delta)
		raw, _ := json.Marshal(b)
		got, err := protocol.ParseTaskBudgetDeadline(string(raw), "session", "private-pod", now)
		if (err == nil) != (delta >= 0) {
			t.Fatalf("campaign delta %v: %v", delta, err)
		}
		if err == nil && (!got.Deadline.Equal(b.Deadline) || !got.CampaignDeadline.Equal(b.CampaignDeadline)) {
			t.Fatal("deadline extended/rebound")
		}
	}
}
