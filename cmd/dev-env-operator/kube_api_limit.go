package main

import "k8s.io/client-go/rest"

// The operator's client-side Kubernetes API limit, off unless --kube-api-qps
// turns it on. controller-runtime turns client-go's limit off by default and
// leaves the load to API priority and fairness. Without a limit, a reconcile
// loop that writes on every pass runs as fast as the API server answers: on
// 2026-11-04 a status-write loop sent about 155 AgentSession status PUTs a
// second, each with a GET, for 13 hours, filled etcd and left the cluster
// read-only (haynes-ops#4663). client-go keeps one token bucket per REST
// client, and controller-runtime makes one client per resource kind, so a
// limit holds for each kind on its own: --kube-api-qps=20 --kube-api-burst=40
// gives AgentSessions, Pods and PersistentVolumeClaims 20 requests a second
// each, with a burst of 40. The operator's measured normal rate is about 1 to
// 1.7 requests a second in all, but its startup resync and rescue bursts were
// not measured, so the limit ships off and an install opts in.
const (
	defaultKubeAPIQPS   = 0
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
