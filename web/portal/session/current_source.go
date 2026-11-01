package session

import (
	"math"
	"strconv"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

const (
	currentSourceIdentityReason    = "Stable account identity is unavailable"
	currentSourceProvenance        = "Retained Codex tool report"
	currentSourceUnavailableReason = "Current source unavailable"
)

// currentSourceReader is one immutable Codex current-source snapshot.
// Allowances decodes and validates the copied bytes on every read.
// It replaces only the Codex card. It does not merge a bound allowance,
// call MapProductAllowance, or read a file.
type currentSourceReader struct {
	raw       []byte
	selection providerallowance.CurrentSourceSelection
}

// NewCurrentSourceReader stores one current-source report for later projection.
// raw and selected are copied. The constructor does not fetch or write.
// Callers pass the reader to SetAllowanceReader themselves. Production does not.
func NewCurrentSourceReader(raw []byte, selected providerallowance.CurrentSourceSelection) AllowanceReader {
	return &currentSourceReader{
		raw:       append([]byte(nil), raw...),
		selection: copyCurrentSourceSelection(selected),
	}
}

func copyCurrentSourceSelection(in providerallowance.CurrentSourceSelection) providerallowance.CurrentSourceSelection {
	return providerallowance.CurrentSourceSelection{
		PublisherRef:        in.PublisherRef,
		ProducerIncarnation: in.ProducerIncarnation,
		SourceGeneration:    in.SourceGeneration,
		MaxAge:              in.MaxAge,
	}
}

// Allowances returns disconnected provider cards with only Codex replaced.
// An empty snapshot, a zero now, or any failed decode or validation yields
// Codex Unknown and no windows. The card does not include the raw error,
// a publisher id, or an incarnation.
func (r *currentSourceReader) Allowances(now time.Time) []portal.ProductAllowance {
	if r == nil {
		return disconnectedAllowances()
	}
	report, ok := r.validatedCurrentSource(now)
	if !ok {
		return currentSourceUnavailable()
	}
	return projectCurrentSource(report, now)
}

func (r *currentSourceReader) validatedCurrentSource(now time.Time) (providerallowance.CurrentSourceReport, bool) {
	if len(r.raw) == 0 || now.IsZero() {
		return providerallowance.CurrentSourceReport{}, false
	}
	report, err := providerallowance.DecodeCurrentSource(r.raw)
	if err != nil {
		return providerallowance.CurrentSourceReport{}, false
	}
	if err := providerallowance.ValidateCurrentSource(report, r.selection, now); err != nil {
		return providerallowance.CurrentSourceReport{}, false
	}
	return report, true
}

func projectCurrentSource(report providerallowance.CurrentSourceReport, now time.Time) []portal.ProductAllowance {
	used, ok := currentSourceUsedPercent(report.UsedPercent)
	if !ok {
		return currentSourceUnavailable()
	}
	windowText, ok := currentSourceWindowText(report.WindowDurationMinutes)
	if !ok {
		return currentSourceUnavailable()
	}
	resetAt := report.ResetsAt
	resetDisplay := allowanceTime(&resetAt)
	if resetDisplay == "Unknown" {
		return currentSourceUnavailable()
	}
	observed := "Unknown"
	observedAt := ""
	if report.ProviderUpdatedAt != nil {
		updated := *report.ProviderUpdatedAt
		observed = allowanceTime(&updated)
		observedAt = updated
	}
	window := portal.ProductAllowanceWindow{
		Percent:        reportedCurrentSourcePercent(report.UsedPercent, report.RemainingPercent),
		Window:         windowText,
		Reset:          resetDisplay,
		Observed:       observed,
		Provenance:     currentSourceProvenance,
		PercentValue:   used,
		PercentKnown:   true,
		Direction:      "used",
		ResetAt:        resetAt,
		ObservedAt:     observedAt,
		WindowKind:     "",
		ResetPrecision: "instant",
		AccountKey:     "",
		AsOf:           now,
		RetrievedAt:    report.RetrievedAt,
	}
	return replaceCodexAllowance(portal.ProductAllowance{
		Provider:   "Codex",
		Value:      portal.AllowanceCurrentSource,
		Reason:     currentSourceIdentityReason,
		Provenance: currentSourceProvenance,
		Windows:    []portal.ProductAllowanceWindow{window},
	})
}

func reportedCurrentSourcePercent(used, remaining string) string {
	return used + "% used, " + remaining + "% remaining"
}

func currentSourceUsedPercent(decimal string) (float64, bool) {
	value, err := strconv.ParseFloat(decimal, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// currentSourceWindowText formats a positive minute count with the shared
// duration helper. A count that helper rejects is not shown as a number.
func currentSourceWindowText(minutes int64) (string, bool) {
	count := providerallowance.Count(minutes)
	amount, unit, ok := allowanceDuration(providerallowance.Window{DurationMinutes: &count})
	if !ok {
		return "", false
	}
	if amount != 1 {
		unit += "s"
	}
	return strconv.FormatInt(amount, 10) + " " + unit, true
}

func currentSourceUnavailable() []portal.ProductAllowance {
	out := disconnectedAllowances()
	for i := range out {
		if out[i].Provider != "Codex" {
			continue
		}
		out[i] = portal.ProductAllowance{
			Provider: out[i].Provider,
			Value:    portal.AllowanceUnknown,
			Reason:   currentSourceUnavailableReason,
		}
		return out
	}
	return out
}

func replaceCodexAllowance(card portal.ProductAllowance) []portal.ProductAllowance {
	out := disconnectedAllowances()
	for i := range out {
		if out[i].Provider != "Codex" {
			continue
		}
		out[i] = card
		return out
	}
	return currentSourceUnavailable()
}
