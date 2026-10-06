package protocol

import "time"

// RescueReport is what `agentd ctl rescue` prints (D-43): step 1 of D-10's
// rescue, and the list of refs that step 5's bundle must cover.
type RescueReport struct {
	Session    string       `json:"session"`
	Stamp      string       `json:"stamp"`
	StartedAt  time.Time    `json:"startedAt"`
	FinishedAt time.Time    `json:"finishedAt"`
	Repos      []RepoRescue `json:"repos"`
	// OK is set when every worktree is clean or rescued and no repo failed.
	// A rescue that is not OK still leaves the volume as it was; the operator
	// marks the session rescueFailed, which blocks archive (D-10).
	OK bool `json:"ok"`
	// CleanAndPushed is D-10's proof that nothing needs a bundle: every repo
	// fetched, every worktree clean, and no local ref holds a commit origin
	// lacks.
	CleanAndPushed bool `json:"cleanAndPushed"`
}

// RepoRescue is one clone under ~/repos.
type RepoRescue struct {
	Path       string `json:"path"`
	Fetched    bool   `json:"fetched"`
	FetchError string `json:"fetchError,omitempty"`
	// Worktrees are every worktree of the clone, its main checkout included.
	Worktrees []WorktreeRescue `json:"worktrees"`
	// UnpushedRefs are the local refs with commits origin lacks, rescue
	// branches included, after the rescue: what the bundle must hold (D-10
	// step 4). Stash entries are named stash@{n}, because only the newest has
	// a ref. Empty when origin has everything.
	UnpushedRefs []Ref  `json:"unpushedRefs,omitempty"`
	Error        string `json:"error,omitempty"`
}

// WorktreeRescue is one worktree.
type WorktreeRescue struct {
	Path string `json:"path"`
	// Branch is empty for a detached HEAD.
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	// Dirty: tracked changes or untracked files that are not ignored.
	Dirty bool `json:"dirty,omitempty"`
	// RescueBranch holds the dirty work, or anchors a detached HEAD whose
	// commit was on no ref. The worktree itself is left as it was.
	RescueBranch string `json:"rescueBranch,omitempty"`
	// Refused says why the work could not be rescued: a merge or rebase in
	// progress, an untracked nested repo, an initialized submodule, or more
	// than 50 MiB untracked.
	Refused string `json:"refused,omitempty"`
}

// Ref is a ref and the commit it names.
type Ref struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}
