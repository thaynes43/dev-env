package codexhostpool

import (
	"encoding/json"
	"testing"
	"time"
)

// evening is inside the DESIGN-063 section 3 read, after the hosts' last
// activity and before the keeper access expiry.
var evening = time.Date(2026, 11, 10, 23, 42, 0, 0, time.UTC)

func clientCaller() Caller {
	return Caller{Kind: KindClient, Name: "workbench", Profile: "workbench"}
}

func devSession() Caller {
	return Caller{Kind: KindSession, Name: "dev-session", Profile: "dev"}
}

func baseInput(c Caller, hosts []Host) Input {
	return Input{
		Hosts:             hosts,
		Projection:        Projection{Decodes: true, ExpiresAt: evening.Add(24 * time.Hour)},
		Caller:            c,
		Request:           Request{Profile: "dev", Repository: "dev-env"},
		Now:               evening,
		MaxRunningPerCell: 1,
		MaxRunning:        2,
	}
}

func runningPaired(name string) Host {
	return Host{
		Name:            name,
		UID:             "uid-" + name,
		Agent:           AgentCodex,
		Mode:            ModeRemote,
		Phase:           PhaseRunning,
		OperatingMode:   ModeRunning,
		PodReady:        true,
		ResolvedProfile: "dev",
		ReadyRepos:      []string{"dev-env"},
		SpecRepo:        "dev-env",
		RemoteState:     StatePairCodeIssued,
		ImageCurrent:    true,
		LastActivity:    evening,
	}
}

func suspendedPaired(name string, at time.Time) Host {
	h := runningPaired(name)
	h.Phase = PhaseSuspended
	h.OperatingMode = ModeSuspended
	h.PodReady = false
	h.ImageCurrent = false
	h.RescueState = RescueCleanAndPushed
	h.SuspendedAt = at
	h.SuspendedBy = "named"
	return h
}

func checkResult(t *testing.T, res Result, action, host, reason string, ready bool) {
	t.Helper()
	if res.Action != action || res.Host != host || res.Reason != reason || res.Ready != ready {
		t.Fatalf("got action=%q host=%q ready=%v reason=%q, want action=%q host=%q ready=%v reason=%q",
			res.Action, res.Host, res.Ready, res.Reason, action, host, ready, reason)
	}
}

func checkSkipped(t *testing.T, res Result, want map[string]string) {
	t.Helper()
	if len(res.Skipped) != len(want) {
		t.Fatalf("skipped %#v, want %#v", res.Skipped, want)
	}
	got := map[string]string{}
	for _, s := range res.Skipped {
		if _, ok := got[s.Name]; ok {
			t.Fatalf("duplicate skip %s", s.Name)
		}
		got[s.Name] = s.Reason
	}
	for name, reason := range want {
		if got[name] != reason {
			t.Fatalf("skip %s=%q, want %q (all %#v)", name, got[name], reason, got)
		}
	}
}

