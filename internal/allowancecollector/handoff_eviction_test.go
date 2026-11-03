package allowancecollector

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func evictionObservationFixture(t *testing.T, host Host, now time.Time) map[string]any {
	t.Helper()
	return map[string]any{"schema": EvictionObservationSchema, "source": "kube-state-metrics", "namespace": Namespace, "name": host.Name, "podUID": host.PodUID, "phase": "Failed", "reason": "Evicted", "ownerKind": "AgentSession", "ownerName": host.Name, "lastReturnedRunning": now.Add(-35 * time.Second).Format(time.RFC3339), "firstReturnedFailed": now.Add(-30 * time.Second).Format(time.RFC3339), "observedAt": now.Add(-10 * time.Second).Format(time.RFC3339), "series": []string{"kube_pod_info", "kube_pod_status_phase", "kube_pod_status_reason"}}
}

func TestEvictionObservationIsAcceptedAsWitnessAndBoundByHash(t *testing.T) {
	in := handoffFixture(t)
	old, err := ParseConfig(in.OldConfig, in.Trust, in.PublisherUID, in.PublisherSA, in.Now)
	if err != nil {
		t.Fatal(err)
	}
	in.EvictedPod, _ = json.Marshal(evictionObservationFixture(t, old.Host, in.Now))
	p, err := PrepareHandoff(in)
	if err != nil {
		t.Fatal("valid observation refused", err)
	}
	// The witness bytes are part of the approved hash.
	other := evictionObservationFixture(t, old.Host, in.Now)
	other["firstReturnedFailed"] = in.Now.Add(-25 * time.Second).Format(time.RFC3339)
	in2 := in
	in2.EvictedPod, _ = json.Marshal(other)
	p2, err := PrepareHandoff(in2)
	if err != nil || p.Hash == p2.Hash {
		t.Fatal("witness not bound by the prepared hash", err)
	}
}

func TestEvictionObservationRefusals(t *testing.T) {
	for name, edit := range map[string]func(map[string]any, Host, time.Time){
		"wrong schema":     func(m map[string]any, _ Host, _ time.Time) { m["schema"] = "other" },
		"wrong source":     func(m map[string]any, _ Host, _ time.Time) { m["source"] = "operator" },
		"wrong namespace":  func(m map[string]any, _ Host, _ time.Time) { m["namespace"] = "other" },
		"wrong name":       func(m map[string]any, _ Host, _ time.Time) { m["name"] = "other" },
		"live pod uid":     func(m map[string]any, _ Host, _ time.Time) { m["podUID"] = "44444444-4444-4444-4444-444444444444" },
		"running":          func(m map[string]any, _ Host, _ time.Time) { m["phase"] = "Running" },
		"not evicted":      func(m map[string]any, _ Host, _ time.Time) { m["reason"] = "Error" },
		"wrong owner kind": func(m map[string]any, _ Host, _ time.Time) { m["ownerKind"] = "Pod" },
		"wrong owner name": func(m map[string]any, _ Host, _ time.Time) { m["ownerName"] = "other" },
		"missing series":   func(m map[string]any, _ Host, _ time.Time) { m["series"] = []string{"kube_pod_info"} },
		"reordered series": func(m map[string]any, _ Host, _ time.Time) {
			m["series"] = []string{"kube_pod_status_reason", "kube_pod_status_phase", "kube_pod_info"}
		},
		"observed in future": func(m map[string]any, h Host, n time.Time) { m["observedAt"] = n.Add(time.Hour).Format(time.RFC3339) },
		"failed after seen":  func(m map[string]any, h Host, n time.Time) { m["firstReturnedFailed"] = n.Format(time.RFC3339) },
		"bad time":           func(m map[string]any, _ Host, _ time.Time) { m["firstReturnedFailed"] = "yesterday" },
		"extra key":          func(m map[string]any, _ Host, _ time.Time) { m["resourceVersion"] = "1" },
	} {
		t.Run(name, func(t *testing.T) {
			in := handoffFixture(t)
			old, err := ParseConfig(in.OldConfig, in.Trust, in.PublisherUID, in.PublisherSA, in.Now)
			if err != nil {
				t.Fatal(err)
			}
			m := evictionObservationFixture(t, old.Host, in.Now)
			edit(m, old.Host, in.Now)
			in.EvictedPod, _ = json.Marshal(m)
			if _, err = PrepareHandoff(in); err == nil {
				t.Fatal("accepted unqualified observation")
			}
		})
	}
	t.Run("oversize", func(t *testing.T) {
		in := handoffFixture(t)
		old, _ := ParseConfig(in.OldConfig, in.Trust, in.PublisherUID, in.PublisherSA, in.Now)
		m := evictionObservationFixture(t, old.Host, in.Now)
		m["source"] = strings.Repeat("x", evictionObservationMax)
		in.EvictedPod, _ = json.Marshal(m)
		if _, err := PrepareHandoff(in); err == nil {
			t.Fatal("accepted oversize observation")
		}
	})
}

