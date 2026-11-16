package codexhostpool

import "testing"

func TestCoordinatorPoolExclusions(t *testing.T) {
	for _, guard := range []string{"reserved-name", "workbench-account", "unknown-account"} {
		t.Run(guard, func(t *testing.T) {
			for _, suspended := range []bool{false, true} {
				h := runningPaired("reserved")
				if suspended {
					h = suspendedPaired("reserved", evening)
				}
				switch guard {
				case "reserved-name":
					h.Reserved = true
				case "workbench-account":
					h.WorkbenchAccount = true
				case "unknown-account":
					h.AccountResolved = false
				}
				in := baseInput(clientCaller(), []Host{h})
				in.MaxRunning, in.MaxRunningPerCell = 1, 1
				res := Select(in)
				if res.Host != "" || res.Action != ActionCreate || len(res.Skipped) != 1 || res.Skipped[0].Reason != map[bool]string{true: ReasonAccountUnresolved, false: ReasonProtected}[guard == "unknown-account"] {
					t.Fatalf("reserved host selected or counted: %+v", res)
				}
				if Suitable(h, in.Caller, in.ResolvedProfile, in.Request.Repository, evening) {
					t.Fatal("lease recheck admitted excluded host")
				}
				in.Request.NamedHost = h.Name
				res = Select(in)
				if res.Action == ActionUse || res.Action == ActionResume || res.Host != "" {
					t.Fatalf("explicit host crossed pool: %+v", res)
				}
			}
		})
	}
}

func TestCoordinatorPoolExcludedRequest(t *testing.T) {
	in := baseInput(clientCaller(), nil)
	in.ProfileExcluded = true
	res := Select(in)
	if res.Host != "" || res.Action != "" || res.Reason != ReasonProtected {
		t.Fatalf("excluded request selected: %+v", res)
	}
}

func TestNamedHostAbsentRefused(t *testing.T) {
	in := baseInput(clientCaller(), nil)
	in.Request.NamedHost = "absent"
	res := Select(in)
	if res.Host != "" || res.Action != "" || res.Ready {
		t.Fatalf("absent named host admitted: %+v", res)
	}
}
