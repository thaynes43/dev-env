package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
	"github.com/thaynes43/dev-env/internal/scheduledwork/executor"
)

// scheduledWorkOptions are the scheduled-work executor's flags (DESIGN-054
// S3b).
type scheduledWorkOptions struct {
	enabled                         bool
	maxHeadless, maxInteractive     int
	headlessGrace, interactiveGrace time.Duration
}

// The bounds of the executor's flags. A grace stays well under a day, so at
// most one occurrence of a daily series is inside its grace at a time.
const (
	maxScheduledWorkCap   = 16
	minScheduledWorkGrace = time.Minute
	maxScheduledWorkGrace = 12 * time.Hour
)

func (o scheduledWorkOptions) validate(apiAddr string, leaderElect bool) error {
	for name, v := range map[string]int{"--scheduled-work-max-headless": o.maxHeadless, "--scheduled-work-max-interactive": o.maxInteractive} {
		if v < 1 || v > maxScheduledWorkCap {
			return fmt.Errorf("%s %d is not 1 to %d", name, v, maxScheduledWorkCap)
		}
	}
	for name, v := range map[string]time.Duration{"--scheduled-work-headless-grace": o.headlessGrace, "--scheduled-work-interactive-grace": o.interactiveGrace} {
		if v < minScheduledWorkGrace || v > maxScheduledWorkGrace {
			return fmt.Errorf("%s %s is not %s to %s", name, v, minScheduledWorkGrace, maxScheduledWorkGrace)
		}
	}
	switch {
	case !o.enabled:
		return nil
	case apiAddr == "0":
		return errors.New("--enable-scheduled-work requires the API (--api-bind-address): the executor creates sessions through its creation path")
	case !leaderElect:
		return errors.New("--enable-scheduled-work requires leader election: the executor runs in the leader only")
	}
	return nil
}

// newScheduledWorkExecutor builds the default-off scheduled-work executor. A
// false flag returns nil, so the operator adds no runnable. It reads the
// ScheduledWorks in the operator's own namespace, where the releaser reads
// them (Reconciler.ScheduledWorkNamespace), with the uncached reader only, and
// creates through creator, the API's fixed-name entry. Its authority and
// template source is executor.Sources (DESIGN-054 S3b2) over the same
// uncached reader: the dispatch templates ConfigMap
// (authority.TemplatesConfigMap) in that namespace, and the owner session and
// its pod in the sessions' namespace. Without that ConfigMap, or without a
// basis the template admits, an occurrence parks and nothing dispatches. Its
// pager (DESIGN-054 Decision 5, S4b) creates the page Reminders, in the
// items' namespace, through c and reads them through the same uncached
// reader; the keeper sends them.
func newScheduledWorkExecutor(o options, c client.Client, reader client.Reader, creator executor.Creator, log logr.Logger) *executor.Executor {
	if !o.scheduledWork.enabled {
		return nil
	}
	sources := &executor.Sources{
		Reader:             reader,
		SessionNamespace:   o.sessionNamespace,
		TemplatesNamespace: o.templatesNamespace,
		TemplatesName:      authority.TemplatesConfigMap,
		StaleAfter:         authority.DefaultHeartbeatStaleAfter,
	}
	return &executor.Executor{
		Client:             c,
		Reader:             reader,
		Namespace:          o.templatesNamespace,
		SessionNamespace:   o.sessionNamespace,
		Creator:            creator,
		Authorizer:         sources,
		Templates:          sources,
		HeadlessGrace:      o.scheduledWork.headlessGrace,
		InteractiveGrace:   o.scheduledWork.interactiveGrace,
		MaxHeadless:        o.scheduledWork.maxHeadless,
		MaxInteractive:     o.scheduledWork.maxInteractive,
		SchemaMissing:      executor.NewSchemaMissingCounter(),
		SchemaReadFailures: executor.NewSchemaReadFailuresCounter(),
		Pager:              executor.NewPager(c, reader),
		Log:                log,
	}
}
