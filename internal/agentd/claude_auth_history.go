package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

const (
	claudeAuthHistoryFile = "claude-auth-history.json"
	claudeAuthHistoryLock = "claude-auth-history.lock"
	claudeAuthHistorySize = 16 << 10
	claudeAuthGenerations = 3
	claudeAuthCountLimit  = 64
	claudeAuthTiming      = "assistant-transcript-proxy"
	claudeAuthRejected    = "authentication"
)

type claudeAuthSource string

const (
	claudeSourceProjection claudeAuthSource = "projection"
	claudeSourceStatic     claudeAuthSource = "static-environment"
	claudeSourcePod        claudeAuthSource = "pod-owned"
	claudeSourceUnknown    claudeAuthSource = "unknown"
)

// A launch is identified locally by the boot nonce and launch creation time.
// Neither comes from the CLI or identifies a conversation.
type claudeAuthLaunch struct {
	BootID    string           `json:"bootId"`
	CreatedAt time.Time        `json:"createdAt"`
	Source    claudeAuthSource `json:"source"`
}

var claudeAuthBootID = regexp.MustCompile(`^[0-9a-f]{12}$`)

func (l claudeAuthLaunch) valid() bool {
	switch l.Source {
	case claudeSourceProjection, claudeSourceStatic, claudeSourcePod, claudeSourceUnknown:
		return claudeAuthBootID.MatchString(l.BootID) && !l.CreatedAt.IsZero()
	default:
		return false
	}
}

type claudeAuthHistory struct {
	Version           int                    `json:"version"`
	Launch            *claudeAuthLaunch      `json:"launch,omitempty"`
	ObservationTiming string                 `json:"observationTiming"`
	ClockSkewUnknown  bool                   `json:"clockSkewUnknown"`
	Generations       []claudeAuthGeneration `json:"generations,omitempty"`
}

// The applied generation describes the file at the proxy observation time.
// The CLI supplies no evidence of the generation actually used by a request.
// ConfirmedAt can be set on a no-op sync (e.g. after upgrading); only a
// successful credential-file write supplies CredentialWrittenAt.
type claudeAuthGeneration struct {
	AppliedGeneration                  uint64            `json:"appliedGeneration"`
	ConfirmedAt                        time.Time         `json:"confirmedAt"`
	CredentialWrittenAt                time.Time         `json:"credentialWrittenAt,omitzero"`
	FirstRejectedAt                    time.Time         `json:"firstRejectedAt,omitzero"`
	LastRejectedAt                     time.Time         `json:"lastRejectedAt,omitzero"`
	LastAcceptedBeforeFirstRejectionAt time.Time         `json:"lastAcceptedBeforeFirstRejectionAt,omitzero"`
	LastAcceptedAt                     time.Time         `json:"lastAcceptedAt,omitzero"`
	FirstRejectionLaunch               *claudeAuthLaunch `json:"firstRejectionLaunch,omitempty"`
	MainChain                          bool              `json:"mainChain,omitempty"`
	Sidechain                          bool              `json:"sidechain,omitempty"`
	ErrorClass                         string            `json:"errorClass,omitempty"`
	RejectionCount                     uint8             `json:"rejectionCount,omitempty"`
	// Equal timestamps on different chains are distinct. Keep exact keys up
	// to saturation so delayed entries and replays survive a daemon restart.
	SeenRejections []claudeAuthRejectionKey `json:"seenRejections,omitempty"`
}

type claudeAuthRejectionKey struct {
	At        time.Time `json:"at"`
	Sidechain bool      `json:"sidechain,omitempty"`
}

