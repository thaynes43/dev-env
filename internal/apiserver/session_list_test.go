package apiserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestSessionListCopiersKeepEmptyArray(t *testing.T) {
	for _, copier := range []struct {
		name string
		copy func(any) any
	}{
		{"legacy usage", legacyUsageShape},
		{"caller worker cap", withoutCallerWorkerCap},
		{"task outcome", withoutTaskOutcome},
	} {
		for _, empty := range []struct {
			name     string
			sessions []apiv1.Session
		}{
			{"nil", nil},
			{"allocated", []apiv1.Session{}},
		} {
			t.Run(copier.name+"/"+empty.name, func(t *testing.T) {
				got, err := json.Marshal(copier.copy(apiv1.SessionList{Sessions: empty.sessions}))
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != `{"sessions":[]}` {
					t.Fatalf("empty session list: got %s, want {\"sessions\":[]}", got)
				}
			})
		}
	}
}

func TestSessionListCopiersKeepEntries(t *testing.T) {
	for _, copier := range []struct {
		name string
		copy func(any) any
	}{
		{"legacy usage", legacyUsageShape},
		{"caller worker cap", withoutCallerWorkerCap},
		{"task outcome", withoutTaskOutcome},
	} {
		t.Run(copier.name, func(t *testing.T) {
			// These fields are visible to legacy clients and must keep both their
			// values and wire keys, including the original list order.
			input := apiv1.SessionList{Sessions: []apiv1.Session{
				{Name: "first", Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Parent: "upgrade-agent/cigar-curation", Lane: "curation", IdempotencyKey: "cigar-curate-20261116", Limits: &apiv1.Limits{Timeout: "1h"}},
				{Name: "second", Phase: "Pending"},
			}}
			before, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			output := copier.copy(input).(apiv1.SessionList)
			after, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("legacy entries changed: got %s, want %s", after, before)
			}
			output.Sessions[0].Name = "changed copy"
			if input.Sessions[0].Name != "first" {
				t.Fatal("copier reused the input's backing array")
			}
		})
	}
}

func TestSummonEmptySessionListWireShape(t *testing.T) {
	f := summonFixture(t)
	// Every combination of the three opt-ins exercises the response paths,
	// including the no-header request used by the curation caller.
	for mask := 0; mask < 8; mask++ {
		t.Run(fmt.Sprintf("opt-ins-%03b", mask), func(t *testing.T) {
			headers := http.Header{}
			if mask&1 != 0 {
				headers.Set(apiv1.AttributionHeader, apiv1.AttributionProvider)
			}
			if mask&2 != 0 {
				headers.Set(apiv1.CallerPolicyLimitsHeader, apiv1.CallerPolicyLimitsV1)
			}
			if mask&4 != 0 {
				headers.Set(apiv1.TaskOutcomeHeader, apiv1.TaskOutcomeV1)
			}
			w := f.doHeader(http.MethodGet, apiv1.SessionsPath, tokStranger, nil, headers)
			if w.Code != http.StatusOK || w.Body.String() != "{\"sessions\":[]}\n" {
				t.Fatalf("empty caller list: HTTP %d %s", w.Code, w.Body.String())
			}
		})
	}
}
