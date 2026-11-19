package codexhostpool

import (
	"testing"
	"time"
)

func memoryHost() Host {
	h := runningPaired("full")
	h.MemoryLimit, h.MemoryCurrent, h.MemoryObservedAt = 4<<30, 3400<<20, evening
	return h
}

func TestHostMemoryCapacity(t *testing.T) {
	t.Run("full leads to create with reason", func(t *testing.T) {
		res := Select(baseInput(clientCaller(), []Host{memoryHost()}))
		checkResult(t, res, ActionCreate, "", ReasonHostFull, false)
		checkSkipped(t, res, map[string]string{"full": ReasonHostFull})
	})
	t.Run("room still reuses", func(t *testing.T) {
		h := memoryHost()
		h.MemoryCurrent = 2 << 30
		checkResult(t, Select(baseInput(clientCaller(), []Host{h})), ActionUse, h.Name, "", true)
	})
	t.Run("another host with room wins", func(t *testing.T) {
		checkResult(t, Select(baseInput(clientCaller(), []Host{memoryHost(), runningPaired("room")})), ActionUse, "room", "", true)
	})
	t.Run("session cannot create beside full host", func(t *testing.T) {
		checkResult(t, Select(baseInput(devSession(), []Host{memoryHost()})), "", "", ReasonCallerCannotCreate, false)
	})
	t.Run("lease still wins", func(t *testing.T) {
		h := memoryHost()
		h.LeaseHolder = "other"
		h.LeaseExpiry = evening.Add(time.Minute)
		checkResult(t, Select(baseInput(clientCaller(), []Host{h})), "", "", ReasonHostBusy, false)
	})
	t.Run("second full host reaches fixed bound", func(t *testing.T) {
		h := memoryHost()
		h.Name = "second"
		in := baseInput(clientCaller(), []Host{memoryHost(), h})
		in.MaxRunning = 20
		in.MaxRunningPerCell = 20
		checkResult(t, Select(in), "", "", ReasonAtCapacity, false)
	})
	t.Run("fleet cap still holds", func(t *testing.T) {
		h := runningPaired("other")
		h.ResolvedProfile = "full"
		checkResult(t, Select(baseInput(clientCaller(), []Host{memoryHost(), h})), "", "", ReasonAtCapacity, false)
	})
	for _, guard := range []string{"hands-off", "protected", "reserved", "workbench"} {
		t.Run(guard, func(t *testing.T) {
			h := memoryHost()
			switch guard {
			case "hands-off":
				h.HandsOff = true
			case "protected":
				h.ProtectedAnnotation = true
			case "reserved":
				h.Reserved = true
			case "workbench":
				h.WorkbenchAccount = true
			}
			res := Select(baseInput(clientCaller(), []Host{h}))
			checkResult(t, res, ActionCreate, "", "", false)
			checkSkipped(t, res, map[string]string{h.Name: ReasonProtected})
		})
	}
}

func TestMemoryObservationBounds(t *testing.T) {
	for _, tc := range []struct {
		name           string
		limit, current int64
		at             time.Time
		reported, full bool
	}{
		{"absolute reserve", 4 << 30, 3200 << 20, evening, true, true},
		{"percentage reserve", 16 << 30, 14 << 30, evening, true, true},
		{"exact absolute boundary", 4 << 30, 3 << 30, evening, true, false},
		{"exact percentage boundary", 10 << 30, 8 << 30, evening, true, false},
		{"over limit", 4 << 30, 5 << 30, evening, true, true},
		{"unknown limit", 0, 0, evening, true, false},
		{"negative", 4 << 30, -1, evening, true, false},
		{"missing time", 4 << 30, 4 << 30, time.Time{}, true, false},
		{"stale", 4 << 30, 4 << 30, evening.Add(-MemoryMaxAge - time.Second), true, false},
		{"future", 4 << 30, 4 << 30, evening.Add(time.Second), true, false},
		{"old pod", 4 << 30, 4 << 30, evening, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := memoryHost()
			h.MemoryLimit = tc.limit
			h.MemoryCurrent = tc.current
			h.MemoryObservedAt = tc.at
			h.Reported = tc.reported
			if got := MemoryFull(h, evening); got != tc.full {
				t.Fatalf("full=%v want %v", got, tc.full)
			}
		})
	}
}

func TestFreshHostIntent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caller  Caller
		allowed bool
	}{
		{"human", Caller{Kind: KindHuman}, true},
		{"trusted client", clientCaller(), true},
		{"ordinary session", devSession(), false},
		{"workbench session", Caller{Kind: KindSession, Workbench: true}, false},
		{"coordinator", Caller{Kind: KindCoordinator}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput(tc.caller, []Host{runningPaired("room")})
			in.Request.FreshHost = true
			if tc.allowed {
				checkResult(t, Select(in), ActionCreate, "", ReasonFreshHost, false)
			} else {
				checkResult(t, Select(in), "", "", ReasonFreshHostDenied, false)
			}
		})
	}
	in := baseInput(clientCaller(), []Host{runningPaired("one"), suspendedPaired("two", evening)})
	in.Request.FreshHost = true
	in.MaxRunning = 20
	in.MaxRunningPerCell = 20
	checkResult(t, Select(in), "", "", ReasonAtCapacity, false)
	in.Hosts = []Host{runningPaired("one")}
	in.Login = LoginLapsed
	checkResult(t, Select(in), "", "", ReasonLoginRequired, false)
}

func TestRetainedOrdinaryVolumeHolds(t *testing.T) {
	for _, state := range []string{"rescue failed", "removal blocked", "unresolved account", "reserved", "workbench", "hands-off", "protected", "archived"} {
		t.Run(state, func(t *testing.T) {
			h := runningPaired("retained")
			want := 1
			switch state {
			case "rescue failed":
				h.RescueFailed = true
			case "removal blocked":
				h.RemovalBlocked = true
			case "unresolved account":
				h.AccountResolved = false
			case "reserved":
				h.Reserved = true
				want = 0
			case "workbench":
				h.WorkbenchAccount = true
				want = 0
			case "hands-off":
				h.HandsOff = true
				want = 0
			case "protected":
				h.ProtectedAnnotation = true
				want = 0
			case "archived":
				h.Archived = true
				want = 0
			}
			if got := RetainedInProfile([]Host{h}, h.ResolvedProfile); got != want {
				t.Fatalf("retained=%d want %d", got, want)
			}
			if got := RetainedInProfile([]Host{h}, "other-profile"); got != 0 {
				t.Fatalf("wrong profile count=%d", got)
			}
		})
	}
}

func TestUnconfirmedRowsAreOnlySkipped(t *testing.T) {
	phantom := runningPaired("unknown")
	phantom.ReservationAttempt = "attempt-unknown"
	// Even a fully populated phantom must never become use/resume/report.
	for _, phase := range []string{PhaseRunning, PhaseSuspended} {
		phantom.Phase = phase
		in := baseInput(clientCaller(), []Host{phantom})
		res := Select(in)
		if res.Host != "" {
			t.Fatalf("phantom offered: %+v", res)
		}
		checkSkipped(t, res, map[string]string{"unknown": ReasonCreateUnconfirmed})
		in.Request.NamedHost = phantom.Name
		res = Select(in)
		if res.Host != "" || res.Action != "" || res.Reason != ReasonCreateUnconfirmed {
			t.Fatalf("named phantom offered: %+v", res)
		}
	}
}
