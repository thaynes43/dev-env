package providerallowance

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// ErrAggregateTrust is the only refusal from ParseAggregateTrust.
// It contains no submitted text.
var ErrAggregateTrust = errors.New("aggregate_trust_refused")

// AggregateTrust is caller-owned source, account, and registry input.
// It is not read from a cached-read payload.
type AggregateTrust struct {
	Contexts []TrustedContext
	Registry *StaticRegistry
	MaxAge   int64
}

const aggregateTrustMaxBytes = 48 * 1024

var aggregateTrustKeys = []string{"maxAgeSeconds", "contexts", "registry"}

// ParseAggregateTrust accepts one bounded trust object.
// maxAgeSeconds is seconds, from 1 through 1800. Contexts and registry rules
// are copied. A malformed, partial, unknown, or oversized object is refused
// with ErrAggregateTrust and no echo of the body.
func ParseAggregateTrust(raw []byte) (AggregateTrust, error) {
	trust, ok := parseAggregateTrust(raw)
	if !ok {
		return AggregateTrust{}, ErrAggregateTrust
	}
	return trust, nil
}

// ExactObjectKeys reports whether raw is one JSON object with each key once.
func ExactObjectKeys(raw []byte, keys []string) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return false
	}
	seen := map[string]struct{}{}
	for dec.More() {
		keyTok, keyErr := dec.Token()
		key, ok := keyTok.(string)
		if keyErr != nil || !ok {
			return false
		}
		if _, dup := seen[key]; dup {
			return false
		}
		seen[key] = struct{}{}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return false
		}
	}
	end, endErr := dec.Token()
	if endErr != nil || end != json.Delim('}') {
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return false
	}
	if len(seen) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := seen[key]; !ok {
			return false
		}
	}
	return true
}

