package main

import (
	"testing"

	"k8s.io/client-go/rest"
)

// The client-side limit ships off: the default leaves client-go's config as
// controller-runtime made it. An install opts in with --kube-api-qps, and
// --kube-api-burst then sizes the burst.
func TestKubeAPILimitDefaultsOffAndIsOptIn(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "dev-env-system")
	limited := func(args ...string) *rest.Config {
		t.Helper()
		o, err := parseFlags(args)
		if err != nil {
			t.Fatal(err)
		}
		// controller-runtime's config: QPS -1 turns client-go's limiter off.
		cfg := &rest.Config{QPS: -1}
		limitKubeAPI(cfg, o.kubeAPIQPS, o.kubeAPIBurst)
		return cfg
	}

	if cfg := limited(); cfg.QPS != -1 || cfg.Burst != 0 {
		t.Fatalf("the default set QPS %v burst %d; want the limit off and the config unchanged", cfg.QPS, cfg.Burst)
	}
	for _, args := range [][]string{{"--kube-api-qps=0"}, {"--kube-api-qps=-1"}, {"--kube-api-qps=0", "--kube-api-burst=0"}} {
		if cfg := limited(args...); cfg.QPS != -1 || cfg.Burst != 0 {
			t.Fatalf("%v set QPS %v burst %d; want the limit off", args, cfg.QPS, cfg.Burst)
		}
	}

	if cfg := limited("--kube-api-qps=20", "--kube-api-burst=40"); cfg.QPS != 20 || cfg.Burst != 40 {
		t.Fatalf("--kube-api-qps=20 --kube-api-burst=40 set QPS %v burst %d, want 20 and 40", cfg.QPS, cfg.Burst)
	}
	if cfg := limited("--kube-api-qps=5", "--kube-api-burst=7"); cfg.QPS != 5 || cfg.Burst != 7 {
		t.Fatalf("--kube-api-qps=5 --kube-api-burst=7 set QPS %v burst %d, want 5 and 7", cfg.QPS, cfg.Burst)
	}
	if cfg := limited("--kube-api-qps=20"); cfg.QPS != 20 || cfg.Burst != 40 {
		t.Fatalf("--kube-api-qps=20 alone set QPS %v burst %d, want 20 and the default burst 40", cfg.QPS, cfg.Burst)
	}

	if _, err := parseFlags([]string{"--kube-api-qps=5", "--kube-api-burst=0"}); err == nil {
		t.Fatal("a limit with no burst was accepted")
	}
}
