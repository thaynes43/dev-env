package controller

import (
	"fmt"
	"slices"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// interruptedBootstrap decides whether a rescue that found no clone of the session's
// repository can truthfully be called an interrupted bootstrap, from agentd's NoCloneProof.
// It is strict: any field it does not recognise, any count above zero, a repository the
// record calls Ready, or a record that names no reason for a repository it does not list
// returns false, and the caller keeps NotProven. It changes a reason and a message only: the
// result stays RescueFailed, so the volume is never counted as rescued (and never becomes
// archivable or deletable by this path). Whether such a volume should count as rescued is an
// open decision for Tom (DESIGN-001, rescue).
func interruptedBootstrap(s *v1alpha1.AgentSession, rep protocol.RescueReport) (string, bool) {
	p := rep.NoClone
	switch {
	case p == nil, p.Version != 1, p.Repo == "", p.Repo != s.Spec.Repo:
		return "", false
	case rep.Agent == nil || rep.Agent.Running:
		return "", false
	case len(rep.Repos) != 0 || rep.VolumeEmpty || rep.Bundle != nil || !rep.OK || !rep.CleanAndPushed:
		return "", false
	case p.ReposEntries != 0 || p.WorkEntries != 0:
		return "", false
	case p.BootstrapState != protocol.BootstrapPartial && p.BootstrapState != protocol.BootstrapFailed:
		return "", false
	}
	if p.RepositoryState == "" {
		// The record stopped before it reached this repository: it must say why.
		if p.RepositoryStep != "" || p.RepositoryReason != "" || !slices.Contains(protocol.CatalogReasons, p.BootstrapReason) {
			return "", false
		}
	} else {
		if p.RepositoryState == protocol.BootstrapReady || !slices.Contains(protocol.BootstrapRepositoryStates, p.RepositoryState) ||
			(p.RepositoryStep != "" && !slices.Contains(protocol.BootstrapSteps, p.RepositoryStep)) ||
			(p.RepositoryReason != "" && !slices.Contains(protocol.RepositoryReasons, p.RepositoryReason)) {
			return "", false
		}
	}
	if p.BootstrapReason != "" && !slices.Contains(protocol.CatalogReasons, p.BootstrapReason) {
		return "", false
	}
	why := p.BootstrapState
	if p.BootstrapReason != "" {
		why += " " + p.BootstrapReason
	}
	if p.RepositoryState != "" {
		why += "; repository " + p.RepositoryState
		if p.RepositoryReason != "" {
			why += " " + p.RepositoryReason
		}
	}
	return fmt.Sprintf("interrupted bootstrap: the declared clone ~/repos/%s was never made (~/repos and ~/work hold nothing; bootstrap record: %s); "+
		"no Git work to rescue in the rescue's scope, ~/repos; HOME preserved and the volume is not counted as rescued", s.Spec.Repo, why), true
}
