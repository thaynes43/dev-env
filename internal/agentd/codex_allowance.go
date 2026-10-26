package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// ReadCodexNativeAllowance performs one documented, metadata-only read on the
// already selected native-host socket. It never starts a daemon, reads auth, or
// calls a thread, turn, login, refresh, reset, redemption, activity, spending,
// or model method. fetchedAt is capture time only: the protocol supplies no
// provider-observation time, so ProviderObservedAt and freshness stay Unknown.
func ReadCodexNativeAllowance(ctx context.Context, s Settings, expectedPID int, fetchedAt time.Time) (providerallowance.ReadResponse, error) {
	r, err := openCodexHostRPC(ctx, s, expectedPID)
	if err != nil {
		return providerallowance.ReadResponse{}, errCodexHostUnknown
	}
	defer r.conn.Close()
	var init struct {
		UserAgent string `json:"userAgent"`
	}
	if r.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "dev_env_allowance_reader", "version": managedCodexVersion}, "capabilities": map[string]bool{"experimentalApi": true}}, &init) != nil || init.UserAgent == "" {
		return providerallowance.ReadResponse{}, errCodexHostUnknown
	}
	if err := r.conn.WriteJSON(map[string]string{"method": "initialized"}); err != nil {
		return providerallowance.ReadResponse{}, errCodexHostUnknown
	}
	var account struct {
		Account json.RawMessage `json:"account"`
	}
	if r.call("account/read", map[string]bool{"refreshToken": false}, &account) != nil {
		return providerallowance.ReadResponse{}, errCodexHostUnknown
	}
	var limits codexRateLimits
	if r.call("account/rateLimits/read", map[string]bool{"excludeResetCreditDetails": true, "supportsLunaReserve": false}, &limits) != nil {
		return providerallowance.ReadResponse{}, errCodexHostUnknown
	}
	return codexAllowanceRead(account.Account != nil && string(account.Account) != "null", limits, fetchedAt.UTC())
}

type codexRateLimits struct {
	RateLimits          *codexLimit           `json:"rateLimits"`
	RateLimitsByLimitID map[string]codexLimit `json:"rateLimitsByLimitId"`
	ResetCredits        *struct {
		AvailableCount *json.Number `json:"availableCount"`
	} `json:"rateLimitResetCredits"`
}
type codexLimit struct {
	LimitID   string          `json:"limitId"`
	Primary   *codexWindow    `json:"primary"`
	Secondary *codexWindow    `json:"secondary"`
	Reached   json.RawMessage `json:"rateLimitReachedType"`
}
type codexWindow struct {
	Used     *json.Number `json:"usedPercent"`
	Duration *json.Number `json:"windowDurationMins"`
	ResetsAt *json.Number `json:"resetsAt"`
}

func codexAllowanceRead(accountPresent bool, limits codexRateLimits, fetchedAt time.Time) (providerallowance.ReadResponse, error) {
	if fetchedAt.IsZero() {
		return providerallowance.ReadResponse{}, fmt.Errorf("native allowance fetch time is required")
	}
	obs := providerallowance.Observation{Schema: providerallowance.SchemaObservation, ObservationID: "codex-native-on-demand", ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", Sequence: 1, Provider: "openai", Source: providerallowance.Source{Kind: providerallowance.SourceNativeRPC, Version: "CLI-and-daemon0.160.1", CapabilityRef: "story188:codex-native-field-schema"}, Scope: providerallowance.Scope{Products: []string{"codex"}, BindingState: providerallowance.BindingUnknown}, CapturedAt: fetchedAt.Format(time.RFC3339Nano), Availability: providerallowance.AvailabilityAvailable, Completeness: providerallowance.CompletenessPartial, DirectFields: []providerallowance.DirectField{{Field: "accountScopePresent", OriginalType: providerallowance.TypeBoolean, LexicalValue: boolLex(accountPresent)}}, Measures: []providerallowance.Measure{}, Derivations: []providerallowance.Derivation{}, EvidenceRefs: []string{"codex-unix-native-sanitized.json"}}
	if limits.ResetCredits == nil {
		obs.DirectFields = append(obs.DirectFields, providerallowance.DirectField{Field: "resetCredits.detailsState", OriginalType: providerallowance.TypeString, LexicalValue: strPtr("unknown")})
	} else if limits.ResetCredits.AvailableCount != nil {
		obs.DirectFields = append(obs.DirectFields, providerallowance.DirectField{Field: "resetCredits.availableCount", OriginalType: providerallowance.TypeDecimal, LexicalValue: strPtr(limits.ResetCredits.AvailableCount.String())}, providerallowance.DirectField{Field: "resetCredits.detailsState", OriginalType: providerallowance.TypeString, LexicalValue: strPtr("not_requested")})
	}
	keys := make([]string, 0, len(limits.RateLimitsByLimitID))
	for k := range limits.RateLimitsByLimitID {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		obs.Measures = appendCodexMeasures(obs.Measures, "bucket."+k, limits.RateLimitsByLimitID[k])
	}
	if limits.RateLimits != nil {
		obs.Measures = appendCodexMeasures(obs.Measures, "legacy", *limits.RateLimits)
	}
	return providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: "codex-native-on-demand", ServedAt: fetchedAt.Format(time.RFC3339Nano), Items: []providerallowance.ReadItem{{Observation: obs, ReceivedAt: fetchedAt.Format(time.RFC3339Nano), Validation: providerallowance.ValidationRecord{State: providerallowance.ValidationPartial, ValidationRefs: []string{}}, Binding: providerallowance.BindingRecord{State: providerallowance.BindingUnknown}, Freshness: providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: fetchedAt.Format(time.RFC3339Nano), Basis: providerallowance.BasisCapture, Reason: "Native account/rate read has no provider observation timestamp; fetch is not provider freshness"}, CurrentMeasures: []providerallowance.CurrentMeasure{}}}, SourceStatuses: []providerallowance.SourceStatus{}}, nil
}
func appendCodexMeasures(out []providerallowance.Measure, prefix string, l codexLimit) []providerallowance.Measure {
	for _, p := range []struct {
		name string
		w    *codexWindow
	}{{"primary", l.Primary}, {"secondary", l.Secondary}} {
		if p.w == nil {
			out = append(out, codexMeasure(prefix+"."+p.name, nil))
			continue
		}
		out = append(out, codexMeasure(prefix+"."+p.name, p.w))
	}
	return out
}
func codexMeasure(id string, w *codexWindow) providerallowance.Measure {
	m := providerallowance.Measure{ID: id, Category: providerallowance.CategoryIncluded, Window: providerallowance.Window{Ref: id + ".window", ResetPrecision: providerallowance.ResetUnknown}, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed, Value: providerallowance.MeasureValue{State: providerallowance.ValueUnknown}}
	if w == nil {
		return m
	}
	if w.Used != nil {
		m.Value = providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: strPtr(w.Used.String())}
	}
	if w.Duration != nil {
		if n, e := strconv.ParseInt(w.Duration.String(), 10, 64); e == nil && n >= 0 {
			x := providerallowance.Count(n)
			m.Window.DurationMinutes = &x
		}
	}
	if w.ResetsAt != nil {
		if n, e := strconv.ParseInt(w.ResetsAt.String(), 10, 64); e == nil && n >= 0 {
			x := time.Unix(n, 0).UTC().Format(time.RFC3339)
			m.Window.ResetAt = &x
			m.Window.ResetPrecision = providerallowance.ResetInstant
		}
	}
	return m
}
func strPtr(s string) *string { return &s }
func boolLex(v bool) *string {
	if v {
		return strPtr("true")
	}
	return strPtr("false")
}
