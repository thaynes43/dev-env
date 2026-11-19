package protocol

import (
	"regexp"
	"slices"
	"strconv"
	"time"
)

const (
	ClaudeAuthHistoryMaxBytes       = 16 << 10
	ClaudeAuthHistoryMaxGenerations = 3
	ClaudeAuthHistoryMaxRejections  = 64
	ClaudeAuthObservationTiming     = "assistant-transcript-proxy"
	ClaudeAuthErrorClass            = "authentication"
)

// ClaudeAuthHistory is diagnostic evidence supplied by a session's own agentd.
// It is shared by the heartbeat, AgentSession schema and opt-in show response.
// +kubebuilder:validation:XValidation:rule="self.clockSkewUnknown",message="clock skew must be unknown"
type ClaudeAuthHistory struct {
	// +kubebuilder:validation:Enum=1
	Version int32             `json:"version"`
	Launch  *ClaudeAuthLaunch `json:"launch,omitempty"`
	// +kubebuilder:validation:MaxLength=26
	// +kubebuilder:validation:Enum=assistant-transcript-proxy
	ObservationTiming string `json:"observationTiming"`
	ClockSkewUnknown  bool   `json:"clockSkewUnknown"`
	// +kubebuilder:validation:MaxItems=3
	// +kubebuilder:validation:XValidation:rule="(size(self) < 2 || uint(self[0].appliedGeneration) < uint(self[1].appliedGeneration)) && (size(self) < 3 || uint(self[1].appliedGeneration) < uint(self[2].appliedGeneration))",message="applied generations must increase"
	Generations []ClaudeAuthGeneration `json:"generations,omitempty"`
}

type ClaudeAuthLaunch struct {
	// +kubebuilder:validation:MaxLength=12
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{12}$`
	BootID    string              `json:"bootId"`
	CreatedAt ClaudeAuthTimestamp `json:"createdAt"`
	// +kubebuilder:validation:MaxLength=18
	// +kubebuilder:validation:Enum=projection;static-environment;pod-owned;unknown
	Source string `json:"source"`
}

// ClaudeAuthTimestamp preserves nanoseconds through the CRD and JSON readers.
// +kubebuilder:validation:MaxLength=30
// +kubebuilder:validation:Format=date-time
// +kubebuilder:validation:Pattern=`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`
type ClaudeAuthTimestamp string

type ClaudeAuthTime struct {
	At ClaudeAuthTimestamp `json:"at"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000000000
	PrecisionNanos int64 `json:"precisionNanos"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.rejectionCount) || self.rejectionCount < 64 ? (has(self.rejectionCount) ? self.rejectionCount : 0) == (has(self.seenRejections) ? self.seenRejections.map(r, r.count).sum() : 0) : (!has(self.seenRejections) || size(self.seenRejections) == 0)",message="rejection keys must account for the count until saturation"
type ClaudeAuthGeneration struct {
	// AppliedGeneration is decimal so JSON and Kubernetes preserve the entire
	// unsigned generation range. It describes the applied file generation.
	// +kubebuilder:validation:MaxLength=20
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]{0,19}$`
	// +kubebuilder:validation:XValidation:rule="uint(self) > uint(0)",message="applied generation must be a positive uint64"
	AppliedGeneration                string              `json:"appliedGeneration"`
	ConfirmedAt                      ClaudeAuthTimestamp `json:"confirmedAt"`
	CredentialWrittenAt              ClaudeAuthTimestamp `json:"credentialWrittenAt,omitempty"`
	FirstRejection                   *ClaudeAuthTime     `json:"firstRejection,omitempty"`
	LastRejection                    *ClaudeAuthTime     `json:"lastRejection,omitempty"`
	LastAcceptedBeforeFirstRejection *ClaudeAuthTime     `json:"lastAcceptedBeforeFirstRejection,omitempty"`
	FirstAccepted                    *ClaudeAuthTime     `json:"firstAccepted,omitempty"`
	LastAccepted                     *ClaudeAuthTime     `json:"lastAccepted,omitempty"`
	FirstAcceptedAfterLastRejection  *ClaudeAuthTime     `json:"firstAcceptedAfterLastRejection,omitempty"`
	FirstRejectionLaunch             *ClaudeAuthLaunch   `json:"firstRejectionLaunch,omitempty"`
	LastRejectionLaunch              *ClaudeAuthLaunch   `json:"lastRejectionLaunch,omitempty"`
	MainChain                        bool                `json:"mainChain,omitempty"`
	Sidechain                        bool                `json:"sidechain,omitempty"`
	// +kubebuilder:validation:MaxLength=14
	// +kubebuilder:validation:Enum=authentication
	ErrorClass string `json:"errorClass,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=64
	RejectionCount int32 `json:"rejectionCount,omitempty"`
	// +kubebuilder:validation:MaxItems=63
	SeenRejections []ClaudeAuthRejectionKey `json:"seenRejections,omitempty"`
}

type ClaudeAuthRejectionKey struct {
	At        ClaudeAuthTimestamp `json:"at"`
	Sidechain bool                `json:"sidechain,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=63
	Count int32 `json:"count"`
}

