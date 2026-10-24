package providerallowance

import (
	"math/big"
	"sort"
	"strings"
)

// DerivedValue is a confirmed formula result. Lexical is the declared spelling.
type DerivedValue struct {
	ID      string
	Formula Formula
	Unit    string
	Lexical string
}

// DerivationResult confirms declared derivations. Confirmed is false when
// Values is empty. An empty derivation list is not a zero allowance.
type DerivationResult struct {
	Confirmed bool
	Values    []DerivedValue
	Issues    []Issue
}

// PoolAssessment counts verified shared pools. An unresolved relationship
// leaves EntitlementRef empty and does not invent an identifier.
type PoolAssessment struct {
	Resolved       bool
	Count          int
	EntitlementRef string
	Issues         []Issue
}

// ProjectionAssessment counts alias projections. Aliased measures count once.
type ProjectionAssessment struct {
	Count        int
	Aliased      bool
	ProjectionID string
	Issues       []Issue
}

// Derive confirms derivations already present on obs. It does not modify obs,
// clamp a percent, or fill in a missing result.
func Derive(obs Observation, trusted TrustedContext, reg Registry) DerivationResult {
	assessment := ValidateObservation(obs, trusted, reg)
	out := DerivationResult{Issues: append([]Issue(nil), assessment.Issues...)}
	if !assessment.ShapeOK || reg == nil || len(obs.Derivations) == 0 {
		return out
	}
	parts := snapshotRegistry(reg)
	values := make([]DerivedValue, 0, len(obs.Derivations))
	clean := len(out.Issues) == 0
	for _, deriv := range obs.Derivations {
		value, issues := confirmDerivation(obs, trusted, parts, deriv)
		if len(issues) > 0 {
			out.Issues = append(out.Issues, issues...)
			clean = false
			continue
		}
		values = append(values, value)
	}
	if clean && len(values) == len(obs.Derivations) {
		out.Confirmed = true
		out.Values = values
	}
	return out
}

