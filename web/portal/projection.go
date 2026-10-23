package portal

import "github.com/thaynes43/dev-env/internal/apiserver/apiv1"

// SourceBinding must come from a trusted resource lookup, not agent prose or a
// guessed session name. Existing apiv1.Session has no project/environment claim;
// the explicit binding and current authorizer supply that missing authority.
type SourceBinding struct {
	Resource                          Resource
	ClaimUID, SessionName, SessionUID string
}

// WorkerSummary deliberately omits prompts, provenance URLs, usage, remote
// pairing, private history, reported outcome and arbitrary agent status text.
// WorkerPhase is source phase metadata, not proof that a native process is live.
type WorkerSummary struct {
	Owner, WorkerPhase string
}

func ProjectWorker(binding SourceBinding, claim apiv1.TaskClaim, session apiv1.Session) (WorkerSummary, error) {
	r := binding.Resource
	if claim.Archived || !identifier.MatchString(r.Project) || !identifier.MatchString(r.Environment) || !identifier.MatchString(r.Task) || binding.ClaimUID == "" || binding.SessionUID == "" || binding.SessionName == "" ||
		claim.UID != binding.ClaimUID || claim.Identity.Project != r.Project || claim.Identity.TaskKey != r.Task || claim.Owner.Name != binding.SessionName || claim.Owner.UID != binding.SessionUID || session.Name != binding.SessionName || session.UID != binding.SessionUID {
		return WorkerSummary{}, ErrUnavailable
	}
	if !validText(claim.Owner.Name) || !validText(session.Phase) {
		return WorkerSummary{}, ErrUnavailable
	}
	return WorkerSummary{Owner: display(claim.Owner.Name), WorkerPhase: display(session.Phase)}, nil
}
