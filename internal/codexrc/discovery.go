// Package codexrc maps caller-authorized, inert discovery observations to a
// unique existing Codex project. It performs no I/O and grants no access.
package codexrc

import (
	"encoding/json"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const SupportedCLIVersion = "0.160.1"

type Outcome string

const (
	Matched            Outcome = "matched"
	Ambiguous          Outcome = "ambiguous"
	None               Outcome = "none"
	PairingRequired    Outcome = "pairing_required"
	PairingUnknown     Outcome = "pairing_unknown"
	UnsupportedVersion Outcome = "unsupported_cli_version"
	InvalidInventory   Outcome = "invalid_inventory"
	InvalidTarget      Outcome = "invalid_target"
)

type PairingState string

const (
	Paired          PairingState = "paired"
	RequiresPairing PairingState = "required"
)

// PairingEvidence is a supported client's observation, independently bound to
// the exact host and installation by the caller. A connected daemon, an old
// code, or remoteControl/pairing/status's code-claimed boolean is insufficient.
// Missing/unsupported evidence stays unknown. This package verifies no source
// authenticity or freshness; the authorized caller must do so before calling.
type PairingEvidence struct {
	State          PairingState
	HostID         string
	InstallationID string
	EvidenceSource string
}

// HostInventory is an already authorized host observation, NOT an app-server
// wire type. ProjectKind ("codex" or "chatgpt") and HostID are supplied by the
// caller's supported inventory. Native project/list has neither field.
// Repository/RepositoryRoot/CatalogRevision attest the accepted repository's
// binding to this host's root; names, paths and native metadata cannot establish it.
// InstallationID is the expected native identity, never an alias for HostID.
// SessionName is an optional verified managed-session mapping for owner action.
// ProjectPages contains every project/list result in cursor order. Status is a
// remoteControl/status/read result. Both are decoded against the pinned shape.
type HostInventory struct {
	ProjectKind     string
	HostID          *string
	InstallationID  string
	SessionName     string
	CLIVersion      string
	CatalogRevision string
	Repository      projectcatalog.Repository
	RepositoryRoot  string
	Status          json.RawMessage
	ProjectPages    []json.RawMessage
	Pairing         PairingEvidence
}

// Target requires a server-accepted catalog snapshot with a selected repository
// and its caller-authorized absolute, normalized POSIX root on the host. No
// filesystem existence, symlink resolution or Git identity is inferred here.
type Target struct {
	Snapshot projectcatalog.Snapshot
	CWD      string
}

type Project struct {
	Kind           string `json:"kind"`
	ProjectID      string `json:"projectId"`
	HostID         string `json:"hostId"`
	InstallationID string `json:"installationId"`
	CWD            string `json:"cwd"`
}

// Result never selects a project for a refusal, including an ambiguity.
// OwnerAction is populated only for a verified pairing-required managed host;
// it contains the exact `agent-run pair SESSION` argv. It is never executed.
type Result struct {
	Outcome     Outcome
	Project     *Project
	OwnerAction []string
}

// RCLaunchError projects discovery into rc-launch.v1 without wiring rc start.
// A successful mapping is discovery only, not a thread, link or readiness claim.
func (r Result) RCLaunchError() *apiv1.RCLaunchError {
	code, message, phase := "protocol_unsupported", "Codex discovery protocol evidence is invalid or incomplete", "project_inventory_ok"
	switch r.Outcome {
	case Matched:
		return nil
	case None, Ambiguous:
		code, message = "codex_inventory_unavailable", "No unique Codex remote project maps to the accepted repository and root"
	case PairingUnknown:
		code, message, phase = "pairing_unknown", "Supported client evidence does not establish phone pairing; do not re-pair on uncertainty", "pairing_required"
	case PairingRequired:
		code, message, phase = "pairing_required", "The selected host requires phone pairing by an authorized operator", "pairing_required"
		if len(r.OwnerAction) == 0 {
			code, message = "pairing_action_unavailable", "The selected host has no verified managed-session pairing action"
		}
	case UnsupportedVersion:
		message = "Codex discovery requires codex-cli 0.160.1"
	case InvalidTarget:
		code, message = "project_unavailable", "An accepted catalog repository and normalized root are required"
	}
	return &apiv1.RCLaunchError{Code: code, Message: message, Phase: phase}
}

// Discover considers the complete supplied catalog-bound inventory. It requires
// a unique host/project/root before evaluating pairing, so unknown candidates
// cannot silently resolve ambiguity. It neither probes nor repairs any host.
func Discover(target Target, hosts []HostInventory) Result {
	repo := target.Snapshot.Selected()
	if repo.Name == "" || target.Snapshot.CatalogRevision() == "" || !absoluteRoot(target.CWD) {
		return Result{Outcome: InvalidTarget}
	}
	if len(hosts) > 64 {
		return Result{Outcome: InvalidInventory}
	}
	type candidate struct {
		project Project
		host    HostInventory
		ready   bool
	}
	var matches []candidate
	seenHosts := map[string]bool{}
	for _, host := range hosts {
		// In particular, the Mac's ChatGPT-kind/null-host inventory cannot
		// become Codex projects by name or by parsing its IDs as native IDs.
		if host.ProjectKind != "codex" || host.HostID == nil || !identifier(*host.HostID) {
			continue
		}
		if host.CatalogRevision != target.Snapshot.CatalogRevision() || host.Repository != repo || host.RepositoryRoot != target.CWD {
			continue
		}
		if host.CLIVersion != SupportedCLIVersion {
			return Result{Outcome: UnsupportedVersion}
		}
		if seenHosts[*host.HostID] || !identifier(host.InstallationID) {
			return Result{Outcome: InvalidInventory}
		}
		seenHosts[*host.HostID] = true
		status, err := decodeStatus(host.Status)
		if err != nil || status.InstallationID != host.InstallationID {
			return Result{Outcome: InvalidInventory}
		}
		projects, err := decodeProjects(host.ProjectPages)
		if err != nil {
			return Result{Outcome: InvalidInventory}
		}
		ready := status.Status == "connected" && status.EnvironmentID != nil && identifier(*status.EnvironmentID)
		for _, project := range projects {
			for _, root := range project.Roots {
				if root.Path == target.CWD {
					matches = append(matches, candidate{Project{Kind: "codex", ProjectID: project.ID, HostID: *host.HostID, InstallationID: host.InstallationID, CWD: root.Path}, host, ready})
					break
				}
			}
		}
	}
	if len(matches) == 0 {
		return Result{Outcome: None}
	}
	if len(matches) != 1 {
		return Result{Outcome: Ambiguous}
	}
	m := matches[0]
	if !m.ready {
		return Result{Outcome: None}
	}
	pair := m.host.Pairing
	if pair.HostID != m.project.HostID || pair.InstallationID != m.project.InstallationID || !identifier(pair.EvidenceSource) {
		return Result{Outcome: PairingUnknown}
	}
	switch pair.State {
	case Paired:
		return Result{Outcome: Matched, Project: &m.project}
	case RequiresPairing:
		session := m.host.SessionName
		if !projectcatalog.ValidName(session) {
			return Result{Outcome: PairingRequired}
		}
		return Result{Outcome: PairingRequired, OwnerAction: []string{"agent-run", "pair", session}}
	default:
		return Result{Outcome: PairingUnknown}
	}
}

func identifier(s string) bool {
	if s == "" || len(s) > 512 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r >= 0x7f && r <= 0x9f {
			return false
		}
	}
	return true
}

func absoluteRoot(s string) bool {
	return len(s) <= 4096 && identifierPath(s) && path.IsAbs(s) && path.Clean(s) == s && !strings.Contains(s, "\\")
}

func identifierPath(s string) bool {
	if s == "" || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r >= 0x7f && r <= 0x9f {
			return false
		}
	}
	return true
}
