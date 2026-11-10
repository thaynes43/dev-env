package agentd

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/claudeauth"
	"github.com/thaynes43/dev-env/internal/codexauth"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// Passive allowance readings (DESIGN-059): the numbers a running session's own
// CLI already received with a model response. agentd makes no provider
// request, starts no provider client and touches no login to get them.

// The reasons a reading was not sent. A heartbeat names one in its
// allowance-reading step, so the operator's status shows why a session that
// has a reading did not report it. Each is fixed text: nothing from the
// reading itself is ever put in a note.
const (
	// allowanceShapeUnrecognized: the source has a record, and the fields the
	// reader uses do not have the documented shape.
	allowanceShapeUnrecognized = "shape_unrecognized"
	// allowanceOutOfBounds: the record has the shape, and a value is outside
	// the bounds of DESIGN-059 4.4, such as a reset before the reading.
	allowanceOutOfBounds = "out_of_bounds"
)

// allowanceFileUnsafe: the helper's output, or its directory, is not a
// regular file and directory owned by this user with no group or other
// access, or it is over the read bound.
const allowanceFileUnsafe = "file_unsafe"

// The fields the image's helper writes for the Claude statusline
// (providerallowance.ClaudeStatuslineRead): one passive item with the two
// documented windows, measured by id.
const (
	claudeStatuslineProducer   = "agent-run-claude-statusline"
	claudeStatuslineCapability = "claude-statusline:documented-windows"
)

// readClaudeStatuslineAllowance reads the helper's last document from
// claudeUsageFile with one bounded read, after checking that the directory
// and the file belong to this user and nobody else can read or write them.
// A missing file or a document with no usable window is no reading (nil, "").
// The reading time is the callback time the helper recorded; windows whose
// reset had already passed at that time describe an old window and are left
// out. The caller keeps an earlier reading time when no value changed.
func readClaudeStatuslineAllowance(s Settings) (*protocol.AllowanceReading, string) {
	if s.RuntimeDir == "" || !filepath.IsAbs(s.RuntimeDir) {
		return nil, ""
	}
	dir, err := os.Lstat(claudeUsageDir(s))
	if os.IsNotExist(err) {
		return nil, ""
	}
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 || !ownedByMe(dir) || dir.Mode().Perm()&0o077 != 0 {
		return nil, allowanceFileUnsafe
	}
	f, err := os.OpenFile(claudeUsageFile(s), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, ""
	}
	if err != nil {
		return nil, allowanceFileUnsafe
	}
	defer func() { _ = f.Close() }()
	limit := int64(providerallowance.FixtureParseLimits.MaxBytes)
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !ownedByMe(fi) || fi.Mode().Perm()&0o077 != 0 || fi.Size() > limit {
		return nil, allowanceFileUnsafe
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, allowanceFileUnsafe
	}
	if len(raw) == 0 {
		return nil, ""
	}
	return claudeStatuslineReading(raw)
}

// claudeStatuslineReading turns the helper's providerallowance.read.v1
// document into a reading. It accepts only the helper's own item shape.
func claudeStatuslineReading(raw []byte) (*protocol.AllowanceReading, string) {
	doc, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil || doc.Schema != providerallowance.SchemaRead {
		return nil, allowanceShapeUnrecognized
	}
	var item *providerallowance.ReadItem
	for i := range doc.Items {
		if doc.Items[i].Observation.Provider != "anthropic" {
			continue
		}
		if item != nil {
			return nil, allowanceShapeUnrecognized
		}
		item = &doc.Items[i]
	}
	if item == nil {
		return nil, ""
	}
	obs := item.Observation
	if obs.ProducerRef != claudeStatuslineProducer || obs.Source.Kind != providerallowance.SourcePassive || obs.Source.CapabilityRef != claudeStatuslineCapability ||
		obs.ProviderObservedAt != nil || !providerallowance.ValidateObservation(obs, providerallowance.TrustedContext{}, nil).ShapeOK {
		return nil, allowanceShapeUnrecognized
	}
	at, err := time.Parse(time.RFC3339Nano, obs.CapturedAt)
	if err != nil || at.IsZero() {
		return nil, allowanceShapeUnrecognized
	}
	r := protocol.AllowanceReading{Provider: protocol.AgentClaude, Source: protocol.AllowanceSourceClaudeStatusline, ReadAt: at.UTC()}
	for _, m := range obs.Measures {
		if m.ID != "five_hour" && m.ID != "seven_day" {
			return nil, allowanceShapeUnrecognized
		}
		if m.Unit != providerallowance.UnitPercent || m.Direction != providerallowance.DirectionUsed || m.Window.DurationMinutes == nil {
			return nil, allowanceShapeUnrecognized
		}
		if m.Value.State != providerallowance.ValueKnown || m.Value.Decimal == nil || m.Window.ResetPrecision != providerallowance.ResetInstant || m.Window.ResetAt == nil {
			continue
		}
		used, ok := protocol.CanonicalAllowancePercent(*m.Value.Decimal)
		if !ok {
			return nil, allowanceShapeUnrecognized
		}
		reset, err := time.Parse(time.RFC3339, *m.Window.ResetAt)
		if err != nil {
			return nil, allowanceShapeUnrecognized
		}
		if !reset.After(r.ReadAt) {
			continue
		}
		r.Windows = append(r.Windows, protocol.AllowanceWindow{Minutes: int64(*m.Window.DurationMinutes), UsedPercent: used, ResetsAt: reset.UTC()})
	}
	if len(r.Windows) == 0 {
		return nil, ""
	}
	if !r.Valid() {
		return nil, allowanceOutOfBounds
	}
	return &r, ""
}

