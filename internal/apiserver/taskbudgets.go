package apiserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

const (
	TaskBudgetUIDAnnotation      = taskbudget.UIDAnnotation
	TaskBudgetEpochAnnotation    = taskbudget.EpochAnnotation
	TaskBudgetDeadlineAnnotation = taskbudget.DeadlineAnnotation
	TaskBudgetHostAnnotation     = taskbudget.HostAnnotation
	TaskBudgetPodAnnotation      = taskbudget.PodAnnotation
	TaskBudgetWorkerAnnotation   = taskbudget.WorkerAnnotation
)

func budgetError(err error) error {
	switch {
	case errors.Is(err, taskbudget.ErrDenied):
		return newError(http.StatusConflict, apiv1.CodeLimitExceeded, "task budget is stopped or binding is invalid")
	case errors.Is(err, taskbudget.ErrConflict):
		return newError(http.StatusConflict, apiv1.CodeConflict, "task budget authority changed")
	default:
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "task budget authority is unavailable")
	}
}

// AssignedTaskBudgets binds one configured campaign to each host. The API has
// no route to replace that mapping, delete a ledger or silently start a new task
// after a latch. Native remote chat coverage requires the owned host executor.
func (s *Server) budgetCaller(ctx context.Context, c *caller, uid string) (*taskbudget.Ledger, error) {
	if s.TaskBudgets == nil || s.TaskBudgets.Store == nil || len(s.AssignedTaskBudgets) == 0 {
		return nil, budgetError(taskbudget.ErrUnavailable)
	}
	l, _, err := s.TaskBudgets.Store.Read(ctx, uid)
	if err != nil {
		return nil, budgetError(err)
	}
	if c.kind == kindHuman {
		return l, nil
	}
	if c.kind == kindCoordinator {
		if _, err := s.resolveCoordinatorForBudget(ctx, c, l.Binding.HostID); err != nil {
			return nil, err
		}
		if s.AssignedTaskBudgets[l.Binding.HostID] != uid || l.Parent != c.parent || l.Binding.PodUID != c.identity.PodUID {
			return nil, coordinatorDenied()
		}
		return l, nil
	}
	if c.kind == kindSession && c.session != nil && c.session.Annotations[TaskBudgetUIDAnnotation] == uid &&
		c.session.Spec.Parent == l.Parent {
		// No new descendant authority is inferred from a caller-provided UID.
		// First unit supports only the configured host's direct managed children.
		b, err := s.bindingForSession(c.session)
		if err != nil {
			return nil, err
		}
		if b.TaskUID != l.Binding.TaskUID || b.Epoch != l.Binding.Epoch || b.HostID != l.Binding.HostID || b.PodUID != l.Binding.PodUID || !b.Deadline.Equal(l.Binding.Deadline) {
			return nil, forbidden("managed child budget binding changed")
		}
		return l, nil
	}
	return nil, forbidden("task budget caller has no bound authority")
}

func (s *Server) resolveCoordinatorForBudget(ctx context.Context, c *caller, hostID string) (*caller, error) {
	for _, host := range s.Policy.Coordinators {
		if host.HostID == hostID && host.ServiceAccount == c.parent {
			return s.resolveCoordinator(ctx, c.identity, host)
		}
	}
	return nil, coordinatorDenied()
}

func (s *Server) createTaskBudget(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if s.TaskBudgets == nil || c.kind != kindCoordinator {
		return 0, nil, budgetError(taskbudget.ErrUnavailable)
	}
	var req apiv1.CreateTaskBudgetRequest
	if err := decodeJSON(w, r, 8192, &req, true); err != nil {
		return 0, nil, err
	}
	if req.Spec.OverallSeconds > apiv1.TaskBudgetPilotOverallSeconds || req.Spec.EffortSeconds > apiv1.TaskBudgetPilotEffortSeconds || req.Spec.CheckpointSeconds > apiv1.TaskBudgetPilotCheckpointSeconds {
		return 0, nil, badRequest("initial task budget exceeds the assigned pilot ceilings")
	}
	for _, host := range s.Policy.Coordinators {
		if host.ServiceAccount != c.parent || s.AssignedTaskBudgets[host.HostID] != req.TaskUID || req.TaskUID == "" {
			continue
		}
		if _, err := s.resolveCoordinator(ctx, c.identity, host); err != nil {
			return 0, nil, err
		}
		l, err := s.TaskBudgets.Create(ctx, req.TaskUID, host.HostID, c.identity.PodUID, c.parent, req.Spec)
		if err != nil {
			return 0, nil, budgetError(err)
		}
		return http.StatusCreated, l.Status(), nil
	}
	return 0, nil, coordinatorDenied()
}

