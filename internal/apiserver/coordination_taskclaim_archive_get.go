package apiserver

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func taskClaimArchiveUnavailable() error {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
}

// archivedTaskClaim reads one full immutable TaskClaim object through the
// UID-first item seam. It never falls back to legacy Get because that entry
// point can select a revision receipt instead of a UID-first receipt.
func (s *Server) archivedTaskClaim(ctx context.Context, name, uid string) (*coord.TaskClaim, time.Time, error) {
	if ctx.Err() != nil {
		return nil, time.Time{}, taskClaimArchiveUnavailable()
	}
	reader, ok := s.CoordinationArchive.(CoordinationArchiveItemReader)
	if !ok || reader == nil {
		return nil, time.Time{}, taskClaimArchiveUnavailable()
	}
	release, err := s.coordinationArchiveSlot(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer release()
	if ctx.Err() != nil {
		return nil, time.Time{}, taskClaimArchiveUnavailable()
	}
	result, err := reader.GetItem(ctx, protocol.CoordArchiveGetQuery{Resource: "taskclaims", Name: name, UID: uid})
	if ctx.Err() != nil {
		return nil, time.Time{}, taskClaimArchiveUnavailable()
	}
	if err != nil {
		var refusal *protocol.HistoryError
		if errors.As(err, &refusal) && refusal.Code == protocol.HistoryErrNotFound {
			return nil, time.Time{}, notFound("task claim not found")
		}
		return nil, time.Time{}, taskClaimArchiveUnavailable()
	}
	canonical, err := protocol.VerifyCoordArchiveObject(result.Manifest.CoordArchiveManifest, bytes.NewReader(result.Object))
	if err != nil || !bytes.Equal(canonical, result.Object) {
		return nil, time.Time{}, taskClaimArchiveUnavailable()
	}
	manifest := result.Manifest.CoordArchiveManifest
	if manifest.Name != name || (uid != "" && manifest.UID != uid) {
		return nil, time.Time{}, taskClaimArchiveUnavailable()
	}
	claim, archivedAt, err := s.archivedTaskClaimMetadata(result.Manifest, canonical)
	if err != nil {
		return nil, time.Time{}, taskClaimArchiveUnavailable()
	}
	return claim, archivedAt, nil
}

func (s *Server) archivedTaskClaimView(ctx context.Context, c *caller, project, name, uid string) (int, any, error) {
	claim, archivedAt, err := s.archivedTaskClaim(ctx, name, uid)
	if ctx.Err() != nil {
		return 0, nil, taskClaimArchiveUnavailable()
	}
	// Recheck the live caller binding and project grant after shelf I/O.
	if scopeErr := s.claimReadScope(ctx, c, project); scopeErr != nil {
		return 0, nil, scopeErr
	}
	if err != nil {
		return 0, nil, err
	}
	if claim.Spec.Identity.Project != project {
		return 0, nil, notFound("task claim not found")
	}
	view := claimMetadata(claim)
	view.Archived, view.ArchivedAt = true, &archivedAt
	return http.StatusOK, view, nil
}
