package controller

import (
	"context"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
)

func (r *Reconciler) observeSessionDirectory(ctx context.Context, s *v1alpha1.AgentSession) error {
	if r.SessionDirectory == nil {
		return sessiondirectory.ErrUnavailable
	}
	if receipt := s.Status.SessionDirectory; receipt != nil && receipt.State == "Confirmed" {
		return r.SessionDirectory.Observe(ctx, receipt.Name, time.Now())
	}
	if s.Status.PrivateHistory != nil && s.Status.PrivateHistory.ExecutorAttempted {
		return sessiondirectory.ErrUnavailable
	}
	return nil
}
