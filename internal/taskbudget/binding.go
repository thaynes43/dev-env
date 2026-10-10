package taskbudget

import (
	"context"
	"strconv"
	"time"
)

const (
	UIDAnnotation      = "dev-env.haynesops.com/task-budget-uid"
	EpochAnnotation    = "dev-env.haynesops.com/task-budget-epoch"
	DeadlineAnnotation = "dev-env.haynesops.com/task-budget-deadline"
	HostAnnotation     = "dev-env.haynesops.com/task-budget-host"
	PodAnnotation      = "dev-env.haynesops.com/task-budget-pod"
	WorkerAnnotation   = "dev-env.haynesops.com/task-budget-worker"
)

func BindingFromAnnotations(a map[string]string) (Binding, error) {
	epoch, err := strconv.ParseUint(a[EpochAnnotation], 10, 64)
	if err != nil || epoch == 0 {
		return Binding{}, ErrDenied
	}
	deadline, err := time.Parse(time.RFC3339Nano, a[DeadlineAnnotation])
	if err != nil || deadline.IsZero() {
		return Binding{}, ErrDenied
	}
	b := Binding{TaskUID: a[UIDAnnotation], Epoch: epoch, Deadline: deadline, HostID: a[HostAnnotation], PodUID: a[PodAnnotation]}
	if !identifier.MatchString(b.TaskUID) || !identifier.MatchString(b.HostID) || !identifier.MatchString(b.PodUID) {
		return Binding{}, ErrDenied
	}
	return b, nil
}

// ManagedWorkerRef is assigned by the controller from live Kubernetes identity,
// never supplied by the worker. PodUID may be filled once after initial creation.
type ManagedWorkerRef struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	SessionUID      string `json:"sessionUID"`
	PodUID          string `json:"podUID,omitempty"`
	LaunchRequested bool   `json:"launchRequested,omitempty"`
}

func (s *Service) BindManagedWorker(ctx context.Context, b Binding, id string, ref ManagedWorkerRef) (*Ledger, error) {
	if !identifier.MatchString(id) || !identifier.MatchString(ref.Namespace) || !identifier.MatchString(ref.Name) || !identifier.MatchString(ref.SessionUID) ||
		(ref.PodUID != "" && !identifier.MatchString(ref.PodUID)) {
		return nil, ErrDenied
	}
	return s.mutate(ctx, b, func(l *Ledger, _ time.Time) error {
		for i := range l.Workers {
			w := &l.Workers[i]
			if w.ID != id {
				continue
			}
			if prior := w.Managed; prior != nil {
				if prior.Namespace != ref.Namespace || prior.Name != ref.Name || prior.SessionUID != ref.SessionUID ||
					(prior.PodUID != "" && prior.PodUID != ref.PodUID) {
					return ErrConflict
				}
				ref.LaunchRequested = prior.LaunchRequested
			}
			copy := ref
			w.Managed = &copy
			return nil
		}
		return ErrDenied
	})
}

// ManagedAdmissionInspector independently verifies the immutable finite executor
// deadline and campaign authority before launch. Precise internal native usage
// may remain unavailable; it never disables elapsed-time or attempt limits.
type ManagedAdmissionInspector interface {
	InspectManagedAdmission(context.Context, *Ledger, ManagedWorkerRef) error
}

// ReserveManagedLaunch durably fences a single Pod create before side effects.
// Unknown create results keep the reservation. Absence never permits a retry.
func (s *Service) ReserveManagedLaunch(ctx context.Context, b Binding, id string) (*Ledger, error) {
	return s.mutate(ctx, b, func(l *Ledger, _ time.Time) error {
		if l.Latched {
			return ErrDenied
		}
		if s.ManagedInspector == nil {
			return ErrUnavailable
		}
		for i := range l.Workers {
			w := &l.Workers[i]
			if w.ID != id {
				continue
			}
			if !w.Active || w.Managed == nil || w.Managed.LaunchRequested || w.Managed.PodUID != "" {
				return ErrDenied
			}
			if err := s.ManagedInspector.InspectManagedAdmission(ctx, l, *w.Managed); err != nil {
				return err
			}
			now := s.now()
			if err := l.account(now); err != nil {
				return err
			}
			l.evaluate(now)
			if l.Latched {
				return ErrDenied
			}
			w.Managed.LaunchRequested = true
			return nil
		}
		return ErrDenied
	})
}
