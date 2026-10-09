package protocol

import (
	"path"
	"time"
)

// RescueReport is what `agentd ctl rescue` prints (D-43, D-48): step 1 of D-10's
// rescue, the list of refs a bundle must cover, and the bundle that covers them.
type RescueReport struct {
	WorkspacePreservation *WorkspacePreservation `json:"workspacePreservation,omitempty"`
	// SourcePodUID is the retained executor proven terminated before a
	// distinct hold Pod rescued its shared task.
	SourcePodUID string       `json:"sourcePodUID,omitempty"`
	Session      string       `json:"session"`
	Stamp        string       `json:"stamp"`
	StartedAt    time.Time    `json:"startedAt"`
	FinishedAt   time.Time    `json:"finishedAt"`
	Repos        []RepoRescue `json:"repos"`
	// OK is set when every worktree is clean or rescued, no repo failed, and
	// every bundle that was needed was written and verified. A rescue that is
	// not OK still leaves the volume as it was; the operator marks the
	// session rescueFailed, which blocks archive (D-10).
	OK bool `json:"ok"`
	// CleanAndPushed is D-10's proof that nothing needs a bundle: every repo
	// fetched, every worktree clean, and no local ref holds a commit origin
	// lacks.
	CleanAndPushed bool `json:"cleanAndPushed"`
	// VolumeEmpty is the proof for a volume no pod ever wrote to (D-55): it
	// holds nothing but an empty lost+found and the shared volume's mount
	// point, so there is no clone and nothing to save. Such a report has no
	// repos, and the rescue wrote nothing, not even agentd's state directory.
	VolumeEmpty bool `json:"volumeEmpty,omitempty"`
	// Agent is what `--stop-agent` did to the agent CLI before the rescue
	// began. It is nil without the flag.
	Agent *AgentStop `json:"agent,omitempty"`
	// Bundle is D-10 step 2: where this rescue's bundles and manifest are on
	// the shared volume. It is nil when no repo had a ref to bundle.
	Bundle *BundleReport `json:"bundle,omitempty"`
}

// WorkspacePreservation distinguishes absent preparation from a clean pushed
// worktree. Generation zero means no durable task owner was ever admitted.
type WorkspacePreservation struct {
	Version         int    `json:"version"`
	Workspace       string `json:"workspace"`
	Task            string `json:"task"`
	SessionUID      string `json:"sessionUID"`
	SourcePodUID    string `json:"sourcePodUID"`
	OwnerGeneration uint64 `json:"ownerGeneration"`
	Kind            string `json:"kind"`
}

// RepoRescue is one clone under ~/repos.
type RepoRescue struct {
	Absent bool `json:"absent,omitempty"`
	// FullBundle preserves all listed owned refs, including already pushed
	// refs, without making origin a prerequisite of this bundle.
	FullBundle bool   `json:"fullBundle,omitempty"`
	Path       string `json:"path"`
	Fetched    bool   `json:"fetched"`
	FetchError string `json:"fetchError,omitempty"`
	// Worktrees are every worktree of the clone, its main checkout included.
	Worktrees []WorktreeRescue `json:"worktrees"`
	// UnpushedRefs are the local refs with commits origin lacks, rescue
	// branches included, after the rescue: what the bundle must hold (D-10
	// step 4). Stash entries are named stash@{n}, because only the newest has
	// a ref. FullBundle shared preservation instead lists every owned ref,
	// including refs already on origin, because the task worktree is absent.
	UnpushedRefs []Ref  `json:"unpushedRefs,omitempty"`
	Error        string `json:"error,omitempty"`
	// Bundle is this clone's bundle, when it has unpushed refs (D-48).
	Bundle *RepoBundle `json:"bundle,omitempty"`
}

// WorktreeRescue is one worktree.
type WorktreeRescue struct {
	Absent bool   `json:"absent,omitempty"`
	Path   string `json:"path"`
	// Branch is empty for a detached HEAD.
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	// Dirty: tracked changes or untracked files that are not ignored.
	Dirty bool `json:"dirty,omitempty"`
	// RescueBranch holds the dirty work, or anchors a detached HEAD whose
	// commit was on no ref. The worktree itself is left as it was.
	RescueBranch string `json:"rescueBranch,omitempty"`
	// Refused says why the work could not be rescued: a merge or rebase in
	// progress, an untracked nested repo, a submodule holding work its origin
	// lacks, or more than 50 MiB untracked.
	Refused string `json:"refused,omitempty"`
}