func TestSelectsRunningPairedHost(t *testing.T) {
	host := runningPaired("paired")
	res := Select(baseInput(clientCaller(), []Host{host}))
	checkResult(t, res, ActionUse, "paired", ReasonFolderTrustPending, false)
	checkSkipped(t, res, nil)

	body, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["host"] != "paired" || wire["action"] != ActionUse || wire["ready"] != false || wire["reason"] != ReasonFolderTrustPending {
		t.Fatalf("wire %#v", wire)
	}

	t.Run("pool label beats newer activity", func(t *testing.T) {
		older := runningPaired("older")
		older.Pool = true
		older.LastActivity = evening.Add(-time.Hour)
		newer := runningPaired("newer")
		newer.LastActivity = evening
		res := Select(baseInput(clientCaller(), []Host{newer, older}))
		checkResult(t, res, ActionUse, "older", ReasonFolderTrustPending, false)
		checkSkipped(t, res, map[string]string{"newer": ReasonNotChosen})
	})

	t.Run("newest activity when neither is pooled", func(t *testing.T) {
		older := runningPaired("older")
		older.LastActivity = evening.Add(-time.Hour)
		newer := runningPaired("newer")
		res := Select(baseInput(clientCaller(), []Host{older, newer}))
		checkResult(t, res, ActionUse, "newer", ReasonFolderTrustPending, false)
		checkSkipped(t, res, map[string]string{"older": ReasonNotChosen})
	})

	t.Run("equal activity breaks ties by name", func(t *testing.T) {
		a := runningPaired("b-host")
		b := runningPaired("a-host")
		res := Select(baseInput(clientCaller(), []Host{a, b}))
		checkResult(t, res, ActionUse, "a-host", ReasonFolderTrustPending, false)
	})

	t.Run("dev session uses a dev host", func(t *testing.T) {
		res := Select(baseInput(devSession(), []Host{runningPaired("paired")}))
		checkResult(t, res, ActionUse, "paired", ReasonFolderTrustPending, false)
	})

	t.Run("full session does not fit the dev host", func(t *testing.T) {
		c := devSession()
		c.Profile = "full"
		res := Select(baseInput(c, []Host{runningPaired("paired")}))
		checkResult(t, res, "", "", ReasonCallerCannotCreate, false)
		checkSkipped(t, res, map[string]string{"paired": ReasonProfileMismatch})
	})

	t.Run("dev session does not fit a full host", func(t *testing.T) {
		h := runningPaired("wide")
		h.ResolvedProfile = "full"
		res := Select(baseInput(devSession(), []Host{h}))
		checkResult(t, res, "", "", ReasonCallerCannotCreate, false)
		checkSkipped(t, res, map[string]string{"wide": ReasonProfileMismatch})
	})

	t.Run("client omitting profile does not reuse dev", func(t *testing.T) {
		in := baseInput(clientCaller(), []Host{runningPaired("paired")})
		in.Request.Profile = ""
		res := Select(in)
		checkResult(t, res, ActionCreate, "", "", false)
		checkSkipped(t, res, map[string]string{"paired": ReasonProfileMismatch})
	})

	t.Run("workbench session selects the default profile", func(t *testing.T) {
		full := runningPaired("full-host")
		full.ResolvedProfile = "full"
		dev := runningPaired("dev-host")
		c := Caller{Kind: KindSession, Name: "wb", Profile: "workbench", Workbench: true}
		in := baseInput(c, []Host{dev, full})
		in.Request.Profile = "dev"
		res := Select(in)
		checkResult(t, res, ActionUse, "full-host", ReasonFolderTrustPending, false)
		checkSkipped(t, res, map[string]string{"dev-host": ReasonProfileMismatch})
	})

	t.Run("non-codex host is not a host", func(t *testing.T) {
		other := runningPaired("claude")
		other.Agent = "claude"
		res := Select(baseInput(clientCaller(), []Host{other, runningPaired("paired")}))
		checkResult(t, res, ActionUse, "paired", ReasonFolderTrustPending, false)
		checkSkipped(t, res, map[string]string{"claude": ReasonNotCodexHost})
	})

	t.Run("hold pod is not used and fills the cell", func(t *testing.T) {
		h := runningPaired("hold")
		h.HoldPod = true
		in := baseInput(clientCaller(), []Host{h})
		in.MaxRunningPerCell = 0
		in.MaxRunning = 0
		res := Select(in)
		checkResult(t, res, ActionReport, "hold", ReasonNotRunning, false)
	})

	t.Run("deletion leaves the set", func(t *testing.T) {
		h := runningPaired("going")
		h.Deletion = true
		res := Select(baseInput(clientCaller(), []Host{h}))
		checkResult(t, res, ActionCreate, "", "", false)
		checkSkipped(t, res, map[string]string{"going": ReasonNotResumable})
	})
}

