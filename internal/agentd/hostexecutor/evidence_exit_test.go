package hostexecutor

import (
	"strings"
	"testing"
	"time"
)

func TestEvidenceExitChecksWholePredicate(t *testing.T) {
	b := Binding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().UTC().Add(time.Minute)}
	baseline := strings.Repeat("a", 64)
	base := Observation{Version: 1, Binding: b, HomeUID: "home", Baseline: baseline, Stage: "Ready", Root: Identity{PID: 3}, NativeVersion: PinnedNativeVersion}
	if EvidenceExit(base, b, "home", baseline) != EvidenceReady {
		t.Fatal("ready refused")
	}
	for _, scenario := range []string{"binding", "home", "baseline", "version", "native-version", "root", "wait", "reaped", "unknown"} {
		p := base
		switch scenario {
		case "binding":
			p.Binding.Epoch++
		case "home":
			p.HomeUID = "other"
		case "baseline":
			p.Baseline = "other"
		case "version":
			p.Version++
		case "native-version":
			p.NativeVersion = "other"
		case "root":
			p.Root.PID = 0
		case "wait":
			p.RootWaitObserved = true
		case "reaped":
			p.TreeReaped = true
		case "unknown":
			p.Stage = "Unknown"
		}
		if ValidEvidenceExit(EvidenceExit(p, b, "home", baseline)) {
			t.Fatal("accepted " + scenario)
		}
	}
	for _, stage := range []struct {
		name string
		code int
	}{{"Prepared", EvidencePrepared}, {"Starting", EvidenceStarting}, {"Stopped", EvidenceStopped}} {
		p := base
		p.Stage = stage.name
		if p.Stage == "Prepared" {
			p.Root = Identity{}
		}
		if p.Stage == "Stopped" {
			p.RootWaitObserved = true
			p.TreeReaped = true
		}
		if EvidenceExit(p, b, "home", baseline) != stage.code {
			t.Fatal(stage.name)
		}
	}
}