func confirmDerivation(obs Observation, trusted TrustedContext, parts RegistryParts, deriv Derivation) (DerivedValue, []Issue) {
	path := "derivations." + deriv.ID
	if deriv.Precision != "exact" {
		return DerivedValue{}, []Issue{issue(IssueDerivationFormula, path+".precision", "only exact precision can be confirmed")}
	}
	if deriv.Formula != FormulaComplement && deriv.Formula != FormulaSum {
		return DerivedValue{}, []Issue{issue(IssueDerivationFormula, path+".formula", "formula is not supported")}
	}
	if len(deriv.InputRefs) == 0 {
		return DerivedValue{}, []Issue{issue(IssueDerivationInputMissing, path+".inputRefs", "derivation has no inputs")}
	}
	seen := map[string]struct{}{}
	inputs := make([]Measure, 0, len(deriv.InputRefs))
	for _, ref := range deriv.InputRefs {
		if _, dup := seen[ref]; dup {
			return DerivedValue{}, []Issue{issue(IssueDerivationDuplicateInput, path+".inputRefs", "derivation input is repeated")}
		}
		seen[ref] = struct{}{}
		measure, ok := measureByID(obs, ref)
		if !ok {
			return DerivedValue{}, []Issue{issue(IssueDerivationExternal, path+".inputRefs", "derivation input is not in this observation")}
		}
		inputs = append(inputs, measure)
	}
	if deriv.Formula == FormulaComplement && len(inputs) != 1 {
		return DerivedValue{}, []Issue{issue(IssueDerivationFormula, path, "complement_percent uses one input")}
	}
	if deriv.Formula == FormulaSum && len(inputs) < 2 {
		return DerivedValue{}, []Issue{issue(IssueDerivationFormula, path, "sum_compatible_values uses two or more inputs")}
	}
	for _, measure := range inputs {
		if measure.Value.State != ValueKnown || measure.Value.Decimal == nil {
			return DerivedValue{}, []Issue{issue(IssueDerivationUnknownInput, path, "derivation input is not a known decimal")}
		}
	}
	if !compatibleInputs(inputs) || deriv.Unit != inputs[0].Unit {
		return DerivedValue{}, []Issue{issue(IssueDerivationIncompatible, path, "derivation inputs differ in window, unit, or currency")}
	}
	if deriv.Formula == FormulaSum && aliasSet(obs, deriv.InputRefs, parts) {
		return DerivedValue{}, []Issue{issue(IssueProjectionAlias, path, "alias projections are one allowance and cannot be summed")}
	}
	rules := make([]MetricRule, len(inputs))
	for i, measure := range inputs {
		rule, issues := oneMetric(parts, obs, measure)
		if len(issues) > 0 {
			return DerivedValue{}, []Issue{issue(IssueDerivationUnknownInput, "measures."+measure.ID, "derivation input is not a trusted measure")}
		}
		ok, code := rangeCheck(rule, *measure.Value.Decimal)
		if !ok {
			return DerivedValue{}, []Issue{issue(code, "measures."+measure.ID+".value.decimal", "derivation input is outside the trusted range")}
		}
		if rule.HistoricalOnly {
			return DerivedValue{}, []Issue{issue(IssueHistoricalNotCurrent, "measures."+measure.ID, "historical field semantics are not a current derivation")}
		}
		rules[i] = rule
	}
	if !sameModel(rules) {
		return DerivedValue{}, []Issue{issue(IssueModelIncompatible, path, "derivation inputs do not share a model scope")}
	}
	if obs.Completeness != CompletenessComplete {
		return DerivedValue{}, []Issue{issue(IssueDerivationPartial, "completeness", "a partial observation cannot confirm a complete derivation")}
	}
	if obs.ProviderObservedAt == nil {
		return DerivedValue{}, []Issue{issue(IssueFreshnessUnknown, "providerObservedAt", "provider time is required before a derivation is advice")}
	}
	if !bindingReady(obs, trusted, parts) {
		return DerivedValue{}, []Issue{issue(IssueNotActionable, path, "trusted binding does not allow this derivation")}
	}
	if deriv.Value.State != ValueKnown || deriv.Value.Decimal == nil {
		return DerivedValue{}, []Issue{issue(IssueDerivationForged, path+".value", "a confirmed derivation needs a known declared decimal")}
	}
	got, issues := expectedValue(deriv.Formula, inputs, rules)
	if len(issues) > 0 {
		return DerivedValue{}, issues
	}
	if got.Sign() < 0 {
		return DerivedValue{}, []Issue{issue(IssueDerivationNegative, path+".value.decimal", "complement would be negative and is not emitted")}
	}
	declared, err := parseDecimal(*deriv.Value.Decimal)
	if err != nil || declared.Cmp(got) != 0 {
		return DerivedValue{}, []Issue{issue(IssueDerivationForged, path+".value.decimal", "declared result does not match the formula")}
	}
	return DerivedValue{ID: deriv.ID, Formula: deriv.Formula, Unit: deriv.Unit, Lexical: *deriv.Value.Decimal}, nil
}

func expectedValue(formula Formula, inputs []Measure, rules []MetricRule) (*big.Rat, []Issue) {
	if formula == FormulaComplement {
		if rules[0].ComplementOf == "" || inputs[0].Direction != DirectionUsed || inputs[0].Unit != UnitPercent || inputs[0].Category != CategoryIncluded {
			return nil, []Issue{issue(IssueDerivationFormula, "formula", "this metric does not support complement_percent")}
		}
		bound, err := parseDecimal(rules[0].ComplementOf)
		used, err2 := parseDecimal(*inputs[0].Value.Decimal)
		if err != nil || err2 != nil {
			return nil, []Issue{issue(IssueDerivationFormula, "formula", "complement bound is not a decimal")}
		}
		return new(big.Rat).Sub(bound, used), nil
	}
	sum := new(big.Rat)
	for _, measure := range inputs {
		part, err := parseDecimal(*measure.Value.Decimal)
		if err != nil {
			return nil, []Issue{issue(IssueKnownDecimal, "measures."+measure.ID, "input decimal is invalid")}
		}
		sum.Add(sum, part)
	}
	return sum, nil
}