func TestSkipsBusyLease(t *testing.T) {
	t.Run("leased host is skipped for a free one", func(t *testing.T) {
		leased := runningPaired("leased")
		leased.LastActivity = evening
		leased.LeaseHolder = "someone-else"
		leased.LeaseExpiry = evening.Add(time.Hour)
		free := runningPaired("free")
		free.LastActivity = evening.Add(-time.Hour)
		res := Select(baseInput(clientCaller(), []Host{leased, free}))
		checkResult(t, res, ActionUse, "free", ReasonFolderTrustPending, false)
		checkSkipped(t, res, map[string]string{"leased": ReasonHostBusy})
	})

	t.Run("every suitable host leased does not create", func(t *testing.T) {
		leased := runningPaired("leased")
		leased.LeaseHolder = "someone-else"
		leased.LeaseExpiry = evening.Add(time.Minute)
		res := Select(baseInput(clientCaller(), []Host{leased}))
		checkResult(t, res, "", "", ReasonHostBusy, false)
		checkSkipped(t, res, map[string]string{"leased": ReasonHostBusy})
	})

	t.Run("expired lease does not block", func(t *testing.T) {
		h := runningPaired("expired")
		h.LeaseHolder = "someone-else"
		h.LeaseExpiry = evening.Add(-time.Second)
		res := Select(baseInput(clientCaller(), []Host{h}))
		checkResult(t, res, ActionUse, "expired", ReasonFolderTrustPending, false)
	})

	t.Run("this caller's lease does not block", func(t *testing.T) {
		h := runningPaired("ours")
		h.LeaseHolder = "workbench"
		h.LeaseExpiry = evening.Add(time.Hour)
		res := Select(baseInput(clientCaller(), []Host{h}))
		checkResult(t, res, ActionUse, "ours", ReasonFolderTrustPending, false)
	})
}

func TestSkipsProtected(t *testing.T) {
	for _, tc := range []struct {
		name string
		mark func(*Host)
	}{
		{"hands-off list", func(h *Host) { h.HandsOff = true }},
		{"protected annotation", func(h *Host) { h.ProtectedAnnotation = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := runningPaired("hands-off")
			tc.mark(&bad)
			good := runningPaired("open")
			res := Select(baseInput(clientCaller(), []Host{bad, good}))
			checkResult(t, res, ActionUse, "open", ReasonFolderTrustPending, false)
			checkSkipped(t, res, map[string]string{"hands-off": ReasonProtected})
		})
	}

	t.Run("protected host does not fill the cell", func(t *testing.T) {
		bad := runningPaired("hands-off")
		bad.HandsOff = true
		res := Select(baseInput(clientCaller(), []Host{bad}))
		checkResult(t, res, ActionCreate, "", "", false)
		checkSkipped(t, res, map[string]string{"hands-off": ReasonProtected})
	})
}

func TestSkipsRescueBlocked(t *testing.T) {
	for _, tc := range []struct {
		name string
		mark func(*Host)
	}{
		{"rescue failed", func(h *Host) { h.RescueState = RescueFailed }},
		{"rescue failed condition", func(h *Host) {
			h.RescueState = RescueVerified
			h.RescueFailed = true
		}},
		{"removal blocked", func(h *Host) {
			h.RescueState = RescueCleanAndPushed
			h.RemovalBlocked = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := suspendedPaired("blocked", evening.Add(-time.Hour))
			tc.mark(&bad)
			good := runningPaired("open")
			res := Select(baseInput(clientCaller(), []Host{bad, good}))
			checkResult(t, res, ActionUse, "open", ReasonFolderTrustPending, false)
			checkSkipped(t, res, map[string]string{"blocked": ReasonRescueBlocked})
		})
	}
}

