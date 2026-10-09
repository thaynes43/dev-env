package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const workspaceStopRequestFile = "workspace-stop-request.json"

type workspaceStopRequest struct {
	Version     int       `json:"version"`
	Workspace   string    `json:"workspace"`
	Task        string    `json:"task"`
	SessionUID  string    `json:"sessionUID"`
	PodUID      string    `json:"podUID"`
	Generation  uint64    `json:"generation"`
	RequestedAt time.Time `json:"requestedAt"`
}

// RequestWorkspaceStop requests supervisor exit without deleting the Pod or
// volumes. It uses only exact operator bindings and private state, so even a
// refused mount/owner admission can stop. It never changes shared ownership;
// only the controller can later turn actual container termination into proof.
func RequestWorkspaceStop(ctx context.Context, s Settings, sess protocol.Session, now time.Time) error {
	if sess.Workspace == nil || sess.Workspace.StopProof != nil || s.WorkspaceID == "" || sess.Workspace.ID != s.WorkspaceID || s.PodUID == "" {
		return errors.New("stop request requires a shared executor, not a hold Pod")
	}
	if err := sess.Validate(); err != nil {
		return err
	}
	unlock, err := workspaceSupervisorLock(ctx, s)
	if err != nil {
		return err
	}
	defer unlock()
	var old workspaceStopRequest
	if readWorkspaceJSON(s.statePath(workspaceStopRequestFile), &old) == nil && old.Version == workspaceVersion && old.Workspace == s.WorkspaceID && old.Task == sess.Name && old.SessionUID == sess.Workspace.SessionUID && old.PodUID == s.PodUID && old.Generation == 0 && !old.RequestedAt.IsZero() {
		return nil
	}
	// Generation zero requests this exact supervisor Pod, including the case
	// where no shared owner was ever admitted. A later Pod UID ignores it.
	request := workspaceStopRequest{workspaceVersion, s.WorkspaceID, sess.Name, sess.Workspace.SessionUID, s.PodUID, 0, now.UTC()}
	return writeWorkspaceJSON(s.statePath(workspaceStopRequestFile), request)
}

// This private gate is never held during shared Git commands. Admission and
// final CLI launch take shared administration first, then this gate; the stop
// requester takes only the gate, so it cannot wait on or mutate a peer owner.
func workspaceSupervisorLock(ctx context.Context, s Settings) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unlock, err := workspaceFileLock(s.StateDir, "workspace-supervisor")
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, err
		}
		if err := pauseWorkspaceAdmission(ctx, 100*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

func workspaceStopRequested(s Settings, sess protocol.Session) (bool, error) {
	if sess.Workspace == nil {
		return false, nil
	}
	owner := taskOwner{}
	if s.writer != nil {
		owner = s.writer.owner
	}
	return workspaceStopForOwner(s, sess, owner)
}

func workspaceStopForOwner(s Settings, sess protocol.Session, owner taskOwner) (bool, error) {
	var request workspaceStopRequest
	err := readWorkspaceJSON(s.statePath(workspaceStopRequestFile), &request)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("uncertain workspace stop request: %w", err)
	}
	// A later verified resume sees the previous Pod's retained request. It
	// cannot stop the new generation merely because its home was retained.
	if request.PodUID != s.PodUID {
		return false, nil
	}
	if sess.Workspace == nil || request.Version != workspaceVersion || request.Workspace != s.WorkspaceID || request.Task != sess.Name ||
		request.SessionUID != sess.Workspace.SessionUID || (request.Generation != 0 && request.Generation != owner.Generation) || request.RequestedAt.IsZero() {
		return false, errors.New("stop request does not match this writer generation")
	}
	return true, nil
}
