package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

const maxTaskClaimArchiveListBytes = 4 << 20

func (s *Server) listArchivedTaskClaims(ctx context.Context, project string, limit int64, rawContinue string) (int, any, error) {
	scope := contextArchiveListCursor{Route: apiv1.TaskClaimsPath, Project: project, Limit: limit}
	after, err := decodeContextArchiveListCursor(rawContinue, scope)
	if err != nil {
		return 0, nil, err
	}
	query := protocol.CoordArchiveListQuery{Resource: "taskclaims", Project: project, Limit: int(limit), After: after}
	if query.Validate() != nil {
		return 0, nil, badRequest("invalid archived task claim cursor")
	}
	lister, ok := s.CoordinationArchive.(CoordinationArchiveLister)
	if !ok || lister == nil || ctx.Err() != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	release, err := s.coordinationArchiveSlot(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer release()
	result, err := lister.List(ctx, query)
	if err != nil || ctx.Err() != nil || len(result.Items) > int(limit) {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	envelope, marshalErr := protocol.CanonicalJSON(result)
	if marshalErr != nil || len(envelope) > maxTaskClaimArchiveListBytes {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	if result.Next != "" {
		nextQuery := query
		nextQuery.After = result.Next
		nextName, nextErr := protocol.CoordArchiveListCursorName(nextQuery)
		if nextErr != nil {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
		}
		if after != "" {
			afterName, afterErr := protocol.CoordArchiveListCursorName(query)
			if afterErr != nil || nextName >= afterName {
				return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
			}
		}
	}
	items := make([]apiv1.TaskClaim, 0, len(result.Items))
	for _, item := range result.Items {
		if item.Error == protocol.HistoryErrCorrupt {
			if !protocol.ValidKubeUID(item.UID) || item.Manifest != nil || len(item.Object) != 0 {
				return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
			}
			continue
		}
		if item.Error != "" || item.Manifest == nil || !protocol.ValidKubeUID(item.UID) {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
		}
		claim, archivedAt, verifyErr := s.archivedTaskClaimMetadata(*item.Manifest, item.Object)
		if verifyErr != nil || item.UID != string(claim.UID) || claim.Spec.Identity.Project != project {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
		}
		view := claimMetadata(claim)
		view.Archived, view.ArchivedAt = true, &archivedAt
		items = append(items, view)
	}
	if ctx.Err() != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	next, err := encodeContextArchiveListCursor(result.Next, scope)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, apiv1.TaskClaimList{Items: items, Continue: next}, nil
}

func (s *Server) archivedTaskClaimMetadata(stored protocol.CoordArchiveStoredManifest, object []byte) (*coord.TaskClaim, time.Time, error) {
	canonical, err := protocol.CanonicalJSON(json.RawMessage(object))
	if err != nil || !bytes.Equal(canonical, object) {
		return nil, time.Time{}, errors.New("invalid metadata projection")
	}
	var wire struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if json.Unmarshal(object, &wire) != nil || wire.Spec == nil || wire.Spec["text"] != nil {
		return nil, time.Time{}, errors.New("metadata projection carries text")
	}
	manifest := stored.CoordArchiveManifest
	if _, err := protocol.CoordArchiveRevisionDir(manifest); err != nil || manifest.Resource != "taskclaims" || manifest.Kind != "TaskClaim" || manifest.APIVersion != protocol.CoordArchiveAPIVersion || manifest.Namespace != s.TaskClaims.Namespace || stored.ArchivedAt.IsZero() || stored.ArchivedAt.Location() != time.UTC {
		return nil, time.Time{}, errors.New("invalid coordination archive manifest")
	}
	var claim coord.TaskClaim
	if json.Unmarshal(object, &claim) != nil || claim.APIVersion != manifest.APIVersion || claim.Kind != manifest.Kind || claim.Namespace != manifest.Namespace || claim.Name != manifest.Name || string(claim.UID) != manifest.UID || claim.ResourceVersion != manifest.ResourceVersion || claim.Spec.Identity.Project != manifest.Project || claim.Name != taskclaim.Name(claim.Spec.Identity) || !taskclaim.ValidSpec(claim.Spec) || claim.CreationTimestamp.IsZero() || !claim.DeletionTimestamp.IsZero() {
		return nil, time.Time{}, errors.New("invalid coordination archive task claim metadata")
	}
	return &claim, stored.ArchivedAt, nil
}
