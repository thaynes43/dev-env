package controller

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The guard of DESIGN-001 5.1 (D-45). The operator deletes a session's pod in two
// transitions only, Draining and Suspended, and suspends only after a rescue
// (D-10). deletePod is the one place in this package that deletes anything, and a
// test (TestOnlyTheGuardDeletes) keeps it that way.

// Finalizer holds a session, and its volume, until the session is rescued. A
// deleted AgentSession is a reap (D-45): the API server keeps the object while the
// finalizer is on it, so the garbage collector never reaches the pod or the
// volume. The volume carries the same finalizer, so even a direct or foreground
// delete of the volume leaves its data until the operator archives it.
const Finalizer = v1alpha1.LabelPrefix + "rescue"

// errRescueNotBuilt is why a suspend or a delete waits today: rescue is plan 01
// step 5. When it lands, rescued() says whether this pod's work is safe.
var errRescueNotBuilt = errors.New("the pod and volume stay until the session is rescued (DESIGN-001 4.4); rescue arrives in plan 01 step 5")

// errDrainNotBuilt is why a Draining session keeps its pod today: drain needs
// agentd's prepare-restart, plan 04 (DESIGN-001 5.2).
var errDrainNotBuilt = errors.New("drain is not built yet (plan 04, DESIGN-001 5.2); the pod stays")

// wantsPodGone reports whether the session asks for its pod to go: suspended, or
// deleted (a reap, which suspends first).
func wantsPodGone(s *v1alpha1.AgentSession) bool {
	return s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !s.DeletionTimestamp.IsZero()
}

// podRemovalAllowed is nil only when 5.1 allows deleting the session's pod now:
// in the Draining transition, or in the Suspended one after a rescue. Every other
// state keeps the pod, whatever else asks.
func podRemovalAllowed(s *v1alpha1.AgentSession) error {
	switch {
	case s.Status.Phase == v1alpha1.PhaseDraining:
		return errDrainNotBuilt
	case wantsPodGone(s):
		if !rescued(s) {
			return errRescueNotBuilt
		}
		return nil
	default:
		return fmt.Errorf("the session is neither draining nor suspending (operatingMode %s); its pod stays (DESIGN-001 5.1)", s.Spec.OperatingMode)
	}
}

// rescued reports whether the session's work is safe off its volume: a bundle
// written after the pod's last start that covers every unpushed ref (D-10).
// Plan 01 step 5 builds the rescue and this check; until then nothing is ever
// rescued, so nothing is ever deleted.
func rescued(*v1alpha1.AgentSession) bool {
	return false
}

// deletePod deletes the session's pod if the guard allows it. It is the only
// delete in the package.
func deletePod(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if err := podRemovalAllowed(s); err != nil {
		return err
	}
	// The precondition makes the delete hit this pod only, not one that
	// replaced it since the reconcile read it.
	return client.IgnoreNotFound(c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID}))
}
