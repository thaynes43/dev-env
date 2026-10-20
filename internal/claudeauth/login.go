package claudeauth

import (
	"encoding/json"
	"time"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

// LoginKey is the second key of the projection Secret: the login's lapse date.
//
// It is a separate document, not a member of access.json, on purpose. agentd
// decodes access.json strictly and refuses any member it does not know, so a new
// member there would stop every session that still runs an older agentd from
// following the keeper's login, and those sessions must not be disturbed. An
// older agentd reads only access.json and ignores this key.
//
// The document is not secret: a version and one time. The member name does not
// contain "refresh", and DecodeLogin refuses any member that does.
const LoginKey = "login.json"

// loginWire is the document at LoginKey.
type loginWire struct {
	Version       int       `json:"version"`
	LoginLapsesAt time.Time `json:"loginLapsesAt"`
}

// EncodeLogin is the canonical form of the lapse document. A zero or
// non-positive time is refused: an unknown lapse is no document at all, never a
// guessed or zero date.
func EncodeLogin(lapsesAt time.Time) ([]byte, error) {
	if lapsesAt.IsZero() || lapsesAt.Unix() <= 0 {
		return nil, ErrInvalid
	}
	return json.Marshal(loginWire{1, lapsesAt.UTC()})
}

// DecodeLogin parses the lapse document. Unknown and duplicate members, a
// version other than 1 and a zero time are ErrInvalid. Errors carry no input.
func DecodeLogin(raw []byte) (time.Time, error) {
	if len(raw) == 0 || len(raw) > 1<<10 {
		return time.Time{}, ErrInvalid
	}
	var w loginWire
	if codexauth.DecodeStrict(raw, &w) != nil || w.Version != 1 || w.LoginLapsesAt.IsZero() || w.LoginLapsesAt.Unix() <= 0 {
		return time.Time{}, ErrInvalid
	}
	return w.LoginLapsesAt.UTC(), nil
}