func (h claudeAuthHistory) valid() bool {
	if h.Version != 1 || h.ObservationTiming != claudeAuthTiming || !h.ClockSkewUnknown ||
		len(h.Generations) > claudeAuthGenerations || (h.Launch != nil && !h.Launch.valid()) {
		return false
	}
	var previous uint64
	for _, g := range h.Generations {
		if g.AppliedGeneration <= previous || g.ConfirmedAt.IsZero() || g.RejectionCount > claudeAuthCountLimit ||
			(g.ErrorClass != "" && g.ErrorClass != claudeAuthRejected) ||
			(g.FirstRejectionLaunch != nil && !g.FirstRejectionLaunch.valid()) {
			return false
		}
		wantKeys := int(g.RejectionCount)
		if g.RejectionCount == claudeAuthCountLimit {
			wantKeys = 0
		}
		if len(g.SeenRejections) != wantKeys {
			return false
		}
		previous = g.AppliedGeneration
	}
	return true
}

func readClaudeAuthHistory(s Settings) (claudeAuthHistory, []byte, error) {
	var h claudeAuthHistory
	raw, err := readBounded(s.statePath(claudeAuthHistoryFile), claudeAuthHistorySize)
	if err != nil {
		return h, nil, err
	}
	if codexauth.DecodeStrict(raw, &h) != nil || !h.valid() {
		return claudeAuthHistory{}, nil, errors.New("invalid Claude auth history")
	}
	return h, raw, nil
}

// Updates serialize independently of the live rejection marker. This file is
// diagnostic only. Its availability has no bearing on auth or recovery.
func updateClaudeAuthHistory(s Settings, update func(*claudeAuthHistory)) {
	lock, err := os.OpenFile(s.statePath(claudeAuthHistoryLock), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = lock.Close() }()
	fi, err := lock.Stat()
	if err != nil || !fi.Mode().IsRegular() || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return
	}
	h, before, err := readClaudeAuthHistory(s)
	if errors.Is(err, os.ErrNotExist) {
		h = claudeAuthHistory{Version: 1, ObservationTiming: claudeAuthTiming, ClockSkewUnknown: true}
	} else if err != nil {
		return
	}
	update(&h)
	raw, err := json.Marshal(h)
	if err == nil && len(raw) <= claudeAuthHistorySize && !bytes.Equal(before, raw) {
		_ = writeClaudePrivateAtomic(s.statePath(claudeAuthHistoryFile), raw)
	}
}

func recordClaudeApplied(s Settings, generation uint64, confirmedAt, writtenAt time.Time) {
	updateClaudeAuthHistory(s, func(h *claudeAuthHistory) {
		if n := len(h.Generations); n > 0 {
			last := &h.Generations[n-1]
			if generation < last.AppliedGeneration {
				return
			}
			if generation == last.AppliedGeneration {
				if last.CredentialWrittenAt.IsZero() {
					last.CredentialWrittenAt = writtenAt
				}
				return
			}
		}
		h.Generations = append(h.Generations, claudeAuthGeneration{
			AppliedGeneration: generation, ConfirmedAt: confirmedAt, CredentialWrittenAt: writtenAt,
		})
		if n := len(h.Generations); n > claudeAuthGenerations {
			h.Generations = h.Generations[n-claudeAuthGenerations:]
		}
	})
}

func claudeHistoryLaunch(s Settings, mode string, l Launch) claudeAuthLaunch {
	source := claudeSourceUnknown
	switch {
	case mode == protocol.ModeRemote || l.ExplicitClaudeRC != nil:
		source = claudeSourcePod
		if s.ClaudeLiveFile != "" {
			source = claudeSourceProjection
		}
	case mode == protocol.ModeLocal || mode == protocol.ModeTask:
		source = claudeSourceStatic
	}
	return claudeAuthLaunch{BootID: l.BootID, CreatedAt: l.CreatedAt.UTC(), Source: source}
}

