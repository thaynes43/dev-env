package main

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
	"github.com/thaynes43/dev-env/internal/scheduledwork/executor"
)

// The scheduled-work executor is default off: with the flag off the operator
// adds no runnable. On, it needs the API and leader election, and its
// authority and templates are one executor.Sources over the uncached reader:
// the dev-env-scheduled-work ConfigMap in the operator's namespace and the
// owner sessions in the sessions' namespace (DESIGN-054 S3b2).
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
	reader := fake.NewClientBuilder().Build()
	ex := newScheduledWorkExecutor(o, nil, reader, nil, logr.Discard())
	if ex == nil || !ex.NeedLeaderElection() || ex.SchemaMissing == nil || ex.SchemaReadFailures == nil {
		t.Fatalf("the flag on: %+v", ex)
	}
	if ex.Namespace != o.templatesNamespace || ex.SessionNamespace != o.sessionNamespace {
		t.Errorf("namespaces: items %q sessions %q", ex.Namespace, ex.SessionNamespace)
	}
	src, ok := ex.Authorizer.(*executor.Sources)
	if !ok {
		t.Fatalf("authorizer %T, want *executor.Sources", ex.Authorizer)
	}
	if tmpl, ok := ex.Templates.(*executor.Sources); !ok || tmpl != src {
		t.Errorf("templates %T: want the authorizer's own Sources, so a decision and its template are one read", ex.Templates)
	}
	if src.Reader != reader || src.SessionNamespace != o.sessionNamespace || src.TemplatesNamespace != o.templatesNamespace ||
		src.TemplatesName != authority.TemplatesConfigMap || src.StaleAfter != authority.DefaultHeartbeatStaleAfter {
		t.Errorf("sources: %+v", src)
	}
	if ex.MaxHeadless != 3 || ex.MaxInteractive != 2 || ex.HeadlessGrace != 20*time.Minute || ex.InteractiveGrace != 3*time.Hour {
		t.Errorf("limits: %d %d %s %s", ex.MaxHeadless, ex.MaxInteractive, ex.HeadlessGrace, ex.InteractiveGrace)
	}
	scheduledWorkPagerSplitsReadsAndWrites(t, o)

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

// scheduledWorkPagerSplitsReadsAndWrites checks the executor's pager: it
// creates page Reminders through the operator's writing client and reads
// them through the uncached reader, never the writing client (the operator's
// cache), so it starts no informer on Reminders.
func scheduledWorkPagerSplitsReadsAndWrites(t *testing.T, o options) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	writer := fake.NewClientBuilder().WithScheme(scheme).Build()
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	ex := newScheduledWorkExecutor(o, writer, reader, nil, logr.Discard())
	if ex.Pager == nil || ex.Pager.Client == nil {
		t.Fatal("the executor has no pager")
	}
	ctx := context.Background()
	r := &v1alpha1.Reminder{ObjectMeta: metav1.ObjectMeta{Namespace: o.templatesNamespace, Name: "swp-probe"}}
	if err := ex.Pager.Client.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKeyFromObject(r)
	if err := writer.Get(ctx, key, &v1alpha1.Reminder{}); err != nil {
		t.Errorf("the pager did not create through the writing client: %v", err)
	}
	if err := ex.Pager.Client.Get(ctx, key, &v1alpha1.Reminder{}); !apierrors.IsNotFound(err) {
		t.Errorf("the pager read through the writing client, not the uncached reader: %v", err)
	}
}
