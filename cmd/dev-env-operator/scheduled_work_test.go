package main

import (
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/thaynes43/dev-env/internal/scheduledwork/executor"
)

// The scheduled-work executor is default off: with the flag off the operator
// adds no runnable. On, it needs the API and leader election, and until S3b2
// it is wired with DenyAll and NoTemplates, so nothing dispatches.
func TestScheduledWorkFlags(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.scheduledWork.enabled {
		t.Fatalf("--enable-scheduled-work must be off by default: %+v, %v", o.scheduledWork, err)
	}
	want := scheduledWorkOptions{maxHeadless: 2, maxInteractive: 1, headlessGrace: 15 * time.Minute, interactiveGrace: 2 * time.Hour}
	if o.scheduledWork != want {
		t.Errorf("defaults %+v, want %+v", o.scheduledWork, want)
	}
	if ex := newScheduledWorkExecutor(o, nil, nil, nil, logr.Discard()); ex != nil {
		t.Fatal("the flag off added the executor")
	}

	o, err = parseFlags([]string{"--enable-scheduled-work", "--scheduled-work-max-headless=3", "--scheduled-work-max-interactive=2",
		"--scheduled-work-headless-grace=20m", "--scheduled-work-interactive-grace=3h"})
	if err != nil {
		t.Fatal(err)
	}
	ex := newScheduledWorkExecutor(o, nil, nil, nil, logr.Discard())
	if ex == nil || !ex.NeedLeaderElection() || ex.SchemaMissing == nil {
		t.Fatalf("the flag on: %+v", ex)
	}
	if ex.Namespace != o.templatesNamespace || ex.SessionNamespace != o.sessionNamespace {
		t.Errorf("namespaces: items %q sessions %q", ex.Namespace, ex.SessionNamespace)
	}
	if _, ok := ex.Authorizer.(executor.DenyAll); !ok {
		t.Errorf("authorizer %T, want DenyAll until S3b2", ex.Authorizer)
	}
	if _, ok := ex.Templates.(executor.NoTemplates); !ok {
		t.Errorf("templates %T, want NoTemplates until S3b2", ex.Templates)
	}
	if ex.MaxHeadless != 3 || ex.MaxInteractive != 2 || ex.HeadlessGrace != 20*time.Minute || ex.InteractiveGrace != 3*time.Hour {
		t.Errorf("limits: %d %d %s %s", ex.MaxHeadless, ex.MaxInteractive, ex.HeadlessGrace, ex.InteractiveGrace)
	}

	for _, args := range [][]string{
		{"--enable-scheduled-work", "--api-bind-address=0"},
		{"--enable-scheduled-work", "--leader-elect=false"},
		{"--scheduled-work-max-headless=0"},
		{"--scheduled-work-max-interactive=17"},
		{"--scheduled-work-headless-grace=30s"},
		{"--scheduled-work-interactive-grace=13h"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}