func TestStaleLoginSnapshotDoesNotBlock(t *testing.T) {
	stale := suspendedPaired("stale", evening.Add(-48*time.Hour))
	stale.RemoteState = StateLoginRequired
	in := baseInput(clientCaller(), []Host{stale})
	in.Projection.ExpiresAt = evening.Add(5*time.Minute + time.Second)
	res := Select(in)
	if res.Reason == ReasonLoginRequired {
		t.Fatal("a frozen login-required snapshot blocked selection")
	}
	checkResult(t, res, ActionCreate, "", "", false)
	checkSkipped(t, res, map[string]string{"stale": ReasonPairingUnknown})

	t.Run("ordinary session cannot create", func(t *testing.T) {
		in := baseInput(devSession(), []Host{stale})
		res := Select(in)
		if res.Reason == ReasonLoginRequired || res.Action == ActionCreate || res.Action == ActionResume {
			t.Fatalf("got %+v", res)
		}
		checkResult(t, res, "", "", ReasonCallerCannotCreate, false)
		checkSkipped(t, res, map[string]string{"stale": ReasonPairingUnknown})
	})

	t.Run("human may create", func(t *testing.T) {
		c := Caller{Kind: KindHuman, Name: "tom", Profile: "full"}
		in := baseInput(c, []Host{stale})
		in.Request.Profile = "dev"
		res := Select(in)
		checkResult(t, res, ActionCreate, "", "", false)
	})

	t.Run("workbench session may create", func(t *testing.T) {
		c := Caller{Kind: KindSession, Name: "wb", Profile: "workbench", Workbench: true}
		// The cell is full, so give it a full host that is not eligible and
		// not running: the stale host retargeted at full still fails pairing.
		h := stale
		h.ResolvedProfile = "full"
		in := baseInput(c, []Host{h})
		res := Select(in)
		checkResult(t, res, ActionCreate, "", "", false)
		checkSkipped(t, res, map[string]string{"stale": ReasonPairingUnknown})
	})
}

func TestCurrentLoginFailureBlocksResumeAndCreate(t *testing.T) {
	running := runningPaired("live")
	suspended := suspendedPaired("asleep", evening.Add(-2*time.Hour))
	hosts := []Host{running, suspended}
	cases := []struct {
		name      string
		decodes   bool
		expiresIn time.Duration
	}{
		{"does not decode", false, 24 * time.Hour},
		{"expires inside five minutes", true, 4 * time.Minute},
		{"expires exactly five minutes ahead", true, 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput(clientCaller(), hosts)
			in.Projection = Projection{Decodes: tc.decodes, ExpiresAt: evening.Add(tc.expiresIn)}
			res := Select(in)
			checkResult(t, res, "", "", ReasonLoginRequired, false)
			if len(res.Skipped) != 0 {
				t.Fatalf("login failure considered hosts: %#v", res.Skipped)
			}
		})
	}
}

func TestSuspendedEligibleComesAfterRunning(t *testing.T) {
	running := runningPaired("live")
	asleep := suspendedPaired("asleep", evening.Add(-3*time.Hour))
	res := Select(baseInput(devSession(), []Host{asleep, running}))
	checkResult(t, res, ActionUse, "live", ReasonFolderTrustPending, false)
	checkSkipped(t, res, map[string]string{"asleep": ReasonNotChosen})

	t.Run("resume the one suspended longest ago", func(t *testing.T) {
		recent := suspendedPaired("recent", evening.Add(-time.Hour))
		old := suspendedPaired("old", evening.Add(-48*time.Hour))
		old.RescueState = RescueVerified
		res := Select(baseInput(devSession(), []Host{recent, old}))
		checkResult(t, res, ActionResume, "old", ReasonFolderTrustPending, false)
		checkSkipped(t, res, map[string]string{"recent": ReasonNotChosen})
	})
}