func compatibleInputs(inputs []Measure) bool {
	first := inputs[0]
	for _, measure := range inputs[1:] {
		if measure.Unit != first.Unit || measure.Category != first.Category || measure.Direction != first.Direction {
			return false
		}
		if !stringPtrEqual(measure.Currency, first.Currency) {
			return false
		}
		if measure.Window.Ref != first.Window.Ref || measure.Window.ResetPrecision != first.Window.ResetPrecision {
			return false
		}
		if !countPtrEqual(measure.Window.DurationMinutes, first.Window.DurationMinutes) {
			return false
		}
		if !stringPtrEqual(measure.Window.StartAt, first.Window.StartAt) || !stringPtrEqual(measure.Window.ResetAt, first.Window.ResetAt) {
			return false
		}
	}
	return true
}

func countPtrEqual(a, b *Count) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameModel(rules []MetricRule) bool {
	for _, rule := range rules[1:] {
		if rule.ModelScope != rules[0].ModelScope {
			return false
		}
	}
	return true
}

func measureByID(obs Observation, id string) (Measure, bool) {
	for _, measure := range obs.Measures {
		if measure.ID == id {
			return measure, true
		}
	}
	return Measure{}, false
}

func aliasSet(obs Observation, ids []string, parts RegistryParts) bool {
	parent := map[string]string{}
	var find func(string) string
	find = func(id string) string {
		if _, ok := parent[id]; !ok {
			parent[id] = id
			return id
		}
		if parent[id] != id {
			parent[id] = find(parent[id])
		}
		return parent[id]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	known := map[string]struct{}{}
	for _, rule := range parts.Projections {
		if rule.Provider != obs.Provider || rule.SourceVersion != obs.Source.Version {
			continue
		}
		members := uniqueStrings(rule.MeasureIDs)
		if len(members) < 2 {
			continue
		}
		for _, id := range members {
			known[id] = struct{}{}
		}
		for _, id := range members[1:] {
			union(members[0], id)
		}
	}
	counts := map[string]int{}
	seen := map[string]struct{}{}
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if _, ok := known[id]; !ok {
			continue
		}
		counts[find(id)]++
	}
	for _, n := range counts {
		if n >= 2 {
			return true
		}
	}
	return false
}

// SharedPools counts verified alias groups. It does not add measure values.
func SharedPools(observations []Observation, trusted []TrustedContext, reg Registry) PoolAssessment {
	var out PoolAssessment
	if reg == nil {
		out.Issues = []Issue{issue(IssueRegistryMissing, "registry", "a trusted registry is required")}
		return out
	}
	if len(observations) != len(trusted) {
		out.Issues = []Issue{issue(IssueAccountMismatch, "scope", "trusted contexts do not align with observations")}
		return out
	}
	parts := snapshotRegistry(reg)
	if dups := registryDuplicates(parts); len(dups) > 0 {
		out.Issues = dups
		return out
	}
	type group struct {
		ref        string
		account    string
		workspace  string
		generation int64
		products   map[string]struct{}
		conflict   bool
		started    bool
	}
	var groups []group
	for _, rule := range parts.Entitlements {
		if !rule.Verified || rule.EntitlementRef == "" || len(rule.Products) < 2 {
			continue
		}
		var current group
		current.ref = rule.EntitlementRef
		current.products = map[string]struct{}{}
		for i, obs := range observations {
			if obs.Scope.EntitlementRef == nil || *obs.Scope.EntitlementRef != rule.EntitlementRef {
				continue
			}
			if !bindingReady(obs, trusted[i], parts) {
				continue
			}
			product, ok := boundEntitlementProduct(obs, rule)
			if !ok {
				out.Issues = append(out.Issues, issue(IssueAccountMismatch, "scope.entitlementRef", "payload entitlement is not a trusted account binding"))
				continue
			}
			account := *obs.Scope.AccountRef
			workspace := *obs.Scope.WorkspaceRef
			if !current.started {
				current.account = account
				current.workspace = workspace
				current.generation = int64(obs.BindingGeneration)
				current.started = true
			} else if current.account != account || current.workspace != workspace || current.generation != int64(obs.BindingGeneration) {
				current.conflict = true
			}
			current.products[product] = struct{}{}
		}
		if current.conflict {
			out.Issues = append(out.Issues, issue(IssueAccountMismatch, "scope.accountRef", "shared-pool aliases cross accounts"))
			continue
		}
		if len(current.products) >= 2 {
			groups = append(groups, current)
		}
	}
	if len(out.Issues) > 0 {
		return out
	}
	out.Count = len(groups)
	if len(groups) == 1 {
		out.Resolved = true
		out.EntitlementRef = groups[0].ref
	}
	return out
}

