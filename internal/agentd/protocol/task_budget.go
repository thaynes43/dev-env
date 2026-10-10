package protocol

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

const TaskBudgetEnv = "AGENTD_TASK_BUDGET"

// TaskBudgetDeadline is immutable Pod environment assigned by the controller.
// It authorizes no extension, progress, resume or resource cleanup.
type TaskBudgetDeadline struct {
	Version    int       `json:"version"`
	TaskUID    string    `json:"taskUID"`
	Epoch      uint64    `json:"epoch"`
	Deadline   time.Time `json:"deadline"`
	HostID     string    `json:"hostID"`
	RootPodUID string    `json:"rootPodUID"`
	SessionUID string    `json:"sessionUID"`
}

func ParseTaskBudgetDeadline(raw string, sessionUID, podUID string, now time.Time) (*TaskBudgetDeadline, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 4096 {
		return nil, errors.New("task budget environment exceeds limit")
	}
	var b TaskBudgetDeadline
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("task budget has trailing data")
	}
	if b.Version != 1 || b.Epoch == 0 || b.SessionUID != sessionUID || sessionUID == "" || podUID == "" || b.Deadline.IsZero() || !now.Before(b.Deadline) {
		return nil, errors.New("task budget identity or finite future deadline unavailable")
	}
	for _, id := range []string{b.TaskUID, b.HostID, b.RootPodUID, b.SessionUID, podUID} {
		if len(id) > 128 || !repoName.MatchString(id) {
			return nil, errors.New("invalid task budget identity")
		}
	}
	return &b, nil
}
