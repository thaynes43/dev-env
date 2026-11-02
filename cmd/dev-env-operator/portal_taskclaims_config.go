package main

import (
	"errors"
	"slices"

	"github.com/thaynes43/dev-env/internal/apiserver"
)

// parsePortalTaskClaims reads --enable-portal-task-claims and
// --portal-task-claims-projects (DESIGN-045). Default off. Enabling needs the
// portal reader, task claims and at least one project, and every project must
// already be in --task-claims-read-projects, so widening claim scope never
// widens the portal by accident. A project list without the enable refuses.
// Source wiring is not a deployed permission.
func parsePortalTaskClaims(enabled bool, raw string, portalReader, taskClaims bool, readProjects []string) (apiserver.PortalTaskClaimsConfig, error) {
	if !enabled {
		if raw != "" {
			return apiserver.PortalTaskClaimsConfig{}, errors.New("--portal-task-claims-projects requires --enable-portal-task-claims")
		}
		return apiserver.PortalTaskClaimsConfig{}, nil
	}
	projects, err := apiserver.ParseTaskClaimReadProjects(raw)
	if err != nil {
		return apiserver.PortalTaskClaimsConfig{}, err
	}
	if !portalReader || !taskClaims || len(projects) == 0 {
		return apiserver.PortalTaskClaimsConfig{}, errors.New("--enable-portal-task-claims requires --enable-portal-reader, --enable-task-claims and at least one --portal-task-claims-projects entry")
	}
	for _, project := range projects {
		if !slices.Contains(readProjects, project) {
			return apiserver.PortalTaskClaimsConfig{}, errors.New("--portal-task-claims-projects must be a subset of --task-claims-read-projects")
		}
	}
	return apiserver.PortalTaskClaimsConfig{Enabled: true, Projects: projects}, nil
}