// Direct checks of the observation form, so a refusal cannot be a fall-through
// to the Pod-object path.
func TestEvictionObservationDirect(t *testing.T) {
	in := handoffFixture(t)
	old, err := ParseConfig(in.OldConfig, in.Trust, in.PublisherUID, in.PublisherSA, in.Now)
	if err != nil {
		t.Fatal(err)
	}
	now, floor := in.Now, in.Now.Add(-2*time.Hour)
	build := func(edit func(map[string]any)) []byte {
		m := evictionObservationFixture(t, old.Host, now)
		if edit != nil {
			edit(m)
		}
		raw, _ := json.Marshal(m)
		return raw
	}
	for name, tc := range map[string]struct {
		raw  []byte
		want bool
	}{
		"baseline":               {build(nil), true},
		"failed equals observed": {build(func(m map[string]any) { m["firstReturnedFailed"] = m["observedAt"] }), true},
		"observed equals now":    {build(func(m map[string]any) { m["observedAt"] = now.Format(time.RFC3339) }), true},
		"running equals floor":   {build(func(m map[string]any) { m["lastReturnedRunning"] = floor.Format(time.RFC3339) }), true},
		"offset and fractions": {build(func(m map[string]any) {
			m["firstReturnedFailed"] = now.Add(-30 * time.Second).In(time.FixedZone("x", 3600)).Format(time.RFC3339Nano)
		}), true},
		"running before floor":      {build(func(m map[string]any) { m["lastReturnedRunning"] = floor.Add(-time.Second).Format(time.RFC3339) }), false},
		"running not before failed": {build(func(m map[string]any) { m["lastReturnedRunning"] = m["firstReturnedFailed"] }), false},
		"running after failed":      {build(func(m map[string]any) { m["lastReturnedRunning"] = now.Format(time.RFC3339) }), false},
		"year one":                  {build(func(m map[string]any) { m["lastReturnedRunning"] = "0001-01-01T00:00:00Z" }), false},
		"missing key":               {build(func(m map[string]any) { delete(m, "reason") }), false},
		"extra key":                 {build(func(m map[string]any) { m["x"] = 1 }), false},
		"case variant key":          {build(func(m map[string]any) { m["Schema"] = m["schema"]; delete(m, "schema") }), false},
		"duplicate series": {build(func(m map[string]any) {
			m["series"] = []string{"kube_pod_info", "kube_pod_info", "kube_pod_status_phase"}
		}), false},
		"fourth series": {build(func(m map[string]any) {
			m["series"] = []string{"kube_pod_info", "kube_pod_status_phase", "kube_pod_status_reason", "x"}
		}), false},
		"null series":   {build(func(m map[string]any) { m["series"] = nil }), false},
		"empty name":    {build(func(m map[string]any) { m["name"] = "" }), false},
		"trailing data": {append(build(nil), []byte(" {}")...), false},
		"duplicate key": {[]byte(strings.Replace(string(build(nil)), `"phase":"Failed"`, `"phase":"Failed","phase":"Failed"`, 1)), false},
		"zero floor":    {build(nil), false},
	} {
		t.Run(name, func(t *testing.T) {
			f := floor
			if name == "zero floor" {
				f = time.Time{}
			}
			if got := validEvictionObservation(tc.raw, old.Host, now, f); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	// The dispatcher sends a 4 KiB-plus document to the Pod path, which refuses it.
	big := build(func(m map[string]any) { m["source"] = strings.Repeat("x", evictionObservationMax) })
	if validEvictionWitness(big, old.Host, now, floor) {
		t.Fatal("oversize accepted")
	}
}

// A witness whose failure predates the held cycle is refused through the plan.
func TestPrepareRefusesObservationOlderThanHeldCycle(t *testing.T) {
	in := handoffFixture(t)
	old, _ := ParseConfig(in.OldConfig, in.Trust, in.PublisherUID, in.PublisherSA, in.Now)
	m := evictionObservationFixture(t, old.Host, in.Now)
	m["lastReturnedRunning"] = in.Now.Add(-10*time.Minute - 5*time.Second).Format(time.RFC3339)
	m["firstReturnedFailed"] = in.Now.Add(-10 * time.Minute).Format(time.RFC3339)
	in.EvictedPod, _ = json.Marshal(m)
	if _, err := PrepareHandoff(in); err == nil {
		t.Fatal("accepted a failure older than the held cycle")
	}
}

// The operator route applies a handoff whose witness is an observation, with the
// same single write and in-slot backup as the Pod-object form.
func TestInSlotHandoffAppliesWithEvictionObservation(t *testing.T) {
	h := newSlotHandoff(t)
	start, err := time.Parse(time.RFC3339Nano, h.held.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	m := evictionObservationFixture(t, h.r.Config.Host, *h.now)
	m["lastReturnedRunning"] = start.UTC().Format(time.RFC3339)
	m["firstReturnedFailed"] = start.Add(time.Second).UTC().Format(time.RFC3339)
	m["observedAt"] = h.now.UTC().Format(time.RFC3339)
	h.in.EvictedPod, _ = json.Marshal(m)
	if h.plan, err = PrepareHandoff(h.in); err != nil {
		t.Fatal("observation refused", err)
	}
	spy := &countHandoffUpdates{Client: h.r.Client}
	run := h.handoffRunner()
	run.Snapshot.EvictedPod = h.in.EvictedPod
	run.Client = spy
	if err = run.Attempt(context.Background()); err != nil || spy.calls != 1 {
		t.Fatal("apply with observation", err, spy.calls)
	}
	if h.slotNow(t).Data[StateKey] != string(h.plan.NewJournal) {
		t.Fatal("journal not replaced")
	}
	// The same document with a failure before the held cycle is refused and writes nothing.
	h2 := newSlotHandoff(t)
	m["lastReturnedRunning"] = start.Add(-time.Hour - 5*time.Second).UTC().Format(time.RFC3339)
	m["firstReturnedFailed"] = start.Add(-time.Hour).UTC().Format(time.RFC3339)
	h2.in.EvictedPod, _ = json.Marshal(m)
	if _, err = PrepareHandoff(h2.in); err == nil {
		t.Fatal("stale observation accepted")
	}
}