// statuslineReadings keeps the reading time of the last Claude reading. The
// callback also runs on events that bring no new numbers (a permission-mode
// change, a timer), so a later callback with the same values keeps the
// earlier time: a reading can look older than it is, never newer
// (DESIGN-059 4.4).
type statuslineReadings struct {
	last *protocol.AllowanceReading
}

// keep returns r with its time moved back to the last reading's when no
// value changed, and remembers it. A nil r leaves the memory as it is.
func (k *statuslineReadings) keep(r *protocol.AllowanceReading) *protocol.AllowanceReading {
	if r == nil {
		return nil
	}
	out := *r
	out.Windows = append([]protocol.AllowanceWindow(nil), r.Windows...)
	if k.last != nil && k.last.SameValues(out) && k.last.ReadAt.Before(out.ReadAt) {
		out.ReadAt = k.last.ReadAt
	}
	saved := out
	saved.Windows = append([]protocol.AllowanceWindow(nil), out.Windows...)
	k.last = &saved
	return &out
}

// The send rule's refusals (DESIGN-059 step 4). Every managed pod holds only
// the keeper's projection of the one login per provider, so a reading is about
// that login by construction, but only while the pod's credential is that
// projection.
const (
	// allowanceNotProjection: this pod is not configured to follow the
	// keeper's projection for the session's provider.
	allowanceNotProjection = "credential_not_projection"
	// allowanceNotCurrent: the projection is configured, and the pod's
	// installed credential is not its current generation, or it is near its
	// expiry or unreadable.
	allowanceNotCurrent = "credential_not_current"
)

// allowanceStepName is the step a refusal is reported in.
const allowanceStepName = "allowance-reading"

func allowanceRefused(reason string) *Step {
	return &Step{Name: allowanceStepName, State: StepWarn, Notes: []string{"reading not sent: " + reason}}
}

// allowanceCredentialRefusal is the send rule: empty while the pod's
// credential for the session's provider is the keeper's current projection,
// else the reason. It only reads; it never applies a projection.
func allowanceCredentialRefusal(s Settings, sess protocol.Session, now time.Time) string {
	switch sess.Agent {
	case protocol.AgentClaude:
		if s.ClaudeLiveFile == "" {
			return allowanceNotProjection
		}
		if !claudeProjectionCurrent(s, now) {
			return allowanceNotCurrent
		}
	case protocol.AgentCodex:
		if s.CodexAccessFile == "" {
			return allowanceNotProjection
		}
		if !codexProjectionCurrent(s, now) {
			return allowanceNotCurrent
		}
	default:
		return allowanceNotProjection
	}
	return ""
}

// claudeProjectionCurrent repeats SyncClaudeAccess's checks without writing:
// the pod's credentials hold no refresh token, the projection is valid, its
// generation is the one the pod recorded, and merging it would change
// nothing, so the pod's access fields are the projection's.
func claudeProjectionCurrent(s Settings, now time.Time) bool {
	if !filepath.IsAbs(s.ClaudeLiveFile) || !filepath.IsAbs(s.ClaudeConfigDir) || !filepath.IsAbs(s.StateDir) {
		return false
	}
	existing, err := readClaudeCredentials(s)
	if err != nil || len(existing) == 0 || claudeauth.HoldsRefreshToken(existing) {
		return false
	}
	raw, err := readBounded(s.ClaudeLiveFile, claudeauth.MaxBytes)
	if err != nil {
		return false
	}
	a, err := claudeauth.Decode(raw, now)
	if err != nil {
		return false
	}
	canonical, err := claudeauth.Encode(a, now)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(canonical)
	data, err := readBounded(s.statePath("claude-access-generation.json"), 4<<10)
	var recorded claudeReceipt
	if err != nil || codexauth.DecodeStrict(data, &recorded) != nil || recorded.Generation != a.Generation ||
		subtle.ConstantTimeCompare([]byte(recorded.Fingerprint), []byte(hex.EncodeToString(sum[:]))) != 1 {
		return false
	}
	_, changed, err := claudeauth.MergeCredentials(existing, a)
	return err == nil && !changed
}

// codexProjectionCurrent is the owned-capture check
// (ownedAllowanceProjection: a valid projection more than five minutes from
// expiry whose generation the pod recorded) plus one more: the auth.json the
// CLI reads is exactly that projection's native file.
func codexProjectionCurrent(s Settings, now time.Time) bool {
	if !filepath.IsAbs(s.CodexHome) {
		return false
	}
	native, _, err := ownedAllowanceProjection(s, now)
	if err != nil {
		return false
	}
	installed, err := readCodexAccessFile(filepath.Join(s.CodexHome, "auth.json"))
	return err == nil && subtle.ConstantTimeCompare(installed, native) == 1
}

// allowanceReading is this heartbeat's reading and, when one was found and
// not sent, the step that says why. A Codex session reads its rollout; a
// Claude remote or local session reads the statusline helper's file. Any
// other session has none. No source, or a source with no reading, is neither.
func (d *Daemon) allowanceReading() (*protocol.AllowanceReading, *Step) {
	var r *protocol.AllowanceReading
	var reason string
	switch {
	case d.Session.Agent == protocol.AgentCodex:
		if path := codexAllowanceRollout(d.S); path != "" {
			r, reason = codexRolloutAllowance(path)
		}
	case d.Session.Agent == protocol.AgentClaude && (d.Session.Mode == protocol.ModeRemote || d.Session.Mode == protocol.ModeLocal):
		r, reason = readClaudeStatuslineAllowance(d.S)
		r = d.statusline.keep(r)
	default:
		return nil, nil
	}
	if r == nil {
		if reason == "" {
			return nil, nil
		}
		return nil, allowanceRefused(reason)
	}
	if reason := allowanceCredentialRefusal(d.S, d.Session, d.now()); reason != "" {
		return nil, allowanceRefused(reason)
	}
	return r, nil
}