var claudeAuthBootID = regexp.MustCompile(`^[0-9a-f]{12}$`)

func (l *ClaudeAuthLaunch) valid() bool {
	if l == nil {
		return true
	}
	switch l.Source {
	case "projection", "static-environment", "pod-owned", "unknown":
		return claudeAuthBootID.MatchString(l.BootID) && l.CreatedAt.valid()
	}
	return false
}

func (at ClaudeAuthTimestamp) valid() bool {
	t, err := time.Parse(time.RFC3339Nano, string(at))
	return err == nil && !t.IsZero() && t.UTC().Format(time.RFC3339Nano) == string(at)
}

func (t *ClaudeAuthTime) valid() bool {
	return t == nil || (t.At.valid() && t.PrecisionNanos >= 1 && t.PrecisionNanos <= int64(time.Second))
}

// Valid checks the bounded schema before the operator offers optional
// evidence to Kubernetes, where a validation error would also reject liveness.
func (h *ClaudeAuthHistory) Valid() bool {
	if h == nil || h.Version != 1 || h.ObservationTiming != ClaudeAuthObservationTiming ||
		!h.ClockSkewUnknown || !h.Launch.valid() || len(h.Generations) > ClaudeAuthHistoryMaxGenerations {
		return false
	}
	var previous uint64
	for _, g := range h.Generations {
		generation, err := strconv.ParseUint(g.AppliedGeneration, 10, 64)
		if err != nil || generation <= previous || strconv.FormatUint(generation, 10) != g.AppliedGeneration || !g.valid() {
			return false
		}
		previous = generation
	}
	return true
}

func (g ClaudeAuthGeneration) valid() bool {
	if !g.ConfirmedAt.valid() || (g.CredentialWrittenAt != "" && !g.CredentialWrittenAt.valid()) ||
		!g.FirstRejection.valid() || !g.LastRejection.valid() || !g.LastAcceptedBeforeFirstRejection.valid() ||
		!g.FirstAccepted.valid() || !g.LastAccepted.valid() || !g.FirstAcceptedAfterLastRejection.valid() ||
		!g.FirstRejectionLaunch.valid() || !g.LastRejectionLaunch.valid() ||
		(g.ErrorClass != "" && g.ErrorClass != ClaudeAuthErrorClass) ||
		g.RejectionCount < 0 || g.RejectionCount > ClaudeAuthHistoryMaxRejections || len(g.SeenRejections) >= ClaudeAuthHistoryMaxRejections {
		return false
	}
	var seen int32
	for _, key := range g.SeenRejections {
		if !key.At.valid() || key.Count < 1 || key.Count >= ClaudeAuthHistoryMaxRejections {
			return false
		}
		seen += key.Count
	}
	if g.RejectionCount == ClaudeAuthHistoryMaxRejections {
		return seen == 0
	}
	return seen == g.RejectionCount
}

// DeepCopy is used by the generated AgentSession DeepCopy.
func (h *ClaudeAuthHistory) DeepCopy() *ClaudeAuthHistory {
	if h == nil {
		return nil
	}
	out := new(*h)
	if h.Launch != nil {
		out.Launch = new(*h.Launch)
	}
	if h.Generations == nil {
		return out
	}
	out.Generations = make([]ClaudeAuthGeneration, len(h.Generations))
	for i, g := range h.Generations {
		out.Generations[i] = g.deepCopy()
	}
	return out
}

func (g ClaudeAuthGeneration) deepCopy() ClaudeAuthGeneration {
	for _, stamp := range []**ClaudeAuthTime{&g.FirstRejection, &g.LastRejection, &g.LastAcceptedBeforeFirstRejection,
		&g.FirstAccepted, &g.LastAccepted, &g.FirstAcceptedAfterLastRejection} {
		if *stamp != nil {
			*stamp = new(**stamp)
		}
	}
	if g.FirstRejectionLaunch != nil {
		g.FirstRejectionLaunch = new(*g.FirstRejectionLaunch)
	}
	if g.LastRejectionLaunch != nil {
		g.LastRejectionLaunch = new(*g.LastRejectionLaunch)
	}
	g.SeenRejections = slices.Clone(g.SeenRejections)
	return g
}
