package usageconsolidation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const (
	// Unknown is an explicit absence of caller-provided evidence, never zero.
	Unknown = "Unknown"
	// InitialManagedInvocationOnly describes the intentionally bounded attribution view.
	InitialManagedInvocationOnly = "InitialManagedInvocationOnly"
)

// Limits bounds one pure projection. They are caller-selected input bounds, not
// a collector policy or a resource reservation.
type Limits struct {
	MaxAllowanceSnapshots int
	MaxAllowanceItems     int
	MaxInitialInvocations int
	MaxAssociations       int
}

// DefaultLimits is a finite limit suitable for ordinary in-memory callers.
var DefaultLimits = Limits{
	MaxAllowanceSnapshots: 64,
	MaxAllowanceItems:     512,
	MaxInitialInvocations: 1024,
	MaxAssociations:       1024,
}

// UIDAssociation is caller-provided metadata for exactly one session UID. The
// projector neither validates nor infers its project, repository, or task.
type UIDAssociation struct {
	SessionUID string `json:"sessionUID"`
	Project    string `json:"project"`
	Repository string `json:"repository"`
	Task       string `json:"task"`
}

// Input holds already-authorized, typed DTOs. It deliberately accepts no raw
// session data, logs, credentials, registry, or provider inputs.
type Input struct {
	AllowanceSnapshots []providerallowance.ReadResponse `json:"allowanceSnapshots"`
	InitialInvocations []protocol.UsageObservation      `json:"initialInvocations"`
	Associations       []UIDAssociation                 `json:"associations"`
}

// Report has deliberately separate allowance and attribution views.
type Report struct {
	Allowance   AllowanceView   `json:"allowance"`
	Attribution AttributionView `json:"attribution"`
}

// AllowanceView retains each original cached read without summing, currentness
// recomputation, or an availability recommendation.
type AllowanceView struct {
	Snapshots                     []providerallowance.ReadResponse `json:"snapshots"`
	CurrentnessAtReportGeneration string                           `json:"currentnessAtReportGeneration"`
	AvailabilityRecommendation    string                           `json:"availabilityRecommendation"`
	Coverage                      Coverage                         `json:"coverage"`
}

// AttributionView contains one row per unambiguous initial invocation. It has
// no aggregate counters, model/account totals, or currency total.
type AttributionView struct {
	Rows              []AttributionRow `json:"rows"`
	WithheldConflicts []ConflictGroup  `json:"withheldConflicts"`
	Coverage          Coverage         `json:"coverage"`
}

// ConflictGroup identifies an initial-invocation group withheld because its
// otherwise valid replays disagree. It contains neither a payload nor totals.
type ConflictGroup struct {
	SessionUID string `json:"sessionUID"`
	AttemptID  string `json:"attemptID"`
	Reason     string `json:"reason"`
}

// Coverage states scope and known omissions; it never treats omitted work as zero.
type Coverage struct {
	Scope string   `json:"scope"`
	Gaps  []string `json:"gaps"`
}

// AttributionRow preserves the validated observation and caller-only labels.
type AttributionRow struct {
	Observation protocol.UsageObservation `json:"observation"`
	Labels      Labels                    `json:"labels"`
}

// Labels are Unknown unless exactly one matching caller association supplied
// them. No provider or model field contributes to these labels.
type Labels struct {
	Project    string `json:"project"`
	Repository string `json:"repository"`
	Task       string `json:"task"`
}

type candidate struct {
	observation protocol.UsageObservation
	canonical   []byte
}

var (
	errLimits = errors.New("usage consolidation limits are invalid")
	errInput  = errors.New("usage consolidation input exceeds limits")
)

