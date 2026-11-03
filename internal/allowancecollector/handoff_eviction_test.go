package allowancecollector

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func evictionObservationFixture(t *testing.T, host Host, now time.Time) map[string]any {
	t.Helper()
	return map[string]any{"schema": EvictionObservationSchema, "source": "kube-state-metrics", "namespace": Namespace, "name": host.Name, "podUID": host.PodUID, "phase": "Failed", "reason": "Evicted", "ownerKind": "AgentSession", "ownerName": host.Name, "failedSince": now.Add(-time.Hour).Format(time.RFC3339), "observedAt": now.Add(-time.Minute).Format(time.RFC3339), "series": []string{"kube_pod_info", "kube_pod_status_phase", "kube_pod_status_reason"}}
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
	other["failedSince"] = in.Now.Add(-2 * time.Hour).Format(time.RFC3339)
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
		"failed after seen":  func(m map[string]any, h Host, n time.Time) { m["failedSince"] = n.Format(time.RFC3339) },
		"bad time":           func(m map[string]any, _ Host, _ time.Time) { m["failedSince"] = "yesterday" },
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