func TestPairingUnknownIsNotResumed(t *testing.T) {
	for _, state := range []string{StateLoginRequired, StateOffline, StateStarting, StateErrored, StateUnpaired, ""} {
		t.Run(state, func(t *testing.T) {
			h := suspendedPaired("unknown", evening.Add(-time.Hour))
			h.RemoteState = state
			res := Select(baseInput(clientCaller(), []Host{h}))
			if res.Action == ActionResume {
				t.Fatalf("resumed %s", state)
			}
			checkResult(t, res, ActionCreate, "", "", false)
			checkSkipped(t, res, map[string]string{"unknown": ReasonPairingUnknown})
		})
	}
}

func TestIdleSuspendedPoolHostIsResumed(t *testing.T) {
	h := suspendedPaired("idle", evening.Add(-72*time.Hour))
	h.SuspendedBy = "idle-timer"
	if h.SuspendedBy != "idle-timer" {
		t.Fatal("fixture lost who suspended the host")
	}
	res := Select(baseInput(devSession(), []Host{h}))
	checkResult(t, res, ActionResume, "idle", ReasonFolderTrustPending, false)
	checkSkipped(t, res, nil)
}

func TestSelectsHostWhoseRepoDiffersWhenRequestedRepoReady(t *testing.T) {
	h := runningPaired("other-default")
	h.SpecRepo = "haynes-ops"
	h.ReadyRepos = []string{"dev-env", "haynes-ops"}
	res := Select(baseInput(clientCaller(), []Host{h}))
	if h.SpecRepo == "dev-env" {
		t.Fatal("fixture no longer differs")
	}
	checkResult(t, res, ActionUse, "other-default", ReasonFolderTrustPending, false)
}

func TestSkipsHostWhenRequestedRepoNotReady(t *testing.T) {
	// spec.repo matches the request and the bootstrap does not. The other
	// host's spec.repo differs and the request is Ready.
	match := runningPaired("spec-matches")
	match.SpecRepo = "dev-env"
	match.ReadyRepos = []string{"haynes-ops"}
	ready := runningPaired("bootstrap-ready")
	ready.SpecRepo = "haynes-ops"
	ready.ReadyRepos = []string{"dev-env"}
	res := Select(baseInput(clientCaller(), []Host{match, ready}))
	checkResult(t, res, ActionUse, "bootstrap-ready", ReasonFolderTrustPending, false)
	checkSkipped(t, res, map[string]string{"spec-matches": ReasonRepoNotReady})
}

func TestUnpushedWorkIsNotResumed(t *testing.T) {
	cases := []struct {
		name string
		mark func(*Host)
	}{
		{"unpushed refs", func(h *Host) { h.UnpushedRefs = 67 }},
		{"omitted refs", func(h *Host) { h.OmittedRefs = 1 }},
		{"rescue missing", func(h *Host) { h.RescueState = "" }},
		{"rescue superseded", func(h *Host) { h.RescueSuperseded = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := suspendedPaired("dirty", evening.Add(-time.Hour))
			tc.mark(&h)
			res := Select(baseInput(clientCaller(), []Host{h}))
			if res.Action == ActionResume {
				t.Fatal("resumed")
			}
			checkResult(t, res, ActionCreate, "", "", false)
			checkSkipped(t, res, map[string]string{"dirty": ReasonUnpushedWork})
		})
	}
}

func TestOutdatedRunningIsNotUsed(t *testing.T) {
	h := runningPaired("old-image")
	h.ImageCurrent = false
	in := baseInput(clientCaller(), []Host{h})
	in.MaxRunningPerCell = 0
	in.MaxRunning = 0
	res := Select(in)
	if res.Action == ActionUse || res.Action == ActionCreate || res.Action == ActionResume {
		t.Fatalf("got %+v", res)
	}
	checkResult(t, res, ActionReport, "old-image", ReasonImageOutdated, false)
}