func parseAggregateTrust(raw []byte) (AggregateTrust, bool) {
	if len(raw) == 0 || len(raw) > aggregateTrustMaxBytes || !utf8.Valid(raw) || bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		return AggregateTrust{}, false
	}
	if !ExactObjectKeys(raw, aggregateTrustKeys) {
		return AggregateTrust{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc struct {
		MaxAgeSeconds int64           `json:"maxAgeSeconds"`
		Contexts      json.RawMessage `json:"contexts"`
		Registry      json.RawMessage `json:"registry"`
	}
	if dec.Decode(&doc) != nil {
		return AggregateTrust{}, false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return AggregateTrust{}, false
	}
	if doc.MaxAgeSeconds <= 0 || doc.MaxAgeSeconds > 1800 {
		return AggregateTrust{}, false
	}
	contexts, ok := parseTrustContexts(doc.Contexts)
	if !ok {
		return AggregateTrust{}, false
	}
	reg, ok := parseTrustRegistry(doc.Registry)
	if !ok {
		return AggregateTrust{}, false
	}
	return AggregateTrust{Contexts: contexts, Registry: reg, MaxAge: doc.MaxAgeSeconds}, true
}

func parseTrustContexts(raw json.RawMessage) ([]TrustedContext, bool) {
	if !jsonArray(raw) {
		return nil, false
	}
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || len(rows) > 8 {
		return nil, false
	}
	out := make([]TrustedContext, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		if !ExactObjectKeys(row, []string{"provider", "producerRef", "producerIncarnation", "bindingGeneration", "accountRef", "workspaceRef", "bindingState", "sourceKind", "sourceVersion"}) {
			return nil, false
		}
		var doc struct {
			Provider            string  `json:"provider"`
			ProducerRef         string  `json:"producerRef"`
			ProducerIncarnation string  `json:"producerIncarnation"`
			BindingGeneration   int64   `json:"bindingGeneration"`
			AccountRef          *string `json:"accountRef"`
			WorkspaceRef        *string `json:"workspaceRef"`
			BindingState        string  `json:"bindingState"`
			SourceKind          string  `json:"sourceKind"`
			SourceVersion       string  `json:"sourceVersion"`
		}
		if strictDecode(row, &doc) != nil {
			return nil, false
		}
		if !knownProvider(doc.Provider) || !trustToken(doc.ProducerRef) || !trustToken(doc.ProducerIncarnation) || !trustToken(doc.SourceVersion) {
			return nil, false
		}
		if _, dup := seen[doc.Provider]; dup || doc.BindingGeneration < 0 {
			return nil, false
		}
		seen[doc.Provider] = struct{}{}
		state := BindingState(doc.BindingState)
		kind := SourceKind(doc.SourceKind)
		if !knownBinding(state) || !knownSourceKind(kind) {
			return nil, false
		}
		personalSource := accountScopeSource(doc.Provider, kind, doc.SourceVersion)
		if state == BindingVerified && (doc.AccountRef == nil || !trustToken(*doc.AccountRef) || doc.WorkspaceRef == nil && !personalSource) {
			return nil, false
		}
		if doc.AccountRef != nil && !trustToken(*doc.AccountRef) || doc.WorkspaceRef != nil && !trustToken(*doc.WorkspaceRef) {
			return nil, false
		}
		out = append(out, TrustedContext{
			Provider: doc.Provider, ProducerRef: doc.ProducerRef, ProducerIncarnation: doc.ProducerIncarnation,
			BindingGeneration: doc.BindingGeneration, AccountRef: cloneString(doc.AccountRef), WorkspaceRef: cloneString(doc.WorkspaceRef),
			BindingState: state, SourceKind: kind, SourceVersion: doc.SourceVersion,
		})
	}
	return out, true
}

func parseTrustRegistry(raw json.RawMessage) (*StaticRegistry, bool) {
	if !ExactObjectKeys(raw, []string{"sources", "metrics", "accounts", "entitlements", "projections"}) {
		return nil, false
	}
	var doc struct {
		Sources      json.RawMessage `json:"sources"`
		Metrics      json.RawMessage `json:"metrics"`
		Accounts     json.RawMessage `json:"accounts"`
		Entitlements json.RawMessage `json:"entitlements"`
		Projections  json.RawMessage `json:"projections"`
	}
	if strictDecode(raw, &doc) != nil {
		return nil, false
	}
	sources, ok := parseTrustSources(doc.Sources)
	if !ok {
		return nil, false
	}
	metrics, ok := parseTrustMetrics(doc.Metrics)
	if !ok {
		return nil, false
	}
	accounts, ok := parseTrustAccounts(doc.Accounts)
	if !ok {
		return nil, false
	}
	ents, ok := parseTrustEntitlements(doc.Entitlements)
	if !ok {
		return nil, false
	}
	projections, ok := parseTrustProjections(doc.Projections)
	if !ok {
		return nil, false
	}
	parts := RegistryParts{Sources: sources, Metrics: metrics, Accounts: accounts, Entitlements: ents, Projections: projections}
	if len(registryDuplicates(parts)) != 0 {
		return nil, false
	}
	return NewStaticRegistry(parts), true
}

func parseTrustSources(raw json.RawMessage) ([]SourceRule, bool) {
	rows, ok := rawRows(raw, 8)
	if !ok {
		return nil, false
	}
	out := make([]SourceRule, 0, len(rows))
	for _, row := range rows {
		if !ExactObjectKeys(row, []string{"provider", "kind", "version", "capabilityRef", "allowNormalizedMeasures", "allowDerivations", "directFields"}) {
			return nil, false
		}
		var doc struct {
			Provider                string          `json:"provider"`
			Kind                    string          `json:"kind"`
			Version                 string          `json:"version"`
			CapabilityRef           string          `json:"capabilityRef"`
			AllowNormalizedMeasures bool            `json:"allowNormalizedMeasures"`
			AllowDerivations        bool            `json:"allowDerivations"`
			DirectFields            json.RawMessage `json:"directFields"`
		}
		if strictDecode(row, &doc) != nil || !knownProvider(doc.Provider) || !knownSourceKind(SourceKind(doc.Kind)) || !trustToken(doc.Version) || !trustToken(doc.CapabilityRef) {
			return nil, false
		}
		fields, ok := parseDirectFields(doc.DirectFields)
		if !ok {
			return nil, false
		}
		out = append(out, SourceRule{
			Provider: doc.Provider, Kind: SourceKind(doc.Kind), Version: doc.Version, CapabilityRef: doc.CapabilityRef,
			AllowNormalizedMeasures: doc.AllowNormalizedMeasures, AllowDerivations: doc.AllowDerivations, DirectFields: fields,
		})
	}
	return out, true
}

func parseDirectFields(raw json.RawMessage) (map[string]DirectFieldRule, bool) {
	rows, ok := directFieldRows(raw)
	if !ok || len(rows) > 128 {
		return nil, false
	}
	if len(rows) == 0 {
		return nil, true
	}
	out := make(map[string]DirectFieldRule, len(rows))
	for name, row := range rows {
		if !trustToken(name) || !ExactObjectKeys(row, []string{"originalType", "class"}) {
			return nil, false
		}
		var doc struct {
			OriginalType string `json:"originalType"`
			Class        string `json:"class"`
		}
		if strictDecode(row, &doc) != nil || !knownOriginalType(OriginalType(doc.OriginalType)) || !knownDirectClass(DirectClass(doc.Class)) {
			return nil, false
		}
		out[name] = DirectFieldRule{OriginalType: OriginalType(doc.OriginalType), Class: DirectClass(doc.Class)}
	}
	return out, true
}

func directFieldRows(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		keyTok, keyErr := dec.Token()
		key, ok := keyTok.(string)
		if keyErr != nil || !ok {
			return nil, false
		}
		if _, dup := out[key]; dup {
			return nil, false
		}
		var row json.RawMessage
		if dec.Decode(&row) != nil {
			return nil, false
		}
		out[key] = append(json.RawMessage(nil), row...)
	}
	end, endErr := dec.Token()
	if endErr != nil || end != json.Delim('}') {
		return nil, false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, false
	}
	return out, true
}

