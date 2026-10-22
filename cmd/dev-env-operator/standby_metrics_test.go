package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/standby"
)

type standbyReadCounter struct {
	reads int
}

func (r *standbyReadCounter) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	r.reads++
	return errors.New("read")
}

func (r *standbyReadCounter) List(context.Context, client.ObjectList, ...client.ListOption) error {
	r.reads++
	return errors.New("read")
}

func TestStandbyCollectorWiring(t *testing.T) {
	now := time.Date(2026, 10, 22, 21, 0, 0, 0, time.UTC)
	on := options{sessionNamespace: "dev-agents", templatesNamespace: "dev-env-system", standbyEnabled: true}
	enabledReads := &standbyReadCounter{}
	enabled := standbyCollector(on, enabledReads)
	enabled.Now = func() time.Time { return now }
	if !enabled.Enabled || enabled.SessionNamespace != "dev-agents" || enabled.StateNamespace != "dev-env-system" {
		t.Fatalf("enabled collector %#v", enabled)
	}
	ch := make(chan prometheus.Metric, 32)
	enabled.Collect(ch)
	if enabledReads.reads != 2 {
		t.Fatalf("enabled collector reads = %d, want list and get", enabledReads.reads)
	}

	off := options{sessionNamespace: "sessions", templatesNamespace: "templates", standbyEnabled: false}
	disabledReads := &standbyReadCounter{}
	disabled := standbyCollector(off, disabledReads)
	if disabled.Enabled || disabled.SessionNamespace != "sessions" || disabled.StateNamespace != "templates" {
		t.Fatalf("disabled collector %#v", disabled)
	}
	disabled.Collect(ch)
	if disabledReads.reads != 0 {
		t.Fatalf("disabled collector reads = %d", disabledReads.reads)
	}

	reg := prometheus.NewRegistry()
	if err := standby.RegisterCollector(reg, disabled); err != nil {
		t.Fatal(err)
	}
	err := standby.RegisterCollector(reg, enabled)
	var already prometheus.AlreadyRegisteredError
	if !errors.As(err, &already) {
		t.Fatalf("duplicate register: %v", err)
	}
}