func (s *Server) taskBudgetControl(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller, admit bool) (int, any, error) {
	var req apiv1.TaskBudgetRequest
	if err := decodeJSON(w, r, 4096, &req, true); err != nil {
		return 0, nil, err
	}
	if req.Binding.TaskUID != r.PathValue("uid") {
		return 0, nil, badRequest("task UID does not match route")
	}
	if _, err := s.budgetCaller(ctx, c, req.Binding.TaskUID); err != nil {
		return 0, nil, err
	}
	// Only the exact owning host may create/continue native model work.
	if admit && c.kind != kindCoordinator {
		return 0, nil, forbidden("host admission requires its bound coordinator")
	}
	var l *taskbudget.Ledger
	var err error
	if admit {
		l, err = s.TaskBudgets.Admit(ctx, req.Binding)
	} else if c.kind == kindCoordinator {
		l, err = s.TaskBudgets.ObserveNative(ctx, req.Binding)
	} else {
		l, err = s.TaskBudgets.Observe(ctx, req.Binding)
	}
	if err != nil {
		return 0, nil, budgetError(err)
	}
	status := l.Status()
	status.Admitted = admit
	return http.StatusOK, status, nil
}

func (s *Server) admitTaskBudget(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	return s.taskBudgetControl(ctx, w, r, c, true)
}

func (s *Server) observeTaskBudget(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	return s.taskBudgetControl(ctx, w, r, c, false)
}

func (s *Server) getTaskBudget(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	l, err := s.budgetCaller(ctx, c, r.PathValue("uid"))
	if err != nil {
		return 0, nil, err
	}
	l, err = s.TaskBudgets.Observe(ctx, l.Binding)
	if err != nil {
		return 0, nil, budgetError(err)
	}
	return http.StatusOK, l.Status(), nil
}

func (s *Server) recordTaskBudgetEvent(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	var req apiv1.TaskBudgetEvent
	if err := decodeJSON(w, r, 8192, &req, true); err != nil {
		return 0, nil, err
	}
	if req.Binding.TaskUID != r.PathValue("uid") {
		return 0, nil, badRequest("task UID does not match route")
	}
	if _, err := s.budgetCaller(ctx, c, req.Binding.TaskUID); err != nil {
		return 0, nil, err
	}
	if c.kind != kindCoordinator && c.kind != kindSession {
		return 0, nil, forbidden("task events require a bound executor")
	}
	// Worker creation is reserved for trusted dispatch, never an arbitrary event.
	if req.Kind == "worker-start" {
		return 0, nil, forbidden("worker accounting starts at trusted admission")
	}
	l, err := s.TaskBudgets.Record(ctx, req)
	if err != nil {
		return 0, nil, budgetError(err)
	}
	return http.StatusOK, l.Status(), nil
}

func (s *Server) extendTaskBudget(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindHuman {
		return 0, nil, forbidden("task extension requires the authenticated owner")
	}
	var req apiv1.TaskBudgetExtension
	if err := decodeJSON(w, r, 8192, &req, true); err != nil {
		return 0, nil, err
	}
	if req.Binding.TaskUID != r.PathValue("uid") {
		return 0, nil, badRequest("task UID does not match route")
	}
	if _, err := s.budgetCaller(ctx, c, req.Binding.TaskUID); err != nil {
		return 0, nil, err
	}
	l, err := s.TaskBudgets.Extend(ctx, req, c.identity.Username)
	if err != nil {
		return 0, nil, budgetError(err)
	}
	return http.StatusOK, l.Status(), nil
}

func annotateTaskBudget(sess *v1alpha1.AgentSession, b apiv1.TaskBudgetBinding) {
	if sess.Annotations == nil {
		sess.Annotations = map[string]string{}
	}
	sess.Annotations[TaskBudgetUIDAnnotation] = b.TaskUID
	sess.Annotations[TaskBudgetEpochAnnotation] = strconv.FormatUint(b.Epoch, 10)
	sess.Annotations[TaskBudgetDeadlineAnnotation] = b.Deadline.Format(time.RFC3339Nano)
	sess.Annotations[TaskBudgetHostAnnotation] = b.HostID
	sess.Annotations[TaskBudgetPodAnnotation] = b.PodUID
}