func (d *Daemon) observeClaudeAuthHistory() {
	if d.Session.Agent != protocol.AgentClaude {
		return
	}
	l, ok := currentLaunch(d.S)
	var p agentPid
	if !ok || readJSONFile(d.S.statePath(pidFile), &p) != nil || !pidAlive(p) {
		return
	}
	launch := claudeHistoryLaunch(d.S, d.Session.Mode, l)
	if !launch.valid() {
		return
	}
	var observations []claudeAuthObservation
	if l.TUI && launch.Source == claudeSourceProjection {
		if tail, ok := readTranscriptTail(d.S, l.ConversationID); ok {
			observations = claudeAuthObservations(tail, launch.CreatedAt, d.now())
		}
	}
	updateClaudeAuthHistory(d.S, func(h *claudeAuthHistory) {
		h.Launch = &launch
		// Record rejections before acceptance so a delayed earlier rejection
		// can rebuild its lower bracket from accepted responses in the tail.
		for _, o := range observations {
			if o.Rejected {
				if g := h.generationAt(o.At); g != nil {
					g.reject(o.claudeAuthRejectionKey, launch)
				}
			}
		}
		for _, o := range observations {
			if !o.Rejected {
				if g := h.generationAt(o.At); g != nil {
					g.accept(o.At)
				}
			}
		}
	})
}

type claudeAuthObservation struct {
	claudeAuthRejectionKey
	Rejected bool
}

func claudeAuthObservations(tail []byte, launchAt, now time.Time) []claudeAuthObservation {
	var observations []claudeAuthObservation
	for line := range bytes.SplitSeq(tail, []byte("\n")) {
		var e transcriptEntry
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		// Defer future or same-tick entries. Counting them early could make a
		// replay move an observation across a later credential write.
		if err != nil || at.Before(launchAt) || !at.Before(now) {
			continue
		}
		// Use structured error evidence for history, including sidechains.
		// An unflagged entry needs model and usage evidence to count as accepted.
		rejected := e.IsAPIError && (string(e.APIErrorStatus) == "401" || string(e.Error) == `"`+authErrorCode+`"`)
		if !rejected && (e.IsAPIError || len(e.APIErrorStatus) != 0 || len(e.Error) != 0 || !acceptedResponse(e.Message)) {
			continue
		}
		observations = append(observations, claudeAuthObservation{
			claudeAuthRejectionKey{At: at.UTC(), Sidechain: e.IsSidechain}, rejected,
		})
	}
	return observations
}

func (h *claudeAuthHistory) generationAt(at time.Time) *claudeAuthGeneration {
	for i := len(h.Generations) - 1; i >= 0; i-- {
		if at.Equal(h.Generations[i].ConfirmedAt) {
			return nil // This timestamp cannot order the response and the write.
		}
		if !at.Before(h.Generations[i].ConfirmedAt) {
			return &h.Generations[i]
		}
	}
	return nil
}

func (g *claudeAuthGeneration) reject(key claudeAuthRejectionKey, launch claudeAuthLaunch) {
	if g.RejectionCount < claudeAuthCountLimit {
		if slices.Contains(g.SeenRejections, key) {
			return
		}
		g.RejectionCount++
		if g.RejectionCount == claudeAuthCountLimit {
			g.SeenRejections = nil
		} else {
			g.SeenRejections = append(g.SeenRejections, key)
		}
	}
	g.ErrorClass = claudeAuthRejected
	g.Sidechain = g.Sidechain || key.Sidechain
	g.MainChain = g.MainChain || !key.Sidechain
	if g.FirstRejectedAt.IsZero() || key.At.Before(g.FirstRejectedAt) {
		g.FirstRejectedAt = key.At
		g.FirstRejectionLaunch = &launch
		if !g.LastAcceptedBeforeFirstRejectionAt.Before(key.At) {
			g.LastAcceptedBeforeFirstRejectionAt = time.Time{}
		}
		g.accept(g.LastAcceptedAt)
	}
	if key.At.After(g.LastRejectedAt) {
		g.LastRejectedAt = key.At
	}
}

func (g *claudeAuthGeneration) accept(at time.Time) {
	if at.After(g.LastAcceptedAt) {
		g.LastAcceptedAt = at
	}
	if at.Before(g.FirstRejectedAt) && at.After(g.LastAcceptedBeforeFirstRejectionAt) {
		g.LastAcceptedBeforeFirstRejectionAt = at
	}
}
