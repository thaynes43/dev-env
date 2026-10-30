package apiv1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const CodexRCMaxRequest = 16 << 10
const CodexRCMaxPacket = 8 << 10
const CodexRCIntentAnnotation = "dev-env.haynesops.com/codex-rc-once"

var codexRCID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var codexRCDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// CodexRCRequest never accepts a path, native method, existing thread or pairing assertion.
type CodexRCRequest struct {
	RequestID  string    `json:"requestID"`
	SessionUID string    `json:"sessionUID"`
	PodUID     string    `json:"podUID"`
	Project    string    `json:"project"`
	Repository string    `json:"repository"`
	Model      string    `json:"model"`
	Effort     string    `json:"effort"`
	Handoff    string    `json:"handoff"`
	Deadline   time.Time `json:"deadline"`
}

func (r CodexRCRequest) Valid(now time.Time) bool {
	return codexRCID.MatchString(r.RequestID) && codexRCID.MatchString(r.SessionUID) && codexRCID.MatchString(r.PodUID) && projectcatalog.ValidName(r.Project) && projectcatalog.ValidName(r.Repository) && protocol.ValidateCodexModel(r.Model) == nil && r.Effort != "" && protocol.ValidateCodexEffort(r.Model, r.Effort) == nil && len(r.Handoff) > 0 && len(r.Handoff) <= 4096 && strings.TrimSpace(r.Handoff) != "" && utf8.ValidString(r.Handoff) && !strings.ContainsFunc(r.Handoff, func(c rune) bool { return unicode.IsControl(c) && c != '\n' && c != '\t' }) && r.Deadline.After(now) && !r.Deadline.After(now.Add(30*time.Second))
}

// CodexRCTarget is deployment-owned qualification, never an HTTP request.
// PairingSource/PairingSHA256 refer to independently verified supported-client
// evidence. Native connected status and code issuance cannot populate it.
// Root device/inode and snapshot bind previously accepted preparation.
type CodexRCTarget struct {
	SessionName       string    `json:"sessionName"`
	SessionUID        string    `json:"sessionUID"`
	PodUID            string    `json:"podUID"`
	Project           string    `json:"project"`
	Repository        string    `json:"repository"`
	SnapshotSHA256    string    `json:"snapshotSHA256"`
	Root              string    `json:"root"`
	RootDevice        uint64    `json:"rootDevice"`
	RootInode         uint64    `json:"rootInode"`
	PreparationSHA256 string    `json:"preparationSHA256"`
	PID               int       `json:"pid"`
	BootID            string    `json:"bootID"`
	StartTicks        uint64    `json:"startTicks"`
	InstallationID    string    `json:"installationID"`
	EnvironmentID     string    `json:"environmentID"`
	PairingSource     string    `json:"pairingSource"`
	PairingSHA256     string    `json:"pairingSHA256"`
	VerifiedAt        time.Time `json:"verifiedAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
}

func (t CodexRCTarget) Valid(now time.Time) bool {
	return projectcatalog.ValidName(t.SessionName) && codexRCID.MatchString(t.SessionUID) && codexRCID.MatchString(t.PodUID) && projectcatalog.ValidName(t.Project) && projectcatalog.ValidName(t.Repository) && codexRCDigest.MatchString(t.SnapshotSHA256) && codexRCDigest.MatchString(t.PreparationSHA256) && filepath.IsAbs(t.Root) && filepath.Clean(t.Root) == t.Root && t.Root != "/" && len(t.Root) <= 4096 && !strings.ContainsAny(t.Root, "\\\x00\r\n") && utf8.ValidString(t.Root) && t.RootInode > 0 && t.PID > 1 && codexRCID.MatchString(t.BootID) && t.StartTicks > 0 && codexRCID.MatchString(t.InstallationID) && codexRCID.MatchString(t.EnvironmentID) && t.PairingSource == "supported-client" && codexRCDigest.MatchString(t.PairingSHA256) && !t.VerifiedAt.IsZero() && !t.VerifiedAt.After(now) && t.ExpiresAt.After(now) && t.ExpiresAt.Sub(t.VerifiedAt) <= 15*time.Minute
}
func (t CodexRCTarget) Matches(r CodexRCRequest) bool {
	return t.SessionUID == r.SessionUID && t.PodUID == r.PodUID && t.Project == r.Project && t.Repository == r.Repository
}

type CodexRCDispatch struct {
	Caller       string         `json:"caller"`
	Request      CodexRCRequest `json:"request"`
	Target       CodexRCTarget  `json:"target"`
	ClientSHA256 string         `json:"clientSHA256"`
}
type CodexRCReceipt struct {
	Caller         string        `json:"caller"`
	RequestID      string        `json:"requestID"`
	ClientSHA256   string        `json:"clientSHA256"`
	DispatchSHA256 string        `json:"dispatchSHA256"`
	Target         CodexRCTarget `json:"target"`
	Model          string        `json:"model"`
	Effort         string        `json:"effort"`
	ThreadID       string        `json:"threadID,omitempty"`
	TurnID         string        `json:"turnID,omitempty"`
	State          string        `json:"state"` // confirmed means both native ACKs, not inference/phone visibility
	Reason         string        `json:"reason"`
	StartedAt      time.Time     `json:"startedAt"`
	FinishedAt     time.Time     `json:"finishedAt"`
}

func CodexRCHash(v any) string { b, _ := json.Marshal(v); return projectcatalog.Digest(b) }
func CodexRCClientHash(principal string, r CodexRCRequest) string {
	return CodexRCHash(struct {
		Principal string
		Request   CodexRCRequest
	}{principal, r})
}

// Reuse the existing duplicate/nesting fence; decode only bounded typed data.
func DecodeCodexRC(raw []byte, out any, limit int) error {
	if len(raw) > limit || !utf8.Valid(raw) || !protocol.StrictPortalJSON(raw) {
		return errors.New("invalid codex RC document")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid codex RC document")
	}
	return nil
}

// Native acknowledgements are bounded identifiers, never a claim of completed
// inference, served model, enrollment or phone visibility.
func (r CodexRCReceipt) Acknowledged() bool {
	return r.State == "confirmed" && r.Reason == "thread_and_turn_acknowledged" && codexRCID.MatchString(r.ThreadID) && codexRCID.MatchString(r.TurnID) && !r.StartedAt.IsZero() && !r.FinishedAt.Before(r.StartedAt)
}

func (d CodexRCDispatch) Valid(now time.Time) bool {
	return d.Request.Valid(now) && d.Target.Valid(now) && d.Target.Matches(d.Request) && len(d.Caller) > 0 && len(d.Caller) <= 256 && utf8.ValidString(d.Caller) && !strings.ContainsFunc(d.Caller, unicode.IsControl) && codexRCDigest.MatchString(d.ClientSHA256)
}
