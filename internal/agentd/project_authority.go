package agentd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// ProjectCatalogSource is configured by trusted management, never by a task
// request, caller-provided file, revision or hash. Read must fetch the named
// accepted GitOps ConfigMap through an uncached reader on every invocation.
type ProjectCatalogSource struct {
	Namespace string
	Name      string
	Read      func(context.Context) (ProjectCatalogResource, error)
}

// ProjectCatalogResource binds the exact accepted bytes to their API identity.
// Data is catalog.json, not a caller-generated snapshot or digest claim.
type ProjectCatalogResource struct {
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
	Data            []byte
}

type projectCatalogAuthority struct {
	source   ProjectCatalogSource
	accepted ProjectCatalogResource
}

func captureProjectCatalogAuthority(ctx context.Context, source *ProjectCatalogSource, catalog *projectcatalog.Catalog) (*projectCatalogAuthority, error) {
	if source == nil || source.Read == nil || source.Namespace == "" || source.Name == "" {
		return nil, errors.New("project synchronization requires a trusted configured accepted-catalog reader")
	}
	// Copy the configured key/callback before dispatch. A mutable caller option
	// cannot change which resource this invocation treats as accepted.
	a := &projectCatalogAuthority{source: *source}
	accepted, err := a.read(ctx)
	if err != nil {
		return nil, err
	}
	if catalog == nil || catalog.Revision() != projectcatalog.Digest(accepted.Data) {
		return nil, errors.New("sync catalog does not match the trusted accepted resource")
	}
	// Callback implementations may reuse buffers. Retain independent exact
	// bytes so a later read cannot mutate the original authority in place.
	accepted.Data = slices.Clone(accepted.Data)
	a.accepted = accepted
	return a, nil
}

func (a *projectCatalogAuthority) read(ctx context.Context) (ProjectCatalogResource, error) {
	resource, err := a.source.Read(ctx)
	if err != nil {
		return ProjectCatalogResource{}, fmt.Errorf("accepted project catalog read is unconfirmed: %w", err)
	}
	if resource.Namespace != a.source.Namespace || resource.Name != a.source.Name || resource.UID == "" || resource.ResourceVersion == "" {
		return ProjectCatalogResource{}, errors.New("accepted project catalog resource identity is missing or changed")
	}
	if _, err := projectcatalog.Parse(resource.Data); err != nil {
		return ProjectCatalogResource{}, fmt.Errorf("accepted project catalog data is invalid: %w", err)
	}
	return resource, nil
}

// confirm runs under the existing primary repository administrative lock,
// immediately before project wrappers and their receipt are published.
func (a *projectCatalogAuthority) confirm(ctx context.Context) error {
	current, err := a.read(ctx)
	if err != nil {
		return err
	}
	old := &a.accepted
	if current.UID != old.UID || current.ResourceVersion != old.ResourceVersion || !bytes.Equal(current.Data, old.Data) {
		return errors.New("accepted project catalog changed before publication; project preserved")
	}
	return nil
}