func parseTrustMetrics(raw json.RawMessage) ([]MetricRule, bool) {
	rows, ok := rawRows(raw, 16)
	if !ok {
		return nil, false
	}
	keys := []string{"validationRef", "provider", "sourceKind", "sourceVersion", "category", "unit", "direction", "windows", "currency", "min", "max", "allowOverLimit", "allowUncapped", "complementOf", "historicalOnly", "modelScope"}
	out := make([]MetricRule, 0, len(rows))
	for _, row := range rows {
		if !ExactObjectKeys(row, keys) {
			return nil, false
		}
		var doc struct {
			ValidationRef  string   `json:"validationRef"`
			Provider       string   `json:"provider"`
			SourceKind     string   `json:"sourceKind"`
			SourceVersion  string   `json:"sourceVersion"`
			Category       string   `json:"category"`
			Unit           string   `json:"unit"`
			Direction      string   `json:"direction"`
			Windows        []string `json:"windows"`
			Currency       *string  `json:"currency"`
			Min            *string  `json:"min"`
			Max            *string  `json:"max"`
			AllowOverLimit bool     `json:"allowOverLimit"`
			AllowUncapped  bool     `json:"allowUncapped"`
			ComplementOf   string   `json:"complementOf"`
			HistoricalOnly bool     `json:"historicalOnly"`
			ModelScope     string   `json:"modelScope"`
		}
		if strictDecode(row, &doc) != nil || !trustToken(doc.ValidationRef) || !knownProvider(doc.Provider) || !knownSourceKind(SourceKind(doc.SourceKind)) || !trustToken(doc.SourceVersion) {
			return nil, false
		}
		if !knownCategory(Category(doc.Category)) || !knownUnit(doc.Unit) || !knownDirection(Direction(doc.Direction)) || len(doc.Windows) == 0 || len(doc.Windows) > 8 {
			return nil, false
		}
		for _, window := range doc.Windows {
			if !trustToken(window) {
				return nil, false
			}
		}
		if doc.Currency != nil && !trustToken(*doc.Currency) || doc.Min != nil && !trustDecimal(*doc.Min) || doc.Max != nil && !trustDecimal(*doc.Max) {
			return nil, false
		}
		if doc.ComplementOf != "" && !trustToken(doc.ComplementOf) || doc.ModelScope != "" && !trustToken(doc.ModelScope) {
			return nil, false
		}
		out = append(out, MetricRule{
			ValidationRef: doc.ValidationRef, Provider: doc.Provider, SourceKind: SourceKind(doc.SourceKind), SourceVersion: doc.SourceVersion,
			Category: Category(doc.Category), Unit: doc.Unit, Direction: Direction(doc.Direction), Windows: append([]string(nil), doc.Windows...),
			Currency: cloneString(doc.Currency), Min: cloneString(doc.Min), Max: cloneString(doc.Max),
			AllowOverLimit: doc.AllowOverLimit, AllowUncapped: doc.AllowUncapped, ComplementOf: doc.ComplementOf,
			HistoricalOnly: doc.HistoricalOnly, ModelScope: doc.ModelScope,
		})
	}
	return out, true
}

