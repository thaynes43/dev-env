package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
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
	AppliedGeneration                uint64            `json:"appliedGeneration"`
	ConfirmedAt                      time.Time         `json:"confirmedAt"`
	CredentialWrittenAt              time.Time         `json:"credentialWrittenAt,omitzero"`
	FirstRejection                   claudeAuthTime    `json:"firstRejection,omitzero"`
	LastRejection                    claudeAuthTime    `json:"lastRejection,omitzero"`
	LastAcceptedBeforeFirstRejection claudeAuthTime    `json:"lastAcceptedBeforeFirstRejection,omitzero"`
	LastAccepted                     claudeAuthTime    `json:"lastAccepted,omitzero"`
	FirstRejectionLaunch             *claudeAuthLaunch `json:"firstRejectionLaunch,omitempty"`
	LastRejectionLaunch              *claudeAuthLaunch `json:"lastRejectionLaunch,omitempty"`
	MainChain                        bool              `json:"mainChain,omitempty"`
	Sidechain                        bool              `json:"sidechain,omitempty"`
	ErrorClass                       string            `json:"errorClass,omitempty"`
	RejectionCount                   uint8             `json:"rejectionCount,omitempty"`
	// Equal timestamps on different chains are distinct. Keep exact keys up
	// to saturation so delayed entries and replays survive a daemon restart.
	SeenRejections []claudeAuthRejectionKey `json:"seenRejections,omitempty"`
}

type claudeAuthRejectionKey struct {
	At        time.Time `json:"at"`
	Sidechain bool      `json:"sidechain,omitempty"`
	Count     uint8     `json:"count"`
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
			(g.FirstRejectionLaunch != nil && !g.FirstRejectionLaunch.valid()) ||
			(g.LastRejectionLaunch != nil && !g.LastRejectionLaunch.valid()) {
			return false
		}
		var seen int
		for _, key := range g.SeenRejections {
			if key.At.IsZero() || key.Count == 0 {
				return false
			}
			seen += int(key.Count)
		}
		if (g.RejectionCount < claudeAuthCountLimit && int(g.RejectionCount) != seen) ||
			(g.RejectionCount == claudeAuthCountLimit && seen != 0) {
			return false
		}
		previous = g.AppliedGeneration
	}
	return true
}

func readClaudeAuthHistory(s Settings) (claudeAuthHistory, []byte, error) {
	var h claudeAuthHistory
	f, err := os.OpenFile(s.statePath(claudeAuthHistoryFile), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return h, nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > claudeAuthHistorySize {
		return h, nil, errors.New("invalid Claude auth history file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, claudeAuthHistorySize+1))
	if err != nil || len(raw) > claudeAuthHistorySize || codexauth.DecodeStrict(raw, &h) != nil || !h.valid() {
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
		observations = readClaudeAuthObservations(d.S, l, d.now())
	}
	updateClaudeAuthHistory(d.S, func(h *claudeAuthHistory) {
		h.Launch = &launch
		// Record rejections before acceptance so a delayed earlier rejection
		// can rebuild its lower bracket from accepted responses in the tail.
		for _, o := range observations {
			if o.Rejected {
				if g := h.generationAt(o.Stamp); g != nil {
					g.reject(o, launch)
				}
			}
		}
		for _, o := range observations {
			if !o.Rejected {
				if g := h.generationAt(o.Stamp); g != nil {
					g.accept(o.Stamp)
				}
			}
		}
	})
}

func (h *claudeAuthHistory) generationAt(stamp claudeAuthTime) *claudeAuthGeneration {
	for i := len(h.Generations) - 1; i >= 0; i-- {
		confirmed := h.Generations[i].ConfirmedAt
		if stamp.At.After(confirmed) {
			return &h.Generations[i]
		}
		if stamp.end().After(confirmed) {
			return nil // The recorded precision cannot order the response and the write.
		}
	}
	return nil
}

func (g *claudeAuthGeneration) reject(o claudeAuthObservation, launch claudeAuthLaunch) {
	if g.RejectionCount < claudeAuthCountLimit {
		g.countRejection(o)
	}
	g.ErrorClass = claudeAuthRejected
	g.Sidechain = g.Sidechain || o.Sidechain
	g.MainChain = g.MainChain || !o.Sidechain
	if g.FirstRejection.IsZero() || o.Stamp.At.Before(g.FirstRejection.At) {
		g.FirstRejection = o.Stamp
		g.FirstRejectionLaunch = &launch
		if g.LastAcceptedBeforeFirstRejection.end().After(o.Stamp.At) {
			g.LastAcceptedBeforeFirstRejection = claudeAuthTime{}
		}
		g.accept(g.LastAccepted)
	}
	if o.Stamp.At.After(g.LastRejection.At) {
		g.LastRejection = o.Stamp
		g.LastRejectionLaunch = &launch
	}
}

// Store only the highest observed multiplicity at each timestamp and chain.
// A native child can appear in both its own file and the parent file.
func (g *claudeAuthGeneration) countRejection(o claudeAuthObservation) {
	for i := range g.SeenRejections {
		key := &g.SeenRejections[i]
		if key.At.Equal(o.Stamp.At) && key.Sidechain == o.Sidechain {
			if o.Count > key.Count {
				g.RejectionCount = min(g.RejectionCount+o.Count-key.Count, claudeAuthCountLimit)
				key.Count = o.Count
			}
			if g.RejectionCount == claudeAuthCountLimit {
				g.SeenRejections = nil
			}
			return
		}
	}
	g.RejectionCount = min(g.RejectionCount+o.Count, claudeAuthCountLimit)
	if g.RejectionCount == claudeAuthCountLimit {
		g.SeenRejections = nil
	} else {
		g.SeenRejections = append(g.SeenRejections, claudeAuthRejectionKey{At: o.Stamp.At, Sidechain: o.Sidechain, Count: o.Count})
	}
}

func (g *claudeAuthGeneration) accept(stamp claudeAuthTime) {
	if stamp.At.After(g.LastAccepted.At) {
		g.LastAccepted = stamp
	}
	if !stamp.end().After(g.FirstRejection.At) && stamp.At.After(g.LastAcceptedBeforeFirstRejection.At) {
		g.LastAcceptedBeforeFirstRejection = stamp
	}
}
