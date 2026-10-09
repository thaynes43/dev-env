package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// CatalogBinding is explicit operator configuration. Admission always resolves
// through the uncached reader and this named ConfigMap, including on retries.
type CatalogBinding struct {
	Key        types.NamespacedName
	CloneOwner string
}

func (b CatalogBinding) Validate() error {
	if b.Key.Namespace != "dev-env-system" || b.Key.Name != "dev-env-project-catalog" || !projectcatalog.ValidName(b.CloneOwner) {
		return errors.New("accepted catalog requires dev-env-system/dev-env-project-catalog and a configured clone owner")
	}
	return nil
}

func (b CatalogBinding) snapshot(ctx context.Context, live client.Reader, project, repo string) ([]byte, projectcatalog.Repository, error) {
	if live == nil || b.Validate() != nil {
		return nil, projectcatalog.Repository{}, errors.New("accepted project catalog is unavailable")
	}
	var cm corev1.ConfigMap
	if err := live.Get(ctx, b.Key, &cm); err != nil || !cm.DeletionTimestamp.IsZero() {
		return nil, projectcatalog.Repository{}, errors.New("accepted project catalog is unavailable")
	}
	catalog, err := projectcatalog.Parse([]byte(cm.Data["catalog.json"]))
	if err != nil {
		return nil, projectcatalog.Repository{}, err
	}
	snapshot, err := catalog.Snapshot(project, repo)
	if err != nil {
		return nil, projectcatalog.Repository{}, err
	}
	selected := snapshot.Selected()
	if strings.Split(selected.GitHub, "/")[0] != b.CloneOwner {
		return nil, projectcatalog.Repository{}, errors.New("selected repository is outside the configured clone owner")
	}
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > projectcatalog.MaxSnapshotBytes {
		return nil, projectcatalog.Repository{}, errors.New("accepted project snapshot exceeds its transport bound")
	}
	return data, selected, nil
}
