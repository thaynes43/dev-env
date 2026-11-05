package main

import (
	"testing"

	"k8s.io/client-go/rest"
)

func TestKubeAPILimitDefaultsOnAndConfigurable(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &rest.Config{QPS: -1}
	limitKubeAPI(cfg, o.kubeAPIQPS, o.kubeAPIBurst)
	if cfg.QPS != 20 || cfg.Burst != 40 {
		t.Fatalf("default limit is QPS %v burst %d, want 20 and 40", cfg.QPS, cfg.Burst)
	}

	o, err = parseFlags([]string{"--kube-api-qps=-1"})
	if err != nil {
		t.Fatal(err)
	}
	cfg = &rest.Config{QPS: -1}
	limitKubeAPI(cfg, o.kubeAPIQPS, o.kubeAPIBurst)
	if cfg.QPS != -1 {
		t.Fatalf("--kube-api-qps=-1 left QPS %v, want the limit off", cfg.QPS)
	}

	if _, err := parseFlags([]string{"--kube-api-qps=5", "--kube-api-burst=0"}); err == nil {
		t.Fatal("a limit with no burst was accepted")
	}
}