func (s *Server) bindingForSession(sess *v1alpha1.AgentSession) (apiv1.TaskBudgetBinding, error) {
	a := sess.Annotations
	epoch, err := strconv.ParseUint(a[TaskBudgetEpochAnnotation], 10, 64)
	if err != nil {
		return apiv1.TaskBudgetBinding{}, budgetError(taskbudget.ErrDenied)
	}
	d, err := time.Parse(time.RFC3339Nano, a[TaskBudgetDeadlineAnnotation])
	if err != nil {
		return apiv1.TaskBudgetBinding{}, budgetError(taskbudget.ErrDenied)
	}
	return apiv1.TaskBudgetBinding{TaskUID: a[TaskBudgetUIDAnnotation], Epoch: epoch, Deadline: d, HostID: a[TaskBudgetHostAnnotation], PodUID: a[TaskBudgetPodAnnotation]}, nil
}

// budgetForDispatch derives authority from the configured live host or the
// parent's server-written binding. Opt-in refuses every unbound dispatch.
func (s *Server) budgetForDispatch(ctx context.Context, c *caller) (apiv1.TaskBudgetBinding, error) {
	if c.kind == kindCoordinator {
		for _, host := range s.Policy.Coordinators {
			if host.ServiceAccount == c.parent {
				uid := s.AssignedTaskBudgets[host.HostID]
				l, err := s.budgetCaller(ctx, c, uid)
				if err != nil {
					return apiv1.TaskBudgetBinding{}, err
				}
				return l.Binding, nil
			}
		}
	}
	if c.kind == kindSession && c.session != nil {
		return apiv1.TaskBudgetBinding{}, forbidden("first task-budget pilot dispatches direct host children only")
	}
	return apiv1.TaskBudgetBinding{}, forbidden("task dispatch has no assigned logical task")
}

func (s *Server) checkSessionTaskBudget(ctx context.Context, sess *v1alpha1.AgentSession) error {
	if sess.Annotations[TaskBudgetUIDAnnotation] == "" {
		return nil
	}
	b, err := s.bindingForSession(sess)
	if err != nil {
		return err
	}
	if err := s.liveTaskBudgetHost(ctx, b); err != nil {
		return err
	}
	l, err := s.TaskBudgets.Observe(ctx, b)
	if err != nil {
		return budgetError(err)
	}
	if l.Latched {
		return budgetError(taskbudget.ErrDenied)
	}

	for _, worker := range l.Workers {
		if worker.ID == sess.Annotations[TaskBudgetWorkerAnnotation] && worker.Active {
			if worker.Managed != nil && worker.Managed.SessionUID != string(sess.UID) {
				return budgetError(taskbudget.ErrDenied)
			}
			return nil
		}
	}
	return budgetError(taskbudget.ErrDenied)
}

func (s *Server) liveTaskBudgetHost(ctx context.Context, b apiv1.TaskBudgetBinding) error {
	if s.Live == nil || s.AssignedTaskBudgets[b.HostID] != b.TaskUID || !s.Policy.CoordinatorEnabled {
		return budgetError(taskbudget.ErrUnavailable)
	}
	for _, host := range s.Policy.Coordinators {
		if host.HostID != b.HostID {
			continue
		}
		ns, sa, ok := strings.Cut(host.ServiceAccount, "/")
		if !ok {
			return budgetError(taskbudget.ErrUnavailable)
		}
		var pod corev1.Pod
		if s.Live.Get(ctx, client.ObjectKey{Namespace: ns, Name: host.PodName}, &pod) != nil ||
			string(pod.UID) != b.PodUID || !pod.DeletionTimestamp.IsZero() || pod.Spec.ServiceAccountName != sa || pod.Annotations[CoordinatorHostAnnotation] != b.HostID {
			return budgetError(taskbudget.ErrUnavailable)
		}
		return nil
	}
	return budgetError(taskbudget.ErrUnavailable)
}

func (s *Server) callerHasTaskBudget(c *caller) bool {
	if c.kind == kindSession && c.session != nil {
		return c.session.Annotations[TaskBudgetUIDAnnotation] != ""
	}
	if c.kind == kindCoordinator {
		for _, host := range s.Policy.Coordinators {
			if host.ServiceAccount == c.parent {
				return s.AssignedTaskBudgets[host.HostID] != ""
			}
		}
	}
	return false
}
