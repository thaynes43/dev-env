package asks

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Sweeper expires unanswered Asks in the operator leader. Enabled is the
// operator's --enable-asks flag; disabled means no reads or writes at all.
type Sweeper struct {
	Enabled bool
	Store   KubeStore
	Clock   clock.Clock
	Every   time.Duration
}

func (*Sweeper) NeedLeaderElection() bool { return true }

func (s *Sweeper) Start(ctx context.Context) error {
	if !s.Enabled {
		return nil
	}
	clk := s.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	every := s.Every
	if every <= 0 {
		every = time.Minute
	}
	for {
		s.Sweep(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-clk.After(every):
		}
	}
}

// Sweep scans at most nine pages of 500 records (namespace capacity is 4096).
// It selects no state label: a crash between status and label writes must not
// hide an Ask from repair. Each mutation rereads live and uses resourceVersion.
func (s *Sweeper) Sweep(ctx context.Context) {
	if !s.Enabled || s.Store.ready() != nil {
		return
	}
	now := time.Now()
	if s.Clock != nil {
		now = s.Clock.Now()
	}
	cursor := ""
	for page := 0; page < 9; page++ {
		var list coord.AskList
		if err := s.Store.Live.List(ctx, &list, client.InNamespace(Namespace), client.Limit(500), client.Continue(cursor)); err != nil {
			s.Store.log().Error(nil, "ask expiry scan failed", "reason", expiryErrorReason(err))
			return
		}
		for i := range list.Items {
			obj := &list.Items[i]
			state, wanted := expiryStates(obj, now)
			if state == apiv1.AskStateAnswered || state == apiv1.AskStateWithdrawn ||
				(state == wanted && obj.Labels[StateLabel] == string(wanted)) {
				continue
			}
			if err := s.expire(ctx, obj.Name, now); err != nil {
				s.Store.log().Error(nil, "ask expiry failed", "id", obj.Name, "reason", expiryErrorReason(err))
			}
		}
		cursor = list.Continue
		if cursor == "" {
			return
		}
	}
	s.Store.log().Error(nil, "ask expiry scan exceeded page bound")
}

func expiryErrorReason(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "NotFound"
	case errors.Is(err, ErrUnavailable):
		return "Unavailable"
	case errors.Is(err, ErrConflict):
		return "Conflict"
	default:
		return string(apierrors.ReasonForError(err))
	}
}

// expiryStates reads only lifecycle fields, without validating Ask prose.
func expiryStates(obj *coord.Ask, now time.Time) (apiv1.AskState, apiv1.AskState) {
	state := apiv1.AskState(obj.Status.State)
	if state == "" {
		state = apiv1.AskStateOpen
	}
	wire := apiv1.Ask{State: state, Type: apiv1.AskType(obj.Spec.Type), ExpiresAt: obj.Spec.ExpiresAt.Time}
	if obj.Spec.Scope != nil {
		wire.Scope = &apiv1.AskScope{ExpiresAt: obj.Spec.Scope.ExpiresAt.Time}
	}
	return state, wire.EffectiveState(now)
}

func (s *Sweeper) expire(ctx context.Context, id string, now time.Time) error {
	for attempt := 0; attempt < 4; attempt++ {
		obj, err := s.Store.load(ctx, id)
		if err != nil {
			return err
		}
		state, wanted := expiryStates(obj, now)
		switch state {
		case apiv1.AskStateAnswered, apiv1.AskStateWithdrawn:
			return nil // neither their status nor their metadata is ours to change
		case apiv1.AskStateOpen, apiv1.AskStateExpired:
		default:
			return ErrUnavailable
		}
		if state == apiv1.AskStateOpen && wanted == apiv1.AskStateExpired {
			obj.Status.State = string(wanted)
			if err := s.Store.Client.Status().Update(ctx, obj); err != nil {
				if apierrors.IsConflict(err) {
					continue // an answer may have won; reread before doing anything
				}
				return err
			}
		}
		if obj.Labels[StateLabel] == string(wanted) {
			return nil
		}
		base := obj.DeepCopy()
		if obj.Labels == nil {
			obj.Labels = map[string]string{}
		}
		obj.Labels[StateLabel] = string(wanted)
		// The optimistic lock prevents a label repair overwriting the state
		// label of an answer that committed since our read.
		err = s.Store.Client.Patch(ctx, obj, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if apierrors.IsConflict(err) {
			continue
		}
		return err
	}
	return ErrConflict
}