func parseTrustAccounts(raw json.RawMessage) ([]AccountRule, bool) {
	rows, ok := rawRows(raw, 8)
	if !ok {
		return nil, false
	}
	out := make([]AccountRule, 0, len(rows))
	for _, row := range rows {
		legacy := []string{"accountRef", "workspaceRef", "generation", "producerRef", "producerIncarnation"}
		extended := append(append([]string(nil), legacy...), "accountScopeOnly")
		if !ExactObjectKeys(row, legacy) && !ExactObjectKeys(row, extended) {
			return nil, false
		}
		var doc struct {
			AccountRef          string  `json:"accountRef"`
			WorkspaceRef        *string `json:"workspaceRef"`
			Generation          int64   `json:"generation"`
			ProducerRef         string  `json:"producerRef"`
			ProducerIncarnation string  `json:"producerIncarnation"`
			AccountScopeOnly    bool    `json:"accountScopeOnly"`
		}
		if strictDecode(row, &doc) != nil || doc.Generation < 0 || !trustToken(doc.AccountRef) || !trustToken(doc.ProducerRef) || !trustToken(doc.ProducerIncarnation) {
			return nil, false
		}
		workspace := ""
		if doc.WorkspaceRef == nil {
			if !doc.AccountScopeOnly {
				return nil, false
			}
		} else {
			if doc.AccountScopeOnly || !trustToken(*doc.WorkspaceRef) {
				return nil, false
			}
			workspace = *doc.WorkspaceRef
		}
		out = append(out, AccountRule{AccountRef: doc.AccountRef, WorkspaceRef: workspace, AccountScopeOnly: doc.AccountScopeOnly, Generation: doc.Generation, ProducerRef: doc.ProducerRef, ProducerIncarnation: doc.ProducerIncarnation})
	}
	return out, true
}

