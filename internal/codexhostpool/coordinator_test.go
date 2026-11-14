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
				if res.Host != "" || res.Action != ActionCreate || len(res.Skipped) != 1 || res.Skipped[0].Reason != ReasonProtected {
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
	in := baseInput(clientCaller(), []Host{runningPaired("ordinary")})
	in.ProfileExcluded = true
	res := Select(in)
	if res.Host != "" || res.Action != "" || res.Reason != ReasonProtected {
		t.Fatalf("excluded request selected: %+v", res)
	}
}