// Ref is a ref and the commit it names.
type Ref struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

// AgentStop is what `agentd ctl rescue --stop-agent` did before the rescue
// (D-48): a rescue for a suspend stops the agent first, so nothing writes to
// the worktree between the rescue and the pod's end.
type AgentStop struct {
	// WasRunning: the agent CLI was running when the rescue began.
	WasRunning bool `json:"wasRunning"`
	// Killed: it, or a process it left in its process group, was still
	// running after the grace that followed SIGTERM, and got SIGKILL.
	Killed bool `json:"killed,omitempty"`
	// Running: a process of the agent's group still runs after the stop, so
	// the rescue may miss what it writes later.
	Running bool `json:"running"`
}

// The shared volume's rescue layout (D-10, D-48). Paths in a report and a
// manifest are relative to the shared volume's root, so they mean the same in
// every pod that mounts it.
const (
	// RescueRoot is the shared volume's rescue directory. memory/, logs/ and
	// mirrors/ (D-15) are its siblings.
	RescueRoot = "rescue"
	// ManifestFile is the manifest's name inside a rescue's directory.
	ManifestFile = "manifest.json"
	// ManifestVersion is the manifest format this agentd writes.
	ManifestVersion = 1
	// StashRefPrefix names a stash entry inside a bundle: stash@{n} becomes
	// refs/agentd-rescue/stash/<n>, because only the newest entry has a ref.
	StashRefPrefix = "refs/agentd-rescue/stash/"
)

// RescueDir is a rescue's directory on the shared volume:
// rescue/<session>/<name>, where name is the report's stamp, with -2, -3 and
// so on for a later rescue in the same minute.
func RescueDir(session, name string) string { return path.Join(RescueRoot, session, name) }

// BundleReport is where one rescue put its bundles.
type BundleReport struct {
	// Dir is the rescue's directory on the shared volume, rescue/<session>/<name>.
	Dir string `json:"dir,omitempty"`
	// Manifest is Dir/manifest.json, set once the manifest was written and
	// read back.
	Manifest string `json:"manifest,omitempty"`
	// Error says why the bundles or the manifest could not be written; the
	// repos' own errors say more.
	Error string `json:"error,omitempty"`
}

// RepoBundle is one clone's bundle: every unpushed ref of the clone, thin
// against origin's default branch (D-48).
type RepoBundle struct {
	// File is the bundle's path on the shared volume, Dir/<clone>.bundle.
	File string `json:"file,omitempty"`
	// SHA256 and Size are of the file as read back from the shared volume.
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	// Base is what the bundle is thin against: refs/remotes/origin/HEAD, or
	// every origin ref when the clone has no origin/HEAD. A restore clones
	// origin first, then fetches the bundle.
	Base string `json:"base,omitempty"`
	// Refs are the refs inside the bundle, each with the local ref it holds.
	Refs []BundleRef `json:"refs,omitempty"`
	// Verified is set when `git bundle verify` passed and the bundle's heads
	// are exactly Refs.
	Verified bool   `json:"verified,omitempty"`
	Error    string `json:"error,omitempty"`
}

// BundleRef is one ref inside a bundle.
type BundleRef struct {
	// Name is the ref's name inside the bundle.
	Name string `json:"name"`
	// Source is the local ref it holds, as UnpushedRefs names it; it equals
	// Name except for a stash entry (stash@{n}).
	Source string `json:"source"`
	Commit string `json:"commit"`
}

// RescueManifest is manifest.json in a rescue's directory: what the bundles
// beside it hold, for a restore and for a human (D-48). It is written after
// the bundles, so a directory without one is a rescue that did not finish.
type RescueManifest struct {
	Version   int       `json:"version"`
	Session   string    `json:"session"`
	Stamp     string    `json:"stamp"`
	Dir       string    `json:"dir"`
	CreatedAt time.Time `json:"createdAt"`
	// Complete is set when every bundle was written and verified.
	Complete bool           `json:"complete"`
	Repos    []ManifestRepo `json:"repos"`
}

// ManifestRepo is one clone in a manifest.
type ManifestRepo struct {
	// Path is the clone on the session volume, for example
	// /home/dev/repos/haynes-ops.
	Path string `json:"path"`
	// Remote is origin's URL without any credentials in it.
	Remote string     `json:"remote,omitempty"`
	Bundle RepoBundle `json:"bundle"`
}