func TestAtCapacityDoesNotCreate(t *testing.T) {
	// Two running hosts in another profile fill the fleet default of 2.
	// Zero caps select the section 6 defaults.
	var hosts []Host
	for _, name := range []string{"full-a", "full-b"} {
		h := runningPaired(name)
		h.ResolvedProfile = "full"
		hosts = append(hosts, h)
	}
	in := baseInput(clientCaller(), hosts)
	in.MaxRunningPerCell = 0
	in.MaxRunning = 0
	res := Select(in)
	if res.Action == ActionCreate || res.Action == ActionResume {
		t.Fatalf("got %+v", res)
	}
	checkResult(t, res, "", "", ReasonAtCapacity, false)
	checkSkipped(t, res, map[string]string{
		"full-a": ReasonProfileMismatch,
		"full-b": ReasonProfileMismatch,
	})

	t.Run("resume would exceed the fleet cap", func(t *testing.T) {
		asleep := suspendedPaired("asleep", evening.Add(-time.Hour))
		in := baseInput(devSession(), append(hosts, asleep))
		in.MaxRunning = 2
		res := Select(in)
		if res.Action == ActionResume || res.Action == ActionCreate {
			t.Fatalf("got %+v", res)
		}
		checkResult(t, res, "", "", ReasonAtCapacity, false)
		checkSkipped(t, res, map[string]string{
			"full-a": ReasonProfileMismatch,
			"full-b": ReasonProfileMismatch,
			"asleep": ReasonAtCapacity,
		})
	})
}

func TestNamedHostIsNotReplaced(t *testing.T) {
	named := runningPaired("named")
	named.HandsOff = true
	better := runningPaired("better")
	in := baseInput(clientCaller(), []Host{named, better})
	in.Request.NamedHost = "named"
	in.Projection.Decodes = false
	res := Select(in)
	checkResult(t, res, ActionUse, "named", "", false)
	checkSkipped(t, res, nil)
	if res.Host == "better" {
		t.Fatal("replaced the named host")
	}
}

func TestFolderTrustPendingStillSelects(t *testing.T) {
	h := runningPaired("paired")
	res := Select(baseInput(clientCaller(), []Host{h}))
	checkResult(t, res, ActionUse, "paired", ReasonFolderTrustPending, false)

	t.Run("recorded trust makes the host ready", func(t *testing.T) {
		h := runningPaired("paired")
		h.TrustedRepos = []string{"dev-env"}
		res := Select(baseInput(clientCaller(), []Host{h}))
		checkResult(t, res, ActionUse, "paired", "", true)
	})

	t.Run("trust for another repo does not count", func(t *testing.T) {
		h := runningPaired("paired")
		h.TrustedRepos = []string{"haynes-ops"}
		res := Select(baseInput(clientCaller(), []Host{h}))
		checkResult(t, res, ActionUse, "paired", ReasonFolderTrustPending, false)
	})
}

func TestLostLeaseReselects(t *testing.T) {
	lost := runningPaired("lost")
	lost.Pool = true
	lost.LeaseConflict = true
	lost.LeaseHolder = "other-caller"
	lost.LeaseExpiry = evening.Add(10 * time.Minute)
	next := runningPaired("next")
	next.LastActivity = evening.Add(-time.Hour)
	res := Select(baseInput(clientCaller(), []Host{lost, next}))
	checkResult(t, res, ActionUse, "next", ReasonFolderTrustPending, false)
	checkSkipped(t, res, map[string]string{"lost": ReasonHostBusy})
}

func TestHeartbeatConflictRetriesSameHost(t *testing.T) {
	same := runningPaired("same")
	same.Pool = true
	same.LeaseConflict = true
	other := runningPaired("other")
	other.LastActivity = evening
	same.LastActivity = evening.Add(-time.Hour)
	res := Select(baseInput(clientCaller(), []Host{other, same}))
	checkResult(t, res, ActionUse, "same", ReasonFolderTrustPending, false)
	checkSkipped(t, res, map[string]string{"other": ReasonNotChosen})
}

