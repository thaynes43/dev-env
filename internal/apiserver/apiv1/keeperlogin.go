package apiv1

import (
	"net/url"
	"time"
)

// The keeper login routes (NB-01, Story NEXT-BATCH-1020). A workbench session,
// and only the workbench, renews the keeper's shared Claude or Codex login
// through the operator, which execs into the keeper pod's login container (the
// helper that already owns the ceremony) and nowhere else:
//
//	GET    /v1/keeper-login/{provider}        state and expiry, nothing else
//	POST   /v1/keeper-login/{provider}/url    open a ceremony; the answer carries the sign-in link
//	POST   /v1/keeper-login/{provider}/code   claude: the one-time code; codex: wait for the device flow to finish
//	DELETE /v1/keeper-login/{provider}        abandon the open ceremony
//
// The link, the Codex device code and the pasted Claude code are secrets. Each
// travels only in the response or request that carries it. The operator keeps
// none of them and never logs one.

// KeeperLoginPath is the route's prefix.
const KeeperLoginPath = "/v1/keeper-login"

// Providers a keeper login is renewed for.
const (
	KeeperLoginClaude = "claude"
	KeeperLoginCodex  = "codex"
)

// KeeperLoginPathFor is the status (and cancel) route of one provider.
func KeeperLoginPathFor(provider string) string {
	return KeeperLoginPath + "/" + url.PathEscape(provider)
}

// KeeperLoginStatus is what GET answers: the keeper's fixed state word and its
// times, and nothing a login ceremony or a credential could carry.
type KeeperLoginStatus struct {
	Provider string `json:"provider"`
	// State is Ready, PendingPublication (the journal is fresh and the access
	// projection is about to follow), NeedsLogin, or Unavailable (the keeper's
	// helper could not be asked).
	State      string `json:"state"`
	Generation uint64 `json:"generation,omitempty"`
	// AccessExpiresAt is when the current access token lapses; the keeper renews
	// it by itself. LoginExpiresAt is when the whole login lapses and a
	// ceremony is due (Claude only, and only when the provider said so). Both
	// are absent from a keeper that predates the fields.
	AccessExpiresAt *time.Time `json:"accessExpiresAt,omitempty"`
	LoginExpiresAt  *time.Time `json:"loginExpiresAt,omitempty"`
}

// KeeperLoginURL is what POST .../url answers. The values are secrets for the
// life of the ceremony: show them to Tom, never store or log them.
type KeeperLoginURL struct {
	Provider string `json:"provider"`
	// URL is the sign-in page. Open it, sign in, and (Claude) copy the code the
	// page shows.
	URL string `json:"url"`
	// UserCode is the Codex device flow's one-time code, shown on the page it
	// links to. Claude's flow has none.
	UserCode string `json:"userCode,omitempty"`
	// ExpiresAt is when the keeper's ceremony window closes.
	ExpiresAt time.Time `json:"expiresAt"`
}

// KeeperLoginCode is the request body of POST .../code for Claude: the one-time
// code, as the sign-in page shows it. Codex's route takes no body.
type KeeperLoginCode struct {
	Code string `json:"code"`
}

// KeeperLoginResult is what POST .../code answers. Result is one of the keeper's
// fixed words: Adopted, AdoptedPendingPublication, NeedsLogin, AccountMismatch,
// Busy or Unavailable, or Waiting (a Codex flow still waiting for the sign-in).
type KeeperLoginResult struct {
	Provider   string `json:"provider"`
	Result     string `json:"result"`
	Generation uint64 `json:"generation,omitempty"`
}
