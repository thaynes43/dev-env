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
	for _, caller := range []Caller{{Kind: KindHuman}, clientCaller(), devSession(), {Kind: KindSession, Workbench: true}, {Kind: KindCoordinator}} {
		t.Run(caller.Kind+caller.Name, func(t *testing.T) {
			in := baseInput(caller, []Host{runningPaired("room")})
			in.Request.FreshHost = true
			if MayRequestFresh(caller) {
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