func TestCoordinatorKindDoesNotResume(t *testing.T) {
	h := suspendedPaired("pool", evening.Add(-time.Hour))
	h.Parent = "dev-agents/someone-else"
	c := Caller{Kind: KindCoordinator, Name: "coord", Profile: "dev", ParentRef: "dev-agents/coord"}
	res := Select(baseInput(c, []Host{h}))
	if res.Action == ActionResume || res.Action == ActionCreate {
		t.Fatalf("got %+v", res)
	}
	checkResult(t, res, "", "", ReasonCallerCannotCreate, false)
	checkSkipped(t, res, map[string]string{"pool": ReasonCallerCannotResume})

	t.Run("own child is resumed", func(t *testing.T) {
		child := h
		child.Parent = c.ParentRef
		res := Select(baseInput(c, []Host{child}))
		checkResult(t, res, ActionResume, "pool", ReasonFolderTrustPending, false)
	})

	t.Run("running host is used", func(t *testing.T) {
		live := runningPaired("live")
		live.Parent = "dev-agents/someone-else"
		res := Select(baseInput(c, []Host{live}))
		checkResult(t, res, ActionUse, "live", ReasonFolderTrustPending, false)
	})
}

func TestUnsuitableRunningHostBlocksCreate(t *testing.T) {
	cases := []struct {
		state  string
		reason string
	}{
		{StateUnpaired, ReasonUnpaired},
		{StateLoginRequired, ReasonRemoteUnavailable},
		{StateOffline, ReasonRemoteUnavailable},
		{StateStarting, ReasonRemoteUnavailable},
		{StateErrored, ReasonRemoteUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			h := runningPaired("filler")
			h.RemoteState = tc.state
			in := baseInput(clientCaller(), []Host{h})
			in.MaxRunningPerCell = 0
			in.MaxRunning = 0
			res := Select(in)
			if res.Action == ActionCreate || res.Action == ActionUse || res.Action == ActionResume {
				t.Fatalf("got %+v", res)
			}
			checkResult(t, res, ActionReport, "filler", tc.reason, false)
		})
	}

	t.Run("reports the pooled filler", func(t *testing.T) {
		plain := runningPaired("plain")
		plain.RemoteState = StateUnpaired
		plain.LastActivity = evening
		pooled := runningPaired("pooled")
		pooled.RemoteState = StateUnpaired
		pooled.Pool = true
		pooled.LastActivity = evening.Add(-time.Hour)
		in := baseInput(clientCaller(), []Host{plain, pooled})
		in.MaxRunningPerCell = 1
		res := Select(in)
		checkResult(t, res, ActionReport, "pooled", ReasonUnpaired, false)
		checkSkipped(t, res, map[string]string{"plain": ReasonUnpaired})
	})
}

// TestSection3InventoryLiveCheck is the section 9 live check, fed the six
// section 3 hosts as facts. The caller is the trusted workbench client
// (kind client), which may name profile dev. Skip precedence records rescue
// failure, then the hands-off list, before profile: a protected host is not
// described as only the wrong profile. Hosts whose resolved profile is not
// dev then fail profile fit. dev-env-1110-230548 is the one that fits.
func TestSection3InventoryLiveCheck(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		repo    string
		action  string
		host    string
		reason  string
		ready   bool
		skipped map[string]string
	}{
		{
			name:    "profile dev repo dev-env",
			profile: "dev",
			repo:    "dev-env",
			action:  ActionUse,
			host:    "dev-env-1110-230548",
			reason:  ReasonFolderTrustPending,
			ready:   false,
			skipped: map[string]string{
				"haynes-ops-1109-115326": ReasonProtected,
				"dev-env-1027-010042":    ReasonProfileMismatch,
				"dev-env-1019-205125":    ReasonProfileMismatch,
				"haynes-ops-1023-041051": ReasonProfileMismatch,
				"dev-env-1020-002245":    ReasonRescueBlocked,
			},
		},
	}
	hosts := section3Hosts()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := baseInput(clientCaller(), hosts)
			in.Request.Profile = tt.profile
			in.Request.Repository = tt.repo
			in.Projection.ExpiresAt = time.Date(2026, 11, 18, 2, 26, 21, 0, time.UTC)
			in.MaxRunningPerCell = 1
			in.MaxRunning = 2
			res := Select(in)
			checkResult(t, res, tt.action, tt.host, tt.reason, tt.ready)
			checkSkipped(t, res, tt.skipped)
		})
	}
}

