package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const workspaceAdmissionFile = "workspace-admission.json"

type workspaceAdmission struct {
	Version    int       `json:"version"`
	Workspace  string    `json:"workspace"`
	Task       string    `json:"task"`
	SessionUID string    `json:"sessionUID"`
	PodUID     string    `json:"podUID"`
	Phase      string    `json:"phase"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

var writeWorkspaceAdmission = writeWorkspaceJSON

// A failed private result publication is an ordinary admission failure, even
// if the original shared-lock wait expired. It must not acquire retry authority
// merely by wrapping that distinct administrative timeout.
func workspaceAdmissionResultFailure(admission, publication error) error {
	return fmt.Errorf("workspace admission failed (%s); private non-admission result: %w", admission.Error(), publication)
}

func admissionMatches(s Settings, sess protocol.Session, a workspaceAdmission, podUID string) error {
	if sess.Workspace == nil || a.Version != workspaceVersion || a.Workspace != s.WorkspaceID || a.Task != sess.Name ||
		a.SessionUID != sess.Workspace.SessionUID || a.PodUID != podUID || a.UpdatedAt.IsZero() {
		return errors.New("private admission result does not match the exact executor")
	}
	switch a.Phase {
	case "Checking", "Refused", "OwnerWriteStarted", "OwnerAdmitted":
		return nil
	default:
		return errors.New("private admission result has an unknown phase")
	}
}

func writeAdmissionPhase(s Settings, sess protocol.Session, phase string, now time.Time) error {
	a := workspaceAdmission{workspaceVersion, s.WorkspaceID, sess.Name, sess.Workspace.SessionUID, s.PodUID, phase, now.UTC()}
	return writeWorkspaceAdmission(s.statePath(workspaceAdmissionFile), a)
}

// Checking precedes every shared admission attempt. A same-Pod uncertain write
// cannot be downgraded, even after a lock becomes free or the daemon restarts.
func beginWorkspaceAdmission(ctx context.Context, s Settings, sess protocol.Session, now time.Time) (bool, error) {
	gate, err := workspaceSupervisorLock(ctx, s)
	if err != nil {
		return false, err
	}
	defer gate()
	var old workspaceAdmission
	err = readWorkspaceJSON(s.statePath(workspaceAdmissionFile), &old)
	if err == nil {
		if err := admissionMatches(s, sess, old, old.PodUID); err != nil {
			return false, err
		}
		if old.PodUID == s.PodUID {
			switch old.Phase {
			case "OwnerWriteStarted":
				return false, errors.New("previous owner write is ambiguous; admission refused")
			case "OwnerAdmitted":
				return false, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("uncertain private admission result: %w", err)
	}
	if err := writeAdmissionPhase(s, sess, "Checking", now); err != nil {
		return false, err
	}
	return true, nil
}

func refuseWorkspaceAdmission(s Settings, sess protocol.Session, now time.Time) error {
	gate, err := workspaceSupervisorLock(context.Background(), s)
	if err != nil {
		return err
	}
	defer gate()
	var a workspaceAdmission
	if err := readWorkspaceJSON(s.statePath(workspaceAdmissionFile), &a); err != nil {
		return err
	}
	if err := admissionMatches(s, sess, a, s.PodUID); err != nil {
		return err
	}
	if a.Phase != "Checking" {
		return errors.New("an attempted owner write cannot become non-admission proof")
	}
	return writeAdmissionPhase(s, sess, "Refused", now)
}
