package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// SSHCommandRunner executes only fixed pvesh operations. PossibleDispatch is
// true whenever a create may have reached the remote host, even on an error.
type SSHCommandRunner interface {
	Run(context.Context, string) (output []byte, possibleDispatch bool, err error)
	Ready() error
}

type tokenInfo struct {
	Expire  int64  `json:"expire"`
	Privsep int    `json:"privsep"`
	Comment string `json:"comment"`
}

// PVE is inconsistent: `pvesh create` returns info.expire and info.privsep as
// JSON strings ("0"), while `pvesh get .../token` returns integers, and the
// boolean schema may also serialize as true/false. Accept those forms and no
// others. The values are still compared exactly afterwards.
func flexInt(raw json.RawMessage, allowBool bool) (int64, bool) {
	text := strings.TrimSpace(string(raw))
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		var quoted string
		if json.Unmarshal(raw, &quoted) != nil {
			return 0, false
		}
		text = quoted
		if text == "" || text != strings.TrimSpace(text) || strings.HasPrefix(text, "+") {
			return 0, false
		}
	} else if allowBool {
		switch text {
		case "true":
			return 1, true
		case "false":
			return 0, true
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	return n, err == nil
}

// Require the fields explicitly; a missing privsep must not silently mean false.
func (t *tokenInfo) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Expire  json.RawMessage `json:"expire"`
		Privsep json.RawMessage `json:"privsep"`
		Comment string          `json:"comment"`
	}
	if json.Unmarshal(raw, &wire) != nil {
		return errors.New("invalid provider metadata")
	}
	privsep, ok := flexInt(wire.Privsep, true)
	if !ok || (privsep != 0 && privsep != 1) {
		return errors.New("missing or invalid provider isolation metadata")
	}
	expire := int64(0)
	if len(wire.Expire) != 0 {
		if expire, ok = flexInt(wire.Expire, false); !ok {
			return errors.New("invalid provider expiry metadata")
		}
	}
	t.Expire = expire
	t.Privsep = int(privsep)
	t.Comment = wire.Comment
	return nil
}

type mintedWire struct {
	Info  tokenInfo `json:"info"`
	Value string    `json:"value"`
	ID    string    `json:"full-tokenid"`
}

type proxmoxProvider struct{ SSH SSHCommandRunner }

type mintResult struct {
	TokenID          string
	TokenSecret      secretValue
	PossibleDispatch bool
}

func (r mintResult) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }
func (mintResult) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

func pveCommand(verb string, e credentialEntry) string {
	path := "/access/users/dev-env@pve/token/" + providerName(e.Spec.Grant.UID)
	command := "sudo -n /usr/bin/pvesh " + verb + " " + path + " --output-format json"
	if verb == "create" {
		command += fmt.Sprintf(" --expire %d --privsep 0 --comment %s", e.Spec.ExpiresAt.Unix(), providerComment(e))
	}
	return command
}

func (p *proxmoxProvider) mint(ctx context.Context, e credentialEntry) (mintResult, error) {
	if !validEntry(e) || ctx.Err() != nil {
		return mintResult{}, errors.New("invalid or cancelled mint request")
	}
	out, dispatched, err := p.SSH.Run(ctx, pveCommand("create", e))
	result := mintResult{PossibleDispatch: dispatched}
	if err != nil {
		return result, errors.New("proxmox mint command failed")
	}
	result.PossibleDispatch = true
	var response mintedWire
	// Each refusal names only which check failed, never a response value.
	switch {
	case decodeBounded(out, &response) != nil:
		return result, errors.New("proxmox mint response was invalid: undecodable")
	case response.ID != providerID(e.Spec.Grant.UID):
		return result, errors.New("proxmox mint response was invalid: token id mismatch")
	case response.Info.Expire != e.Spec.ExpiresAt.Unix():
		return result, errors.New("proxmox mint response was invalid: expiry mismatch")
	case response.Info.Privsep != 0:
		return result, errors.New("proxmox mint response was invalid: privilege separation not disabled")
	case response.Info.Comment != providerComment(e):
		return result, errors.New("proxmox mint response was invalid: comment mismatch")
	case !validTokenValue(response.Value):
		return result, errors.New("proxmox mint response was invalid: secret shape")
	}
	result.TokenID = response.ID
	result.TokenSecret = newSecretValue(response.Value)
	return result, nil
}

// remove verifies provider ownership before deletion and verifies absence by
// listing metadata afterwards. NotFound must be proved by successful JSON list,
// never by interpreting raw stderr text or a generic failed GET as absence.
func (p *proxmoxProvider) remove(ctx context.Context, e credentialEntry) (bool, error) {
	present, err := p.inspect(ctx, e)
	if err != nil || !present {
		return !present, err
	}
	if _, _, err = p.SSH.Run(ctx, pveCommand("delete", e)); err != nil {
		return false, errors.New("proxmox token cleanup failed")
	}
	present, err = p.inspect(ctx, e)
	return !present, err
}
func (p *proxmoxProvider) inspect(ctx context.Context, e credentialEntry) (bool, error) {
	out, _, err := p.SSH.Run(ctx, "sudo -n /usr/bin/pvesh get /access/users/dev-env@pve/token --output-format json")
	if err != nil {
		return false, errors.New("could not inspect Proxmox token metadata")
	}
	var tokens []struct {
		TokenID string          `json:"tokenid"`
		Expire  json.RawMessage `json:"expire"`
		Privsep json.RawMessage `json:"privsep"`
		Comment string          `json:"comment"`
	}
	if decodeBounded(out, &tokens) != nil {
		return false, errors.New("proxmox token metadata was invalid")
	}
	for _, t := range tokens {
		if t.TokenID != providerName(e.Spec.Grant.UID) {
			continue
		}
		expire, okExpire := flexInt(t.Expire, false)
		privsep, okPrivsep := flexInt(t.Privsep, true)
		if !okExpire || expire != e.Spec.ExpiresAt.Unix() || !okPrivsep || privsep != 0 || t.Comment != providerComment(e) {
			return true, errors.New("proxmox token ownership does not match")
		}
		return true, nil
	}
	return false, nil
}

func decodeBounded(raw []byte, dst any) error {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return errors.New("invalid provider response size")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	// PVE may add harmless metadata fields; material and required ownership
	// fields are checked separately. Reject additional JSON documents.
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid provider JSON")
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("additional provider JSON")
	}
	return nil
}
func validTokenValue(value string) bool {
	if len(value) < 16 || len(value) > 4096 {
		return false
	}
	for _, r := range value {
		if r < 33 || r > 126 {
			return false
		}
	}
	return !strings.ContainsAny(value, "\"\\")
}

// Retry intervals are timer waits; there are no load or tight retry loops.
const credentialPoll = 15 * time.Second
const credentialAttemptTimeout = 30 * time.Second
