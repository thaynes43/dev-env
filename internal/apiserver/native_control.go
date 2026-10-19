package apiserver

import (
	"context"
	"net/http"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

type NativeOwnerControl interface {
	Control(context.Context, *taskbudget.Ledger, apiv1.NativeControlRequest) (apiv1.NativeControlResponse, error)
}

func (s *Server) consumeNativeControl(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindCoordinator || s.NativeOwnerControl == nil || s.TaskBudgets == nil {
		return 0, nil, forbidden("native intent consumption requires the original owned root")
	}
	var req apiv1.NativeControlRequest
	if err := decodeJSON(w, r, 16<<10, &req, true); err != nil {
		return 0, nil, err
	}
	if !req.Valid() || req.Binding.TaskUID != r.PathValue("uid") {
		return 0, nil, badRequest("invalid native intent")
	}
	if _, err := s.budgetCaller(ctx, c, req.Binding.TaskUID); err != nil {
		return 0, nil, err
	}
	l, err := s.TaskBudgets.ConsumeNativeControl(ctx, req)
	if err != nil {
		return 0, nil, budgetError(err)
	}
	if l == nil || l.Latched || l.NativeStopRequested() {
		return 0, nil, budgetError(taskbudget.ErrDenied)
	}
	return http.StatusOK, apiv1.NativeControlPermit{Binding: req.Binding, ID: req.ID, RequestSHA256: apiv1.NativeControlDigest(req), Consumed: true}, nil
}

func (s *Server) nativeControl(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindHuman {
		return 0, nil, forbidden("native control requires the authenticated owner")
	}
	if s.NativeOwnerControl == nil || s.TaskBudgets == nil {
		return 0, nil, budgetError(taskbudget.ErrUnavailable)
	}
	var req apiv1.NativeControlRequest
	if err := decodeJSON(w, r, 16<<10, &req, true); err != nil {
		return 0, nil, err
	}
	if !req.Valid() || req.Binding.TaskUID != r.PathValue("uid") {
		return 0, nil, badRequest("invalid native owner request")
	}
	if _, err := s.budgetCaller(ctx, c, req.Binding.TaskUID); err != nil {
		return 0, nil, err
	}
	deadline := time.Now().Add(8 * time.Second)
	if req.Binding.Deadline.Before(deadline) {
		deadline = req.Binding.Deadline
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	// Reserve on the existing durable campaign before any native mutation.
	// An unknown execution result remains consumed, including across replicas.
	var l *taskbudget.Ledger
	var err error
	if req.Operation == "thread/read" {
		// Passive reads add no intent/counter and are safe to request again.
		l, err = s.TaskBudgets.ObserveNative(bounded, req.Binding)
		if err == nil && (l.Latched || l.NativeStopRequested()) {
			err = taskbudget.ErrDenied
		}
	} else {
		l, err = s.TaskBudgets.ReserveNativeControl(bounded, req, c.identity.Username)
	}
	if err != nil {
		return 0, nil, budgetError(err)
	}
	if req.Operation == "host/stop" {
		return http.StatusAccepted, l.Status(), nil
	}
	response, err := s.NativeOwnerControl.Control(bounded, l, req)
	if err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "native request unconfirmed; request ID consumed, do not replay mutations")
	}
	return http.StatusOK, response, nil
}
