package apiv1

import "time"

const TaskBudgetsPath = "/v1/task-budgets"

// These are the bounded first pilot's server-side ceilings. An executor cannot
// choose a larger initial campaign budget by supplying a different request.
const (
	TaskBudgetPilotOverallSeconds    int64 = 45 * 60
	TaskBudgetPilotEffortSeconds     int64 = 60 * 60
	TaskBudgetPilotCheckpointSeconds int64 = 10 * 60
)

func TaskBudgetPath(uid string) string { return TaskBudgetsPath + "/" + uid }

// TaskBudgetBinding fences one assigned logical task on one owned host. Epoch
// changes require a validated owner decision; a Pod replacement is not a reset.
type TaskBudgetBinding struct {
	TaskUID  string    `json:"taskUID"`
	Epoch    uint64    `json:"epoch"`
	Deadline time.Time `json:"deadline"`
	HostID   string    `json:"hostID"`
	PodUID   string    `json:"podUID"`
}

type TaskBudgetSpec struct {
	SuccessCondition  string `json:"successCondition"`
	OverallSeconds    int64  `json:"overallSeconds"`
	EffortSeconds     int64  `json:"effortSeconds"`
	CheckpointSeconds int64  `json:"checkpointSeconds"`
}

type CreateTaskBudgetRequest struct {
	TaskUID string         `json:"taskUID"`
	Spec    TaskBudgetSpec `json:"spec"`
}

type TaskBudgetRequest struct {
	Binding TaskBudgetBinding `json:"binding"`
}

// Evidence is a reference to a receipt checked by the server's validator, never
// a caller's declaration of progress or process termination.
type TaskBudgetEvidence struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
}

type TaskBudgetEvent struct {
	Binding   TaskBudgetBinding  `json:"binding"`
	ID        string             `json:"id"`
	Kind      string             `json:"kind"`
	WorkerID  string             `json:"workerID,omitempty"`
	AttemptID string             `json:"attemptID,omitempty"`
	BlockerID string             `json:"blockerID,omitempty"`
	Evidence  TaskBudgetEvidence `json:"evidence,omitempty"`
}

type TaskBudgetExtension struct {
	Binding           TaskBudgetBinding  `json:"binding"`
	DecisionID        string             `json:"decisionID"`
	NextStep          string             `json:"nextStep"`
	OverallSeconds    int64              `json:"overallSeconds"`
	EffortSeconds     int64              `json:"effortSeconds"`
	CheckpointSeconds int64              `json:"checkpointSeconds"`
	Evidence          TaskBudgetEvidence `json:"evidence"`
}

type TaskBudgetEscalation struct {
	ID       string    `json:"id"`
	Epoch    uint64    `json:"epoch"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
	Delivery string    `json:"delivery"`
}

type TaskBudgetStatus struct {
	Observed           bool                  `json:"observed"`
	Admitted           bool                  `json:"admitted"`
	Binding            TaskBudgetBinding     `json:"binding"`
	Latched            bool                  `json:"latched"`
	Reason             string                `json:"reason,omitempty"`
	EffortMilliseconds int64                 `json:"effortMilliseconds"`
	UsageAccounting    string                `json:"usageAccounting"`
	LastProgress       time.Time             `json:"lastProgress"`
	NextCheckpoint     time.Time             `json:"nextCheckpoint"`
	Escalation         *TaskBudgetEscalation `json:"escalation,omitempty"`
	StopConfirmed      bool                  `json:"stopConfirmed"`
	Blockers           []TaskBudgetBlocker   `json:"blockers,omitempty"`
	LastEvidence       *TaskBudgetEvidence   `json:"lastEvidence,omitempty"`
}

type TaskBudgetBlocker struct {
	ID             string `json:"id"`
	FailedAttempts int    `json:"failedAttempts"`
	Resolved       bool   `json:"resolved"`
}