func section3Hosts() []Host {
	act := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			panic(err)
		}
		return ts
	}
	return []Host{
		{
			Name: "dev-env-1110-230548", UID: "inventory-1110",
			Agent: AgentCodex, Mode: ModeRemote,
			Phase: PhaseRunning, OperatingMode: ModeRunning, PodReady: true,
			ResolvedProfile: "dev", ReadyRepos: []string{"dev-env"}, SpecRepo: "dev-env",
			RemoteState: StatePairCodeIssued, ImageCurrent: true,
			LastActivity: act("2026-11-10T23:36:41Z"),
			Parent:       "dev-env-system/dev-env-human",
		},
		{
			Name: "haynes-ops-1109-115326", UID: "inventory-1109",
			Agent: AgentCodex, Mode: ModeRemote,
			Phase: PhaseRunning, OperatingMode: ModeRunning, PodReady: true,
			ResolvedProfile: "full", SpecRepo: "haynes-ops",
			HandsOff: true, RemoteState: StatePairCodeIssued, ImageCurrent: false,
			LastActivity: act("2026-11-10T23:37:17Z"),
			Parent:       "dev/dev-env",
		},
		{
			Name: "dev-env-1027-010042", UID: "inventory-1027",
			Agent: AgentCodex, Mode: ModeRemote,
			Phase: PhaseSuspended, OperatingMode: ModeSuspended,
			ResolvedProfile: "full", SpecRepo: "dev-env",
			RescueState: RescueCleanAndPushed, RemoteState: StateLoginRequired,
			SuspendedBy: "idle-timer", SuspendedAt: act("2026-11-07T21:10:07Z"),
			LastActivity: act("2026-10-27T01:05:33Z"),
			Parent:       "haynes-ops-1022-015547",
		},
		{
			Name: "dev-env-1019-205125", UID: "inventory-1019",
			Agent: AgentCodex, Mode: ModeRemote,
			Phase: PhaseSuspended, OperatingMode: ModeSuspended,
			ResolvedProfile: "full", SpecRepo: "dev-env",
			RescueState: RescueVerified, UnpushedRefs: 67, RemoteState: StateLoginRequired,
			SuspendedBy: "idle-timer", SuspendedAt: act("2026-11-07T21:12:35Z"),
			LastActivity: act("2026-10-19T20:52:06Z"),
			Parent:       "dev/dev-env",
		},
		{
			Name: "haynes-ops-1023-041051", UID: "inventory-1023",
			Agent: AgentCodex, Mode: ModeRemote,
			Phase: PhaseSuspended, OperatingMode: ModeSuspended,
			ResolvedProfile: "workbench", SpecRepo: "haynes-ops",
			RescueState: RescueVerified, UnpushedRefs: 42, RemoteState: StateLoginRequired,
			SuspendedBy: "idle-timer", SuspendedAt: act("2026-11-07T21:11:01Z"),
			LastActivity: act("2026-10-28T12:12:23Z"),
			Parent:       "dev/dev-env",
		},
		{
			Name: "dev-env-1020-002245", UID: "inventory-1020",
			Agent: AgentCodex, Mode: ModeRemote,
			Phase: PhaseSuspended, OperatingMode: ModeRunning,
			Deletion: true, HoldPod: true,
			ResolvedProfile: "full", SpecRepo: "dev-env",
			RescueState: RescueFailed, RemovalBlocked: true, RemoteState: StateOffline,
			ImageCurrent: false,
			Parent:       "haynes-ops-1019-233310",
		},
	}
}
