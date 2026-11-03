package allowancecollector

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// EvictionObservationSchema names the second accepted form of the old-Pod
// witness. The archived Pod API object is gone once a Session replaces its
// Pod, but kube-state-metrics keeps the Pod's identity and terminal state as
// series. The observation records those facts. Like a Pod object, its
// provenance is an external precondition that the qualifying coordinator must
// establish; the shape and hash prove neither provenance nor approval.
const EvictionObservationSchema = "dev-env.allowance-handoff.eviction-observation.v1"

const evictionObservationMax = 4 << 10

var evictionObservationKeys = []string{"schema", "source", "namespace", "name", "podUID", "phase", "reason", "ownerKind", "ownerName", "failedSince", "observedAt", "series"}

// evictionSeries is the exact retained evidence set, in order.
var evictionSeries = []string{"kube_pod_info", "kube_pod_status_phase", "kube_pod_status_reason"}

type evictionObservation struct {
	Schema      string   `json:"schema"`
	Source      string   `json:"source"`
	Namespace   string   `json:"namespace"`
	Name        string   `json:"name"`
	PodUID      string   `json:"podUID"`
	Phase       string   `json:"phase"`
	Reason      string   `json:"reason"`
	OwnerKind   string   `json:"ownerKind"`
	OwnerName   string   `json:"ownerName"`
	FailedSince string   `json:"failedSince"`
	ObservedAt  string   `json:"observedAt"`
	Series      []string `json:"series"`
}

// validEvictionWitness accepts either an archived Pod object or an eviction
// observation. An observation carries no owner UID, service account, image or
// resource version: those stay bound by the fingerprinted old configuration and
// the live Session checks, and the observation is not a Pod object.
func validEvictionWitness(raw []byte, old Host, now time.Time) bool {
	if len(raw) > 0 && len(raw) <= evictionObservationMax && providerallowance.ValidJSONDocument(raw, evictionObservationMax) && providerallowance.ExactObjectKeys(raw, evictionObservationKeys) {
		return validEvictionObservation(raw, old, now)
	}
	return validEvictedPod(raw, old)
}

func validEvictionObservation(raw []byte, old Host, now time.Time) bool {
	var o evictionObservation
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&o) != nil || dec.Decode(new(any)) != io.EOF {
		return false
	}
	failed, e1 := time.Parse(time.RFC3339, o.FailedSince)
	seen, e2 := time.Parse(time.RFC3339, o.ObservedAt)
	if e1 != nil || e2 != nil || now.IsZero() || failed.After(seen) || seen.After(now) {
		return false
	}
	if o.Schema != EvictionObservationSchema || o.Source != "kube-state-metrics" || o.Namespace != Namespace || o.Name != old.Name || o.PodUID != old.PodUID || o.Phase != "Failed" || o.Reason != "Evicted" || o.OwnerKind != "AgentSession" || o.OwnerName != old.Name || len(o.Series) != len(evictionSeries) {
		return false
	}
	for i, s := range evictionSeries {
		if o.Series[i] != s {
			return false
		}
	}
	return true
}
