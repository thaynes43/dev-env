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
	"github.com/thaynes43/dev-env/internal/contextpublication"
)

const coordinationArchiveSlotWait = 2 * time.Second

// CoordinationArchiveReader is shelf.CoordinationArchive's typed Get contract.
// The server owns the shared exec slot, so the transport never creates one.
type CoordinationArchiveReader interface {
	Get(context.Context, protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error)
}

func (s *Server) coordinationArchiveSlot(ctx context.Context) (func(), error) {
	if s.CoordinationArchive == nil || s.ShelfSlots == nil {
		return nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, coordinationArchiveSlotWait)
	release, err := s.ShelfSlots.Take(ctx)
	if err != nil {
		cancel()
		return nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	return func() {
		release()
		cancel()
	}, nil
}

func (s *Server) archivedPublishedContext(ctx context.Context, name, project string) (*coord.PublishedContext, time.Time, error) {
	if ctx.Err() != nil {
		return nil, time.Time{}, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	release, err := s.coordinationArchiveSlot(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer release()
	if ctx.Err() != nil {
		return nil, time.Time{}, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	result, err := s.CoordinationArchive.Get(ctx, protocol.CoordArchiveGetQuery{Resource: "publishedcontexts", Name: name})
	if ctx.Err() != nil {
		return nil, time.Time{}, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	if err != nil {
		var refusal *protocol.HistoryError
		if errors.As(err, &refusal) && refusal.Code == protocol.HistoryErrNotFound {
			return nil, time.Time{}, notFound("published context not found")
		}
		return nil, time.Time{}, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	canonical, err := protocol.VerifyCoordArchiveObject(result.Manifest.CoordArchiveManifest, bytes.NewReader(result.Object))
	if err != nil || !bytes.Equal(canonical, result.Object) {
		return nil, time.Time{}, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	manifest := result.Manifest.CoordArchiveManifest
	if manifest.Resource != "publishedcontexts" || manifest.Kind != "PublishedContext" || manifest.APIVersion != "coordination.dev-env.haynesops.com/v1alpha1" || manifest.Namespace != s.PublishedContexts.Namespace || manifest.Name != name || result.Manifest.ArchivedAt.IsZero() || result.Manifest.ArchivedAt.Location() != time.UTC {
		return nil, time.Time{}, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	var archived coord.PublishedContext
	if json.Unmarshal(canonical, &archived) != nil || archived.APIVersion != manifest.APIVersion || archived.Kind != manifest.Kind || archived.Namespace != manifest.Namespace || archived.Name != manifest.Name || string(archived.UID) != manifest.UID || archived.ResourceVersion != manifest.ResourceVersion || archived.Spec.Project != manifest.Project || archived.Name != contextpublication.Name(archived.Spec.Identity) || !contextpublication.ValidSpec(archived.Spec) || archived.CreationTimestamp.IsZero() || !archived.DeletionTimestamp.IsZero() {
		return nil, time.Time{}, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	if ctx.Err() != nil {
		return nil, time.Time{}, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	if manifest.Project != project {
		return nil, time.Time{}, notFound("published context not found")
	}
	return &archived, result.Manifest.ArchivedAt, nil
}

func archivedContextMetadata(p *coord.PublishedContext, archivedAt time.Time) apiv1.ContextMetadata {
	m := contextMetadata(p)
	m.Archived = true
	m.ArchivedAt = &archivedAt
	// Keep immutable attribution; do not join a possibly absent source Session.
	return m
}
