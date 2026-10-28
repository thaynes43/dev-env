package protocol

// NoCloneProof is agentd's evidence, read from the session volume, that the session's
// declared repository was never cloned because its bootstrap was interrupted: nothing
// under ~/repos (not even clone staging), nothing under ~/work, and a bootstrap record
// that does not say the repository is Ready. It is counts and closed-set codes only: no
// path, history or detail text. The operator uses it to give a rescue it cannot prove
// clean a truthful reason (BootstrapInterrupted) instead of NotProven. It proves nothing
// about Git work outside ~/repos, which a rescue never looks at, and it never makes a
// volume safe to archive or delete.
type NoCloneProof struct {
	Version int `json:"version"`
	// Repo is the declared repository the proof is about (the session's spec.repo).
	Repo string `json:"repo"`
	// ReposEntries and WorkEntries count every entry, hidden ones included, directly
	// in ~/repos and ~/work. A directory that does not exist counts 0.
	ReposEntries int `json:"reposEntries"`
	WorkEntries  int `json:"workEntries"`
	// BootstrapState and BootstrapReason are the bootstrap record's own (~/.agentd,
	// project-bootstrap.json). RepositoryState, Step and Reason are its entry for Repo;
	// RepositoryState is empty when the record does not list it.
	BootstrapState   string `json:"bootstrapState"`
	BootstrapReason  string `json:"bootstrapReason,omitempty"`
	RepositoryState  string `json:"repositoryState,omitempty"`
	RepositoryStep   string `json:"repositoryStep,omitempty"`
	RepositoryReason string `json:"repositoryReason,omitempty"`
}