func parseTrustEntitlements(raw json.RawMessage) ([]EntitlementRule, bool) {
	rows, ok := rawRows(raw, 8)
	if !ok {
		return nil, false
	}
	out := make([]EntitlementRule, 0, len(rows))
	for _, row := range rows {
		if !ExactObjectKeys(row, []string{"entitlementRef", "products", "verified", "bindings"}) {
			return nil, false
		}
		var doc struct {
			EntitlementRef string          `json:"entitlementRef"`
			Products       []string        `json:"products"`
			Verified       bool            `json:"verified"`
			Bindings       json.RawMessage `json:"bindings"`
		}
		if strictDecode(row, &doc) != nil || !trustToken(doc.EntitlementRef) || len(doc.Products) == 0 || len(doc.Products) > 8 {
			return nil, false
		}
		for _, product := range doc.Products {
			if !trustToken(product) {
				return nil, false
			}
		}
		bindings, ok := rawRows(doc.Bindings, 8)
		if !ok {
			return nil, false
		}
		parsed := make([]EntitlementBinding, 0, len(bindings))
		for _, binding := range bindings {
			if !ExactObjectKeys(binding, []string{"accountRef", "workspaceRef", "generation", "producerRef", "producerIncarnation", "product"}) {
				return nil, false
			}
			var item struct {
				AccountRef          string `json:"accountRef"`
				WorkspaceRef        string `json:"workspaceRef"`
				Generation          int64  `json:"generation"`
				ProducerRef         string `json:"producerRef"`
				ProducerIncarnation string `json:"producerIncarnation"`
				Product             string `json:"product"`
			}
			if strictDecode(binding, &item) != nil || item.Generation < 0 || !trustToken(item.AccountRef) || !trustToken(item.WorkspaceRef) || !trustToken(item.ProducerRef) || !trustToken(item.ProducerIncarnation) || !trustToken(item.Product) {
				return nil, false
			}
			parsed = append(parsed, EntitlementBinding{AccountRef: item.AccountRef, WorkspaceRef: item.WorkspaceRef, Generation: item.Generation, ProducerRef: item.ProducerRef, ProducerIncarnation: item.ProducerIncarnation, Product: item.Product})
		}
		out = append(out, EntitlementRule{EntitlementRef: doc.EntitlementRef, Products: append([]string(nil), doc.Products...), Verified: doc.Verified, Bindings: parsed})
	}
	return out, true
}

func parseTrustProjections(raw json.RawMessage) ([]ProjectionRule, bool) {
	rows, ok := rawRows(raw, 8)
	if !ok {
		return nil, false
	}
	out := make([]ProjectionRule, 0, len(rows))
	for _, row := range rows {
		if !ExactObjectKeys(row, []string{"id", "provider", "sourceVersion", "measureIds"}) {
			return nil, false
		}
		var doc struct {
			ID            string   `json:"id"`
			Provider      string   `json:"provider"`
			SourceVersion string   `json:"sourceVersion"`
			MeasureIDs    []string `json:"measureIds"`
		}
		if strictDecode(row, &doc) != nil || !trustToken(doc.ID) || !knownProvider(doc.Provider) || !trustToken(doc.SourceVersion) || len(doc.MeasureIDs) == 0 || len(doc.MeasureIDs) > 8 {
			return nil, false
		}
		for _, id := range doc.MeasureIDs {
			if !trustToken(id) {
				return nil, false
			}
		}
		out = append(out, ProjectionRule{ID: doc.ID, Provider: doc.Provider, SourceVersion: doc.SourceVersion, MeasureIDs: append([]string(nil), doc.MeasureIDs...)})
	}
	return out, true
}

func rawRows(raw json.RawMessage, max int) ([]json.RawMessage, bool) {
	if !jsonArray(raw) {
		return nil, false
	}
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || len(rows) > max {
		return nil, false
	}
	return rows, true
}

func strictDecode(raw []byte, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func jsonArray(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	return err == nil && tok == json.Delim('[')
}

func knownProvider(provider string) bool {
	switch provider {
	case "cursor", "anthropic", "openai":
		return true
	default:
		return false
	}
}

func knownDirectClass(class DirectClass) bool {
	switch class {
	case ClassDisplay, ClassInventory, ClassSignal:
		return true
	default:
		return false
	}
}

func knownUnit(unit string) bool {
	return unit == UnitPercent || unit == UnitMoney
}

func trustToken(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7E {
			return false
		}
	}
	return true
}

func trustDecimal(value string) bool {
	return trustToken(value) && decimalPattern.MatchString(value)
}
