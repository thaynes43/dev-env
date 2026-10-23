package apiv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

const (
	// CapturedUsageScope is the only receipt scope: direct children captured
	// while this parent object still exists.
	CapturedUsageScope = "CapturedDirectChildInitialInvocations"
	// CapturedUsageCoverage is capture-only. It is not a claim that every child
	// was seen.
	CapturedUsageCoverage = "CaptureOnly"
	// CapturedUsageRetention keeps the ledger only as long as the parent object.
	CapturedUsageRetention = "ParentLifetime"
	// CapturedUsageMaxReceipts is the row cap. A further child sets overflow
	// and does not evict a row.
	CapturedUsageMaxReceipts = 64
	// CapturedUsageMaxBytes is the serialized cap for both the stored ledger
	// and this direct-v1 view.
	CapturedUsageMaxBytes   = 262144
	capturedUsageKeyVersion = "1"
)

// CapturedUsageKey is the stable identity of one captured initial observation.
// The version is the capture-key version, not the observation version.
func CapturedUsageKey(parentUID, childUID, podUID, attemptID, observationID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{capturedUsageKeyVersion, parentUID, childUID, podUID, attemptID, observationID}, "\n")))
	return hex.EncodeToString(sum[:])
}

// NormalizeCapturedUsage checks one explicit capture view. A nil view is
// unavailable. Any malformed or oversized view is replaced by one closed view
// with no receipts, so a caller cannot keep a trusted prefix.
func NormalizeCapturedUsage(sessionUID string, in *CapturedUsage) *CapturedUsage {
	closed := func(reason string) *CapturedUsage {
		switch reason {
		case "disabled", "unavailable", "malformed", "oversized":
		default:
			reason = "malformed"
		}
		return &CapturedUsage{Available: false, Reason: reason}
	}
	if in == nil {
		return closed("unavailable")
	}
	if !in.Available {
		if in.Reason == "" || in.Scope != "" || in.Coverage != "" || in.Retention != "" || in.Overflow || len(in.Receipts) != 0 {
			return closed("malformed")
		}
		return closed(in.Reason)
	}
	if in.Reason != "" || sessionUID == "" || in.Scope != CapturedUsageScope || in.Coverage != CapturedUsageCoverage || in.Retention != CapturedUsageRetention {
		return closed("malformed")
	}
	if len(in.Receipts) > CapturedUsageMaxReceipts {
		return closed("oversized")
	}
	if len(in.Receipts) == 0 && !in.Overflow {
		return closed("unavailable")
	}
	rows := append([]CapturedUsageReceipt(nil), in.Receipts...)
	slices.SortFunc(rows, func(a, b CapturedUsageReceipt) int { return strings.Compare(a.Key, b.Key) })
	seenKey := map[string]struct{}{}
	seenChild := map[string]struct{}{}
	for i := range rows {
		row := &rows[i]
		if row.CapturedAt.IsZero() || !safeCapturedName(row.ChildName) || row.ParentUID != sessionUID || row.Observation == nil {
			return closed("malformed")
		}
		obs := *row.Observation
		obs.ObservedAt = obs.ObservedAt.UTC()
		if obs.Validate() != nil {
			return closed("malformed")
		}
		childUID := obs.Source.SessionUID
		key := CapturedUsageKey(sessionUID, childUID, obs.Source.PodUID, obs.Source.AttemptID, obs.ID)
		if _, ok := seenKey[key]; ok || row.Key != key {
			return closed("malformed")
		}
		if childUID == "" {
			return closed("malformed")
		}
		if _, ok := seenChild[childUID]; ok {
			return closed("malformed")
		}
		seenKey[key] = struct{}{}
		seenChild[childUID] = struct{}{}
		row.CapturedAt = row.CapturedAt.UTC()
		row.Observation = &obs
	}
	out := &CapturedUsage{
		Available: true, Scope: in.Scope, Coverage: in.Coverage, Retention: in.Retention,
		Overflow: in.Overflow, Receipts: rows,
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > CapturedUsageMaxBytes {
		return closed("oversized")
	}
	return out
}

func safeCapturedName(name string) bool {
	if len(name) > 253 || name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
