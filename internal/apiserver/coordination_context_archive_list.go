package apiserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/contextpublication"
)

const maxContextArchiveListBytes = 4 << 20

type contextArchiveListCursor struct {
	Route     string `json:"route"`
	Project   string `json:"project"`
	Recipient string `json:"recipient"`
	Limit     int64  `json:"limit"`
	Continue  string `json:"continue"`
}

func decodeContextArchiveListCursor(raw string, scope contextArchiveListCursor) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 4096 {
		return "", badRequest("invalid archived context cursor")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return "", badRequest("invalid archived context cursor")
	}
	var decoded contextArchiveListCursor
	if json.Unmarshal(data, &decoded) != nil {
		return "", badRequest("invalid archived context cursor")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || string(canonical) != string(data) || decoded.Continue == "" {
		return "", badRequest("invalid archived context cursor")
	}
	continuation := decoded.Continue
	decoded.Continue = ""
	if decoded != scope {
		return "", badRequest("archived context cursor belongs to another query")
	}
	return continuation, nil
}

func encodeContextArchiveListCursor(next string, scope contextArchiveListCursor) (string, error) {
	if next == "" {
		return "", nil
	}
	scope.Continue = next
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > 4096 {
		return "", newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	return encoded, nil
}

func (s *Server) listArchivedPublishedContexts(ctx context.Context, project string, limit int64, rawContinue string) (int, any, error) {
	scope := contextArchiveListCursor{Route: apiv1.PublishedContextsPath, Project: project, Limit: limit}
	after, err := decodeContextArchiveListCursor(rawContinue, scope)
	if err != nil {
		return 0, nil, err
	}
	query := protocol.CoordArchiveListQuery{Resource: "publishedcontexts", Project: project, Limit: int(limit), After: after}
	if query.Validate() != nil {
		return 0, nil, badRequest("invalid archived context cursor")
	}
	lister, ok := s.CoordinationArchive.(CoordinationArchiveLister)
	if !ok || lister == nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	if ctx.Err() != nil {
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
	if marshalErr != nil || len(envelope) > maxContextArchiveListBytes {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	if result.Next != "" {
		if result.Next == after {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
		}
		nextQuery := query
		nextQuery.After = result.Next
		if nextQuery.Validate() != nil {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
		}
	}
	items := make([]apiv1.ContextMetadata, 0, len(result.Items))
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
		archived, archivedAt, verifyErr := s.archivedPublishedContextMetadata(*item.Manifest, item.Object)
		if verifyErr != nil || item.UID != string(archived.UID) || archived.Spec.Project != project {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
		}
		items = append(items, archivedContextMetadata(archived, archivedAt))
	}
	if ctx.Err() != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "coordination archive unavailable")
	}
	next, err := encodeContextArchiveListCursor(result.Next, scope)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, apiv1.PublishedContextList{Items: items, Continue: next}, nil
}

func (s *Server) archivedPublishedContextMetadata(stored protocol.CoordArchiveStoredManifest, object []byte) (*coord.PublishedContext, time.Time, error) {
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
	if _, err := protocol.CoordArchiveRevisionDir(manifest); err != nil || manifest.Resource != "publishedcontexts" || manifest.Kind != "PublishedContext" || manifest.APIVersion != protocol.CoordArchiveAPIVersion || manifest.Namespace != s.PublishedContexts.Namespace || stored.ArchivedAt.IsZero() || stored.ArchivedAt.Location() != time.UTC {
		return nil, time.Time{}, errors.New("invalid coordination archive manifest")
	}
	var archived coord.PublishedContext
	if json.Unmarshal(object, &archived) != nil || archived.APIVersion != manifest.APIVersion || archived.Kind != manifest.Kind || archived.Namespace != manifest.Namespace || archived.Name != manifest.Name || string(archived.UID) != manifest.UID || archived.ResourceVersion != manifest.ResourceVersion || archived.Spec.Project != manifest.Project || archived.Name != contextpublication.Name(archived.Spec.Identity) || !contextpublication.ValidArchiveMetadata(archived.Spec) || archived.CreationTimestamp.IsZero() || !archived.DeletionTimestamp.IsZero() {
		return nil, time.Time{}, errors.New("invalid coordination archive context metadata")
	}
	return &archived, stored.ArchivedAt, nil
}
