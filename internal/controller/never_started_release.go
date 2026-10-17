package controller

import (
	"errors"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

// neverStartedReleaseAllowed is nil only for plan 11 F2 (DESIGN-025 section 23):
// a reaped private Session whose retention admission is NeverStarted, with no
// executor attempt and no recorded HOME. The caller still proves, by uncached
// reads, that no Pod and no HOME claim of its names exist. This predicate does
// not delete a volume, delete a pod, or lift a finalizer. archive,
// releaseIfEmpty and releaseSession do not consult it yet.
func neverStartedReleaseAllowed(s *v1alpha1.AgentSession) error {
	h := s.Status.PrivateHistory
	switch {
	case s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil || projectcanary.IsPilot(s):
		return errors.New("only an ordinary private Session can take the never-started release")
	case s.DeletionTimestamp.IsZero():
		return errors.New("a live Session is not released")
	case privateHistoryIdentity(s) != nil:
		return privateHistoryIdentity(s)
	case h.Admission != sharedNeverStarted || h.HomeUID != "" || h.ExecutorAttempted || h.ExecutorIntent != "" || h.ExecutorNotCreated ||
		h.LastExecutor != nil || h.ExecutorTerminal != nil || h.ExecutorMissed != nil || h.StopRequest != nil || h.Expiry != nil || h.ExpiryClearedAt != nil:
		return errors.New("never-started release requires an untouched NeverStarted admission")
	default:
		return nil
	}
}