// Projections reports whether measure ids are aliases of one projection.
func Projections(obs Observation, reg Registry) ProjectionAssessment {
	var out ProjectionAssessment
	if reg == nil {
		out.Count = len(obs.Measures)
		out.Issues = []Issue{issue(IssueRegistryMissing, "registry", "a trusted registry is required")}
		return out
	}
	parts := snapshotRegistry(reg)
	if issues := append(registryDuplicates(parts), projectionAmbiguities(parts.Projections)...); len(issues) > 0 {
		out.Issues = issues
		return out
	}
	covered := map[string]struct{}{}
	groups := 0
	var id string
	for _, rule := range parts.Projections {
		if rule.Provider != obs.Provider || rule.SourceVersion != obs.Source.Version || len(rule.MeasureIDs) < 2 {
			continue
		}
		if !coversMeasures(obs, rule.MeasureIDs) {
			continue
		}
		groups++
		id = rule.ID
		for _, measureID := range rule.MeasureIDs {
			covered[measureID] = struct{}{}
		}
	}
	extra := 0
	for _, measure := range obs.Measures {
		if _, ok := covered[measure.ID]; !ok {
			extra++
		}
	}
	if groups == 0 {
		out.Count = len(obs.Measures)
		return out
	}
	out.Aliased = true
	out.Count = groups + extra
	if groups == 1 && extra == 0 {
		out.ProjectionID = id
	}
	return out
}

func boundEntitlementProduct(obs Observation, rule EntitlementRule) (string, bool) {
	if obs.Scope.AccountRef == nil || obs.Scope.WorkspaceRef == nil || len(obs.Scope.Products) != 1 {
		return "", false
	}
	product := obs.Scope.Products[0]
	if !containsStr(rule.Products, product) {
		return "", false
	}
	for _, binding := range rule.Bindings {
		if binding.AccountRef == *obs.Scope.AccountRef &&
			binding.WorkspaceRef == *obs.Scope.WorkspaceRef &&
			binding.Generation == int64(obs.BindingGeneration) &&
			binding.ProducerRef == obs.ProducerRef &&
			binding.ProducerIncarnation == obs.ProducerIncarnation &&
			binding.Product == product {
			return product, true
		}
	}
	return "", false
}

func projectionAmbiguities(rules []ProjectionRule) []Issue {
	var out []Issue
	seenSet := map[string]struct{}{}
	owner := map[string]string{}
	for _, rule := range rules {
		members := uniqueStrings(rule.MeasureIDs)
		if len(members) != len(rule.MeasureIDs) {
			out = append(out, issue(IssueRegistryDuplicate, "registry.projection", "projection repeats a measure"))
			continue
		}
		if len(members) < 2 {
			continue
		}
		sorted := append([]string(nil), members...)
		sort.Strings(sorted)
		setKey := rule.Provider + "\x00" + rule.SourceVersion + "\x00" + strings.Join(sorted, "\x00")
		if _, ok := seenSet[setKey]; ok {
			out = append(out, issue(IssueRegistryDuplicate, "registry.projection", "projection member set is repeated"))
			continue
		}
		seenSet[setKey] = struct{}{}
		for _, member := range members {
			memKey := rule.Provider + "\x00" + rule.SourceVersion + "\x00" + member
			if prev, ok := owner[memKey]; ok && prev != rule.ID {
				out = append(out, issue(IssueRegistryDuplicate, "registry.projection", "projection groups overlap"))
			}
			owner[memKey] = rule.ID
		}
	}
	return out
}

func uniqueStrings(ids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func coversMeasures(obs Observation, ids []string) bool {
	have := map[string]struct{}{}
	for _, measure := range obs.Measures {
		have[measure.ID] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := have[id]; !ok {
			return false
		}
	}
	return true
}
