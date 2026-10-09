package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
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
// volumes. Only the controller can later turn actual termination into proof.
func RequestWorkspaceStop(ctx context.Context, s Settings, sess protocol.Session, now time.Time) error {
	if err := workspacePreflight(s, sess); err != nil {
		return err
	}
	if sess.Workspace == nil || sess.Workspace.StopProof != nil {
		return errors.New("stop request requires a shared executor, not a hold Pod")
	}
	// The controller's exec budget is 35 seconds. A stop request never waits
	// longer than 30 seconds for admission serialization; it can retry later.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock, err := workspaceAdminLock(ctx, s, sess.Repo)
	if err != nil {
		return err
	}
	defer unlock()
	var owner taskOwner
	if err := readWorkspaceJSON(s.ownerPath(sess.Name), &owner); err != nil {
		return err
	}
	if err := ownerMatches(s, sess, owner, true); err != nil {
		return err
	}
	if owner.State != "owned" {
		return errors.New("task is already stopped")
	}
	var old workspaceStopRequest
	if readWorkspaceJSON(s.statePath(workspaceStopRequestFile), &old) == nil && old.Version == workspaceVersion && old.Workspace == s.WorkspaceID && old.Task == sess.Name && old.SessionUID == sess.Workspace.SessionUID && old.PodUID == s.PodUID && old.Generation == owner.Generation && !old.RequestedAt.IsZero() {
		return nil
	}
	request := workspaceStopRequest{workspaceVersion, s.WorkspaceID, sess.Name, sess.Workspace.SessionUID, s.PodUID, owner.Generation, now.UTC()}
	return writeWorkspaceJSON(s.statePath(workspaceStopRequestFile), request)
}

func workspaceStopRequested(s Settings, sess protocol.Session) (bool, error) {
	if sess.Workspace == nil {
		return false, nil
	}
	if s.writer == nil {
		return false, nil
	}
	return workspaceStopForOwner(s, sess, s.writer.owner)
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
		request.SessionUID != sess.Workspace.SessionUID || request.Generation != owner.Generation || request.RequestedAt.IsZero() {
		return false, errors.New("stop request does not match this writer generation")
	}
	return true, nil
}
