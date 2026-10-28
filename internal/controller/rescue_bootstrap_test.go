package controller

// A rescue that finds no clone of the session's repository, on a volume that is not
// empty. Synthetic reports only. The result stays Failed with the volume kept; only the
// reason and the message become truthful when agentd's NoCloneProof holds (DESIGN-001,
// rescue). Everything unexpected stays NotProven.

import (
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// interruptedReport is a clone-less rescue: agentd stopped the agent, found no clone, and read
// a bootstrap record that stopped on the catalog before it reached the repository.
func interruptedReport() protocol.RescueReport {
	rep := cleanReport("s1")
	rep.Repos = []protocol.RepoRescue{}
	rep.NoClone = &protocol.NoCloneProof{Version: 1, Repo: "haynes-ops", BootstrapState: protocol.BootstrapFailed, BootstrapReason: protocol.ReasonCatalogUnconfirmed}
	return rep
}

func TestInterruptedBootstrapIsNamedAndStaysFailed(t *testing.T) {
	for name, edit := range map[string]func(*protocol.RescueReport){
		"catalog unconfirmed, repository not listed": func(*protocol.RescueReport) {},
		"repository preserved at clone, stopped": func(r *protocol.RescueReport) {
			r.NoClone.BootstrapState, r.NoClone.BootstrapReason = protocol.BootstrapPartial, ""
			r.NoClone.RepositoryState, r.NoClone.RepositoryStep, r.NoClone.RepositoryReason = protocol.BootstrapPreserved, protocol.StepClone, protocol.ReasonStopped
		},
		"repository preserved, timed out": func(r *protocol.RescueReport) {
			r.NoClone.RepositoryState, r.NoClone.RepositoryReason = protocol.BootstrapPreserved, protocol.ReasonTimedOut
		},
	} {
		t.Run(name, func(t *testing.T) {
			rep := interruptedReport()
			edit(&rep)
			rec, reason := verdictFor(rep)
			if rec.Result != v1alpha1.RescueFailed || reason != "BootstrapInterrupted" {
				t.Fatalf("result %s reason %s: %s", rec.Result, reason, rec.Message)
			}
			for _, want := range []string{"interrupted bootstrap", "~/repos/haynes-ops was never made", "no Git work to rescue", "HOME preserved", "not counted as rescued", "bootstrap record:"} {
				if !strings.Contains(rec.Message, want) {
					t.Errorf("message lacks %q: %s", want, rec.Message)
				}
			}
			if strings.Contains(rec.Message, "nothing proves") {
				t.Errorf("the generic NotProven text remains: %s", rec.Message)
			}
			// The volume is still kept: not counted as rescued, so not archivable.
			s := &v1alpha1.AgentSession{}
			s.Status.Rescue = rec
			s.Status.Rescue.PodUID = "pod-1"
			if rescued(s) {
				t.Fatal("an interrupted bootstrap counts as rescued")
			}
			if len(rec.UnpushedRefs) != 0 || rec.LastBundle != "" || rec.SharedProof != nil {
				t.Errorf("the record claims preserved work: %+v", rec)
			}
		})
	}
}

// Anything that is not exactly the proven shape keeps today's NotProven.
func TestInterruptedBootstrapNeedsTheWholeProof(t *testing.T) {
	for name, edit := range map[string]func(*protocol.RescueReport){
		"no proof at all":             func(r *protocol.RescueReport) { r.NoClone = nil },
		"another repository":          func(r *protocol.RescueReport) { r.NoClone.Repo = "other" },
		"unknown version":             func(r *protocol.RescueReport) { r.NoClone.Version = 2 },
		"an entry in ~/repos":         func(r *protocol.RescueReport) { r.NoClone.ReposEntries = 1 },
		"a hidden staging directory":  func(r *protocol.RescueReport) { r.NoClone.ReposEntries = 1; r.NoClone.WorkEntries = 0 },
		"an entry in ~/work":          func(r *protocol.RescueReport) { r.NoClone.WorkEntries = 1 },
		"a negative count":            func(r *protocol.RescueReport) { r.NoClone.ReposEntries = -1 },
		"a Ready bootstrap":           func(r *protocol.RescueReport) { r.NoClone.BootstrapState = protocol.BootstrapReady },
		"an unknown bootstrap state":  func(r *protocol.RescueReport) { r.NoClone.BootstrapState = "Unknown" },
		"repository recorded Ready":   func(r *protocol.RescueReport) { r.NoClone.RepositoryState = protocol.BootstrapReady },
		"not listed and no reason":    func(r *protocol.RescueReport) { r.NoClone.BootstrapReason = "" },
		"not listed, a repo code":     func(r *protocol.RescueReport) { r.NoClone.BootstrapReason = protocol.ReasonCloneFailed },
		"not listed but has a step":   func(r *protocol.RescueReport) { r.NoClone.RepositoryStep = protocol.StepClone },
		"an unknown repository state": func(r *protocol.RescueReport) { r.NoClone.RepositoryState = "Gone" },
		"an unknown step": func(r *protocol.RescueReport) {
			r.NoClone.RepositoryState, r.NoClone.RepositoryStep = protocol.BootstrapPreserved, "Sideways"
		},
		"an unknown repository reason": func(r *protocol.RescueReport) {
			r.NoClone.RepositoryState, r.NoClone.RepositoryReason = protocol.BootstrapPreserved, "Because"
		},
		"the agent still ran": func(r *protocol.RescueReport) { r.Agent.Running = true },
		"a clone is listed": func(r *protocol.RescueReport) {
			r.Repos = []protocol.RepoRescue{{Path: "/home/dev/repos/other", Fetched: true}}
		},
		"the agent was not reported": func(r *protocol.RescueReport) { r.Agent = nil },
		"not ok":                     func(r *protocol.RescueReport) { r.OK = false },
		"not clean and pushed":       func(r *protocol.RescueReport) { r.CleanAndPushed = false },
		"a bundle was written":       func(r *protocol.RescueReport) { r.Bundle = &protocol.BundleReport{Dir: "x", Manifest: "y"} },
	} {
		t.Run(name, func(t *testing.T) {
			rep := interruptedReport()
			edit(&rep)
			rec, reason := verdictFor(rep)
			if rec.Result != v1alpha1.RescueFailed {
				t.Fatalf("result %s", rec.Result)
			}
			if reason == "BootstrapInterrupted" || strings.Contains(rec.Message, "interrupted bootstrap") {
				t.Fatalf("named an interrupted bootstrap from a broken proof: %s / %s", reason, rec.Message)
			}
		})
	}
	// The plain old shape, no proof: exactly today's verdict.
	rep := cleanReport("s1")
	rep.Repos = nil
	if rec, reason := verdictFor(rep); reason != "NotProven" || !strings.Contains(rec.Message, "clone ~/repos/haynes-ops is not in the report") {
		t.Errorf("the old NotProven verdict changed: %s %s", reason, rec.Message)
	}
}

// A volume that is truly empty keeps its own verdict; a proof does not change it.
func TestInterruptedBootstrapDoesNotTouchVolumeEmpty(t *testing.T) {
	rep := protocol.RescueReport{Session: "s1", Stamp: "20261006-1730", OK: true, CleanAndPushed: true, VolumeEmpty: true, Agent: &protocol.AgentStop{}}
	if rec, reason := verdictFor(rep); rec.Result != v1alpha1.RescueCleanAndPushed || reason != "VolumeEmpty" {
		t.Errorf("empty volume: %s %s", rec.Result, reason)
	}
}

// The operator decodes the report leniently: an older agentd's report (no noClone) still
// reads, and a newer agentd's extra fields are ignored. So the operator may ship in either
// order, but the verdict only changes where agentd is new enough to send the proof.
func TestRescueReportDecodingIsLenientAboutNoClone(t *testing.T) {
	buf := func(b string) *cappedBuffer {
		c := &cappedBuffer{max: 1 << 20}
		_, _ = c.Write([]byte(b))
		return c
	}
	old := `{"session":"s1","stamp":"20261006-1730","startedAt":"2026-10-06T17:30:00Z","finishedAt":"2026-10-06T17:30:01Z","repos":[],"ok":true,"cleanAndPushed":true}`
	rep, err := parseRescueOutput(buf(old), "", nil)
	if err != nil || rep.NoClone != nil {
		t.Fatalf("old report: %v %+v", err, rep.NoClone)
	}
	newer := `{"session":"s1","stamp":"20261006-1730","startedAt":"2026-10-06T17:30:00Z","finishedAt":"2026-10-06T17:30:01Z","repos":[],"ok":true,"cleanAndPushed":true,` +
		`"noClone":{"version":1,"repo":"haynes-ops","reposEntries":0,"workEntries":0,"bootstrapState":"Failed","bootstrapReason":"CatalogUnconfirmed","aFutureField":7},"anotherFutureField":true}`
	rep, err = parseRescueOutput(buf(newer), "", nil)
	if err != nil || rep.NoClone == nil || rep.NoClone.BootstrapReason != "CatalogUnconfirmed" {
		t.Fatalf("new report: %v %+v", err, rep.NoClone)
	}
}
