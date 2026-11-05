package main

import "k8s.io/client-go/rest"

// The operator's client-side Kubernetes API limit. controller-runtime turns
// client-go's limit off by default and leaves the load to API priority and
// fairness. Without a limit, a reconcile loop that writes on every pass runs
// as fast as the API server answers: on 2026-11-04 a status-write loop sent
// about 155 AgentSession status PUTs a second, each with a GET, for 13 hours,
// filled etcd and left the cluster read-only (haynes-ops#4663). client-go
// keeps one token bucket per REST client, and controller-runtime makes one
// client per resource kind, so the limit holds for each kind on its own:
// AgentSessions, Pods and PersistentVolumeClaims each get 20 a second with a
// burst of 40. The operator's steady state is well under 1 a second per kind,
// and a full startup pass over every session sends a few hundred requests.
const (
	defaultKubeAPIQPS   = 20
	defaultKubeAPIBurst = 40
)

// limitKubeAPI sets the client-side limit on cfg; a qps of 0 or less leaves
// the config as it is.
func limitKubeAPI(cfg *rest.Config, qps float64, burst int) {
	if qps <= 0 {
		return
	}
	cfg.QPS = float32(qps)
	cfg.Burst = burst
}
