package summonledger

import (
	"slices"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The request hash is the one internal/apiserver computed before story 800
// moved it here: these three values were printed by the API's
// summonRequestHash at dev-env main 42552fa343ac24a62db1072313f44823da561f73.
// A charge already in a ledger carries such a hash, so a change would refuse
// a retry of every charge in flight.
func TestRequestHashPinned(t *testing.T) {
	for _, tc := range []struct {
		mode, lane, name string
		priority         v1alpha1.SummonPriority
		want             string
	}{
		{"task", "remediation", "rem-responder-1", v1alpha1.SummonPriorityUrgent, "9957bb8ac42fd4e3fa8369c2ad15a55928626d5b12425b1288b171ff11962d9c"},
		{"remote", "escalation", "esc-1", v1alpha1.SummonPriorityUrgent, "953a4c82c383188d99edbd4252a685e1309ff4052d8cb7e0a4fe743ef2bc7ee7"},
		{"remote", "upgrade", "wo-1", v1alpha1.SummonPriorityNormal, "8d1f437052b884f2f0e78b82139444fb8c58ebb163d327ec77ef33b0489037fd"},
	} {
		if got := RequestHash(tc.mode, tc.lane, tc.name, tc.priority); got != tc.want {
			t.Errorf("RequestHash(%q, %q, %q, %q) = %s, want %s", tc.mode, tc.lane, tc.name, tc.priority, got, tc.want)
		}
	}
	// Each input is its own field: moving a byte between two changes the hash.
	if RequestHash("task", "remediation", "a", "urgent") == RequestHash("task", "remediatio", "na", "urgent") {
		t.Fatal("two fields run together")
	}
}

// The name sequence: the requested name, then -2 to -99, and nothing after.
func TestNameSequence(t *testing.T) {
	got := NameSequence("rem-responder-1")
	if len(got) != MaxSuffix || MaxSuffix != 99 {
		t.Fatalf("%d names, MaxSuffix %d", len(got), MaxSuffix)
	}
	for i, want := range map[int]string{0: "rem-responder-1", 1: "rem-responder-1-2", 97: "rem-responder-1-98", 98: "rem-responder-1-99"} {
		if got[i] != want {
			t.Errorf("name %d = %q, want %q", i, got[i], want)
		}
	}
	if slices.Contains(got, "rem-responder-1-100") || slices.Contains(got, "rem-responder-1-1") {
		t.Fatalf("a name outside the sequence: %v", got)
	}
}

// The requested names a charge's name can come from: itself, and the name
// before a suffix -2 to -99 written as NameSequence writes it.
func TestRequestedNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"rem-1", []string{"rem-1"}},
		{"rem-1-2", []string{"rem-1-2", "rem-1"}},
		{"rem-1-99", []string{"rem-1-99", "rem-1"}},
		{"rem-1-100", []string{"rem-1-100"}},
		{"rem-1-02", []string{"rem-1-02"}},
		{"rem-1-1", []string{"rem-1-1"}},
		{"rem-1-+2", []string{"rem-1-+2"}},
		{"-2", []string{"-2"}},
		{"rem", []string{"rem"}},
	} {
		if got := requestedNames(tc.name); !slices.Equal(got, tc.want) {
			t.Errorf("requestedNames(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The filing's dispatch id fits spec.summon.dispatchID's pattern for a
// Kubernetes UID, and no caller's create can draw it: those are 32 hex.
func TestFilingID(t *testing.T) {
	id := FilingID("0f9a6c1e-3b7d-4e2a-9c5b-8d1e2f3a4b5c")
	if id != "filing:0f9a6c1e-3b7d-4e2a-9c5b-8d1e2f3a4b5c" || !DispatchIDPattern.MatchString(id) {
		t.Fatalf("FilingID = %q", id)
	}
}