// Project produces a deterministic, deep-copied report. It reads no clock and
// does not call providers. Invalid initial observations are rejected rather
// than being silently attributed.
func Project(in Input, limits Limits) (Report, error) {
	if limits.MaxAllowanceSnapshots <= 0 || limits.MaxAllowanceItems <= 0 || limits.MaxInitialInvocations <= 0 || limits.MaxAssociations <= 0 {
		return Report{}, errLimits
	}
	if len(in.AllowanceSnapshots) > limits.MaxAllowanceSnapshots || len(in.InitialInvocations) > limits.MaxInitialInvocations || len(in.Associations) > limits.MaxAssociations {
		return Report{}, errInput
	}
	items := 0
	for _, snapshot := range in.AllowanceSnapshots {
		items += len(snapshot.Items)
		if items > limits.MaxAllowanceItems {
			return Report{}, errInput
		}
	}

	rows, conflicts, err := projectRows(in.InitialInvocations, in.Associations)
	if err != nil {
		return Report{}, err
	}
	snapshots, err := clone(in.AllowanceSnapshots)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Allowance: AllowanceView{
			Snapshots:                     snapshots,
			CurrentnessAtReportGeneration: Unknown,
			AvailabilityRecommendation:    Unknown,
			Coverage: Coverage{Scope: "CallerProvidedAllowanceSnapshots", Gaps: []string{
				"missing snapshots are a coverage gap, not zero account consumption",
				"allowance coverage does not establish attribution coverage",
			}},
		},
		Attribution: AttributionView{
			Rows:              rows,
			WithheldConflicts: conflicts,
			Coverage: Coverage{Scope: InitialManagedInvocationOnly, Gaps: []string{
				"later conversation turns are not measured",
				"uncaptured or off-platform work is not measured",
				"missing associations are not measured labels and are not inferred",
				"attribution coverage does not establish allowance coverage",
				"Cursor, Grok, mandatory full-V1, and native188 gaps remain explicit",
			}},
		},
	}, nil
}

func projectRows(observations []protocol.UsageObservation, associations []UIDAssociation) ([]AttributionRow, []ConflictGroup, error) {
	groups := make(map[string][]candidate, len(observations))
	for _, observation := range observations {
		if err := observation.Validate(); err != nil {
			return nil, nil, fmt.Errorf("invalid initial invocation: %w", err)
		}
		canonical, err := json.Marshal(observation)
		if err != nil {
			return nil, nil, fmt.Errorf("canonical initial invocation: %w", err)
		}
		identity := observation.Source.SessionUID + "\x00" + observation.Source.AttemptID
		groups[identity] = append(groups[identity], candidate{observation: observation, canonical: canonical})
	}

	labelsByUID := associationLabels(associations)
	identities := make([]string, 0, len(groups))
	for identity := range groups {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	rows := make([]AttributionRow, 0, len(identities))
	conflicts := make([]ConflictGroup, 0)
	for _, identity := range identities {
		candidates := groups[identity]
		if conflicted(candidates) {
			conflicts = append(conflicts, ConflictGroup{
				SessionUID: candidates[0].observation.Source.SessionUID,
				AttemptID:  candidates[0].observation.Source.AttemptID,
				Reason:     "ConflictingInitialInvocationReplay",
			})
			continue
		}
		observation, err := clone(candidates[0].observation)
		if err != nil {
			return nil, nil, err
		}
		labels, ok := labelsByUID[observation.Source.SessionUID]
		if !ok {
			labels = unknownLabels()
		}
		rows = append(rows, AttributionRow{Observation: observation, Labels: labels})
	}
	return rows, conflicts, nil
}

func conflicted(candidates []candidate) bool {
	for i := 1; i < len(candidates); i++ {
		if !bytes.Equal(candidates[0].canonical, candidates[i].canonical) {
			return true
		}
	}
	return false
}

func associationLabels(associations []UIDAssociation) map[string]Labels {
	result := make(map[string]Labels, len(associations))
	conflicts := make(map[string]bool)
	for _, association := range associations {
		labels := Labels{Project: labelOrUnknown(association.Project), Repository: labelOrUnknown(association.Repository), Task: labelOrUnknown(association.Task)}
		if current, ok := result[association.SessionUID]; ok && current != labels {
			conflicts[association.SessionUID] = true
			continue
		}
		result[association.SessionUID] = labels
	}
	for uid := range conflicts {
		result[uid] = unknownLabels()
	}
	return result
}

func unknownLabels() Labels {
	return Labels{Project: Unknown, Repository: Unknown, Task: Unknown}
}

func labelOrUnknown(label string) string {
	if label == "" {
		return Unknown
	}
	return label
}

func clone[T any](value T) (T, error) {
	var copy T
	encoded, err := json.Marshal(value)
	if err != nil {
		return copy, err
	}
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return copy, err
	}
	return copy, nil
}
