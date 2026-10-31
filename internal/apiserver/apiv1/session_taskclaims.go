package apiv1

// SessionTaskClaimsHeader opts a detail read into claim metadata. Older strict
// Session readers receive the original shape and perform no claim lookup.
const SessionTaskClaimsHeader = "X-Dev-Env-Session-Task-Claims"
const SessionTaskClaimsV1 = "v1"

// SessionTaskClaims reports a bounded observation, not ownership transfer or
// workflow success. Unknown never asserts absence of work.
type SessionTaskClaims struct {
	State  string      `json:"state"`
	Reason string      `json:"reason"`
	Items  []TaskClaim `json:"items"`
}
