package providerallowance

import (
	"math/big"
	"strconv"
	"strings"
)

const (
	IssueRegistryMissing          = "registry_missing"
	IssueRegistryDuplicate        = "registry_duplicate"
	IssueSourceUntrusted          = "source_untrusted"
	IssueSourceMismatch           = "source_mismatch"
	IssueMetricUntrusted          = "metric_untrusted"
	IssueMetricMismatch           = "metric_mismatch"
	IssueRangeMissing             = "range_missing"
	IssueRangeUnsupported         = "range_unsupported"
	IssueBindingForged            = "binding_forged"
	IssueBindingMismatch          = "binding_mismatch"
	IssueGenerationMismatch       = "generation_mismatch"
	IssueProducerMismatch         = "producer_mismatch"
	IssueAccountMismatch          = "account_mismatch"
	IssueWorkspaceMismatch        = "workspace_mismatch"
	IssueFieldNotAllowlisted      = "field_not_allowlisted"
	IssueDisplayPromotion         = "display_promotion"
	IssueDuplicateID              = "duplicate_id"
	IssueKnownDecimal             = "known_decimal"
	IssueLexicalType              = "lexical_type"
	IssueCurrency                 = "currency"
	IssueUncapped                 = "uncapped_unsupported"
	IssueResetCreditMisuse        = "reset_credit_misuse"
	IssueFreshnessUnknown         = "freshness_unknown"
	IssueDerivationInputMissing   = "derivation_input_missing"
	IssueDerivationExternal       = "derivation_external"
	IssueDerivationIncompatible   = "derivation_incompatible"
	IssueDerivationPartial        = "derivation_partial"
	IssueDerivationUnknownInput   = "derivation_unknown_input"
	IssueDerivationFormula        = "derivation_formula"
	IssueDerivationNegative       = "derivation_negative_complement"
	IssueDerivationForged         = "derivation_forged_result"
	IssueDerivationDuplicateInput = "derivation_duplicate_input"
	IssueProjectionAlias          = "projection_alias"
	IssueNotActionable            = "not_actionable"
	IssueHistoricalNotCurrent     = "historical_not_current"
	IssueModelIncompatible        = "model_incompatible"
)

// Issue is one contract finding. Codes are stable for tests.
type Issue struct {
	Code    string
	Path    string
	Message string
}

func issue(code, path, message string) Issue {
	return Issue{Code: code, Path: path, Message: message}
}

// MeasureAssessment is the trust result for one measure. SemanticsEvidenced
// can be true for a historical field while Actionable stays false.
type MeasureAssessment struct {
	ID                 string
	SemanticsEvidenced bool
	HistoricalOnly     bool
	InRange            bool
	Actionable         bool
}

// ResetCreditAssessment classifies direct reset-credit inventory. The three
// authority fields stay false: this package does not turn the count into a
// rate-limit budget, an account block, or a capacity grant.
type ResetCreditAssessment struct {
	Present             bool
	CountLexical        string
	DetailsState        string
	DetailsAreEmptyList bool
	RateLimitBudget     bool
	BlocksAccountUse    bool
	GrantsCapacity      bool
}

// Assessment is the result of ValidateObservation. ServedModel stays
// ServedModelUnknown. Evidence references are not consulted.
type Assessment struct {
	ShapeOK            bool
	ProviderTimeKnown  bool
	TrustedBinding     BindingState
	ObservationBinding BindingState
	CompleteAllowance  bool
	ServedModel        string
	Issues             []Issue
	Measures           []MeasureAssessment
	ResetCredit        ResetCreditAssessment
}

// ValidateObservation checks shape and caller-supplied trust. It does not
// modify obs and does not read a clock or a provider.
func ValidateObservation(obs Observation, trusted TrustedContext, reg Registry) Assessment {
	var out Assessment
	out.ServedModel = ServedModelUnknown
	out.TrustedBinding = trusted.BindingState
	out.ObservationBinding = obs.Scope.BindingState
	out.ProviderTimeKnown = obs.ProviderObservedAt != nil
	shape := shapeIssues(obs)
	out.Issues = append(out.Issues, shape...)
	out.ShapeOK = len(shape) == 0
	out.Measures = make([]MeasureAssessment, len(obs.Measures))
	for i, m := range obs.Measures {
		out.Measures[i].ID = m.ID
	}
	if reg == nil {
		out.Issues = append(out.Issues, issue(IssueRegistryMissing, "registry", "a trusted registry is required"))
		return out
	}
	parts := snapshotRegistry(reg)
	out.Issues = append(out.Issues, registryDuplicates(parts)...)
	out.Issues = append(out.Issues, identityIssues(obs, trusted, parts)...)
	src, n := findSource(parts, obs)
	srcOK := n == 1
	if n == 0 {
		out.Issues = append(out.Issues, issue(IssueSourceUntrusted, "source", "source version is not in the trusted registry"))
	}
	if srcOK && src.CapabilityRef != "" && src.CapabilityRef != obs.Source.CapabilityRef {
		out.Issues = append(out.Issues, issue(IssueSourceMismatch, "source.capabilityRef", "capability reference does not match the trusted source"))
		srcOK = false
	}
	if srcOK {
		out.Issues = append(out.Issues, allowlistIssues(obs, src)...)
		out.ResetCredit = resetCredit(obs, src)
		out.Issues = append(out.Issues, resetMisuse(obs, src)...)
		if !src.AllowNormalizedMeasures && (len(obs.Measures) > 0 || len(obs.Derivations) > 0) {
			out.Issues = append(out.Issues, issue(IssueDisplayPromotion, "measures", "display-only source cannot carry normalized measures or derivations"))
		}
		if src.AllowNormalizedMeasures && !src.AllowDerivations && len(obs.Derivations) > 0 {
			out.Issues = append(out.Issues, issue(IssueDerivationFormula, "derivations", "this source cannot carry derivations"))
		}
	}
	blocked := !out.ShapeOK || hasIdentityBlock(out.Issues) || !srcOK
	assessMeasures(&out, obs, trusted, parts, src, srcOK, blocked)
	out.CompleteAllowance = completeAllowance(obs, out)
	return out
}

func shapeIssues(obs Observation) []Issue {
	var out []Issue
	seenDirect := map[string]struct{}{}
	for _, field := range obs.DirectFields {
		if _, dup := seenDirect[field.Field]; dup {
			out = append(out, issue(IssueDuplicateID, "directFields."+field.Field, "duplicate direct field"))
		}
		seenDirect[field.Field] = struct{}{}
		if !lexicalOK(field) {
			out = append(out, issue(IssueLexicalType, "directFields."+field.Field, "lexical value does not match originalType"))
		}
	}
	seenMeasure := map[string]struct{}{}
	for _, m := range obs.Measures {
		if _, dup := seenMeasure[m.ID]; dup {
			out = append(out, issue(IssueDuplicateID, "measures."+m.ID, "duplicate measure id"))
		}
		seenMeasure[m.ID] = struct{}{}
		out = append(out, valueShape(m.Value, "measures."+m.ID+".value")...)
		if (m.Currency != nil) != (m.Unit == UnitMoney) {
			out = append(out, issue(IssueCurrency, "measures."+m.ID+".currency", "currency is set only for unit money"))
		}
	}
	seenDeriv := map[string]struct{}{}
	for _, d := range obs.Derivations {
		if _, dup := seenDeriv[d.ID]; dup {
			out = append(out, issue(IssueDuplicateID, "derivations."+d.ID, "duplicate derivation id"))
		}
		seenDeriv[d.ID] = struct{}{}
		out = append(out, valueShape(d.Value, "derivations."+d.ID+".value")...)
	}
	return out
}

func valueShape(v MeasureValue, path string) []Issue {
	if v.State == ValueKnown && v.Decimal == nil {
		return []Issue{issue(IssueKnownDecimal, path, "a known value requires a decimal")}
	}
	if v.State != ValueKnown && v.Decimal != nil {
		return []Issue{issue(IssueKnownDecimal, path, "only a known value carries a decimal")}
	}
	return nil
}

func lexicalOK(field DirectField) bool {
	switch field.OriginalType {
	case TypeNull:
		return field.LexicalValue == nil
	case TypeBoolean:
		return field.LexicalValue != nil && (*field.LexicalValue == "true" || *field.LexicalValue == "false")
	case TypeDecimal:
		return field.LexicalValue != nil && decimalPattern.MatchString(*field.LexicalValue)
	case TypeString:
		return field.LexicalValue != nil
	default:
		return false
	}
}

func identityIssues(obs Observation, trusted TrustedContext, parts RegistryParts) []Issue {
	var out []Issue
	if trusted.ProducerRef != obs.ProducerRef || trusted.ProducerIncarnation != obs.ProducerIncarnation {
		out = append(out, issue(IssueProducerMismatch, "producerRef", "trusted producer does not match the observation"))
	}
	if trusted.Provider != obs.Provider || trusted.SourceKind != obs.Source.Kind || trusted.SourceVersion != obs.Source.Version {
		out = append(out, issue(IssueSourceMismatch, "source", "trusted source does not match the observation"))
	}
	if trusted.BindingGeneration != int64(obs.BindingGeneration) {
		out = append(out, issue(IssueGenerationMismatch, "bindingGeneration", "trusted generation does not match the observation"))
	}
	if !stringPtrEqual(trusted.AccountRef, obs.Scope.AccountRef) {
		out = append(out, issue(IssueAccountMismatch, "scope.accountRef", "trusted account does not match the observation"))
	}
	if !stringPtrEqual(trusted.WorkspaceRef, obs.Scope.WorkspaceRef) {
		out = append(out, issue(IssueWorkspaceMismatch, "scope.workspaceRef", "trusted workspace does not match the observation"))
	}
	switch {
	case obs.Scope.BindingState == BindingVerified && trusted.BindingState != BindingVerified:
		out = append(out, issue(IssueBindingForged, "scope.bindingState", "observation bindingState verified is not trusted"))
	case trusted.BindingState != obs.Scope.BindingState:
		out = append(out, issue(IssueBindingMismatch, "scope.bindingState", "trusted binding state does not match the observation"))
	case trusted.BindingState == BindingVerified && !accountListed(parts, obs):
		out = append(out, issue(IssueAccountMismatch, "scope.accountRef", "no trusted account record matches this binding"))
	}
	return out
}

func hasIdentityBlock(issues []Issue) bool {
	for _, item := range issues {
		switch item.Code {
		case IssueProducerMismatch, IssueSourceMismatch, IssueGenerationMismatch, IssueAccountMismatch, IssueWorkspaceMismatch, IssueBindingForged, IssueBindingMismatch, IssueRegistryDuplicate:
			return true
		}
	}
	return false
}

func assessMeasures(out *Assessment, obs Observation, trusted TrustedContext, parts RegistryParts, src SourceRule, srcOK, blocked bool) {
	ready := srcOK && bindingReady(obs, trusted, parts)
	for i, m := range obs.Measures {
		ma := out.Measures[i]
		if !srcOK || !src.AllowNormalizedMeasures {
			out.Measures[i] = ma
			continue
		}
		if m.Value.State != ValueKnown && m.Value.State != ValueUncapped {
			if m.ValidationRef != nil && *m.ValidationRef != "" {
				if _, issues := oneMetric(parts, obs, m); len(issues) > 0 {
					out.Issues = append(out.Issues, issues...)
				}
			}
			out.Measures[i] = ma
			continue
		}
		if m.Value.State == ValueKnown && m.Value.Decimal == nil {
			out.Measures[i] = ma
			continue
		}
		rule, issues := oneMetric(parts, obs, m)
		if len(issues) > 0 {
			out.Issues = append(out.Issues, issues...)
			out.Measures[i] = ma
			continue
		}
		ma.HistoricalOnly = rule.HistoricalOnly
		if m.Value.State == ValueUncapped {
			if !rule.AllowUncapped {
				out.Issues = append(out.Issues, issue(IssueUncapped, "measures."+m.ID, "uncapped is not allowed for this metric"))
				out.Measures[i] = ma
				continue
			}
			ma.SemanticsEvidenced = true
			ma.InRange = true
			ma.Actionable = !rule.HistoricalOnly && ready && !blocked
			out.Measures[i] = ma
			continue
		}
		ok, code := rangeCheck(rule, *m.Value.Decimal)
		ma.InRange = ok
		if !ok {
			out.Issues = append(out.Issues, issue(code, "measures."+m.ID+".value.decimal", "value is outside the trusted metric range"))
			out.Measures[i] = ma
			continue
		}
		ma.SemanticsEvidenced = true
		ma.Actionable = !rule.HistoricalOnly && ready && !blocked
		out.Measures[i] = ma
	}
}

func completeAllowance(obs Observation, a Assessment) bool {
	if !a.ShapeOK || len(a.Issues) > 0 || !a.ProviderTimeKnown {
		return false
	}
	if a.TrustedBinding != BindingVerified || a.ObservationBinding != BindingVerified {
		return false
	}
	if obs.Completeness != CompletenessComplete || len(obs.Derivations) > 0 || len(a.Measures) == 0 {
		return false
	}
	for _, m := range a.Measures {
		if !m.Actionable {
			return false
		}
	}
	return true
}

func bindingReady(obs Observation, trusted TrustedContext, parts RegistryParts) bool {
	if trusted.BindingState != BindingVerified || obs.Scope.BindingState != BindingVerified {
		return false
	}
	if trusted.ProducerRef != obs.ProducerRef || trusted.ProducerIncarnation != obs.ProducerIncarnation {
		return false
	}
	if trusted.Provider != obs.Provider || trusted.SourceKind != obs.Source.Kind || trusted.SourceVersion != obs.Source.Version {
		return false
	}
	if trusted.BindingGeneration != int64(obs.BindingGeneration) {
		return false
	}
	if !stringPtrEqual(trusted.AccountRef, obs.Scope.AccountRef) || !stringPtrEqual(trusted.WorkspaceRef, obs.Scope.WorkspaceRef) {
		return false
	}
	if obs.ProviderObservedAt == nil || obs.Scope.AccountRef == nil || obs.Scope.WorkspaceRef == nil {
		return false
	}
	return accountListed(parts, obs)
}

func accountListed(parts RegistryParts, obs Observation) bool {
	if obs.Scope.AccountRef == nil || obs.Scope.WorkspaceRef == nil {
		return false
	}
	n := 0
	for _, account := range parts.Accounts {
		if account.AccountRef == *obs.Scope.AccountRef && account.WorkspaceRef == *obs.Scope.WorkspaceRef && account.Generation == int64(obs.BindingGeneration) && account.ProducerRef == obs.ProducerRef && account.ProducerIncarnation == obs.ProducerIncarnation {
			n++
		}
	}
	return n == 1
}

func findSource(parts RegistryParts, obs Observation) (SourceRule, int) {
	var found SourceRule
	n := 0
	for _, src := range parts.Sources {
		if src.Provider == obs.Provider && src.Kind == obs.Source.Kind && src.Version == obs.Source.Version {
			found = src
			n++
		}
	}
	if n == 1 {
		return found, 1
	}
	return SourceRule{}, n
}

func oneMetric(parts RegistryParts, obs Observation, m Measure) (MetricRule, []Issue) {
	if m.ValidationRef == nil || *m.ValidationRef == "" {
		return MetricRule{}, []Issue{issue(IssueMetricUntrusted, "measures."+m.ID+".validationRef", "known value has no trusted validation reference")}
	}
	ref := *m.ValidationRef
	var matched []MetricRule
	saw := false
	for _, rule := range parts.Metrics {
		if rule.ValidationRef != ref {
			continue
		}
		saw = true
		if metricMatches(rule, obs, m) {
			matched = append(matched, rule)
		}
	}
	path := "measures." + m.ID + ".validationRef"
	if !saw {
		return MetricRule{}, []Issue{issue(IssueMetricUntrusted, path, "validation reference is not in the trusted registry")}
	}
	if len(matched) == 0 {
		return MetricRule{}, []Issue{issue(IssueMetricMismatch, "measures."+m.ID, "validation reference does not match this measure")}
	}
	if len(matched) > 1 {
		return MetricRule{}, []Issue{issue(IssueRegistryDuplicate, path, "more than one trusted rule matches this measure")}
	}
	return matched[0], nil
}

func metricMatches(rule MetricRule, obs Observation, m Measure) bool {
	if rule.Provider != obs.Provider || rule.SourceKind != obs.Source.Kind || rule.SourceVersion != obs.Source.Version {
		return false
	}
	if rule.Category != m.Category || rule.Unit != m.Unit || rule.Direction != m.Direction {
		return false
	}
	if !stringPtrEqual(rule.Currency, m.Currency) {
		return false
	}
	return containsStr(rule.Windows, m.Window.Ref)
}

func rangeCheck(rule MetricRule, lexical string) (bool, string) {
	rat, err := parseDecimal(lexical)
	if err != nil {
		return false, IssueKnownDecimal
	}
	if rule.Min == nil && !rule.AllowOverLimit {
		return false, IssueRangeMissing
	}
	if rule.Max == nil && !rule.AllowOverLimit {
		return false, IssueRangeMissing
	}
	if rule.Min != nil {
		min, err := parseDecimal(*rule.Min)
		if err != nil || rat.Cmp(min) < 0 {
			return false, IssueRangeUnsupported
		}
	}
	if rule.Max != nil && !rule.AllowOverLimit {
		max, err := parseDecimal(*rule.Max)
		if err != nil || rat.Cmp(max) > 0 {
			return false, IssueRangeUnsupported
		}
	}
	return true, ""
}

func allowlistIssues(obs Observation, src SourceRule) []Issue {
	var out []Issue
	seen := map[string]struct{}{}
	for _, field := range obs.DirectFields {
		if _, dup := seen[field.Field]; dup {
			continue
		}
		seen[field.Field] = struct{}{}
		rule, ok := src.DirectFields[field.Field]
		if !ok {
			out = append(out, issue(IssueFieldNotAllowlisted, "directFields."+field.Field, "direct field is not in the trusted allowlist"))
			continue
		}
		if rule.OriginalType != field.OriginalType {
			out = append(out, issue(IssueFieldNotAllowlisted, "directFields."+field.Field, "direct field type does not match the trusted allowlist"))
		}
	}
	return out
}

func resetCredit(obs Observation, src SourceRule) ResetCreditAssessment {
	var out ResetCreditAssessment
	for _, field := range obs.DirectFields {
		rule, ok := src.DirectFields[field.Field]
		if !ok || rule.Class != ClassInventory || field.LexicalValue == nil {
			continue
		}
		switch field.Field {
		case "resetCredits.availableCount":
			out.Present = true
			out.CountLexical = *field.LexicalValue
		case "resetCredits.detailsState":
			out.DetailsState = *field.LexicalValue
		}
	}
	return out
}

func resetMisuse(obs Observation, src SourceRule) []Issue {
	names := map[string]struct{}{}
	for field, rule := range src.DirectFields {
		if rule.Class == ClassInventory {
			names[field] = struct{}{}
		}
	}
	var out []Issue
	for _, m := range obs.Measures {
		if m.Category != CategoryRateLimit && m.Category != CategoryLocal {
			continue
		}
		for _, ev := range m.EvidenceFields {
			if _, ok := names[ev]; ok {
				out = append(out, issue(IssueResetCreditMisuse, "measures."+m.ID, "reset-credit inventory is not a rate-limit budget or capacity grant"))
			}
		}
	}
	return out
}

func registryDuplicates(parts RegistryParts) []Issue {
	var out []Issue
	seenSrc := map[string]struct{}{}
	for _, src := range parts.Sources {
		key := src.Provider + "\x00" + string(src.Kind) + "\x00" + src.Version
		if _, ok := seenSrc[key]; ok {
			out = append(out, issue(IssueRegistryDuplicate, "registry.source", "duplicate source version"))
		}
		seenSrc[key] = struct{}{}
	}
	seenMetric := map[string]struct{}{}
	for _, metric := range parts.Metrics {
		for _, window := range metric.Windows {
			key := strings.Join([]string{metric.ValidationRef, metric.Provider, string(metric.SourceKind), metric.SourceVersion, string(metric.Category), metric.Unit, string(metric.Direction), window}, "\x00")
			if _, ok := seenMetric[key]; ok {
				out = append(out, issue(IssueRegistryDuplicate, "registry.metric", "duplicate metric rule"))
			}
			seenMetric[key] = struct{}{}
		}
	}
	seenAccount := map[string]struct{}{}
	for _, account := range parts.Accounts {
		key := account.AccountRef + "\x00" + account.WorkspaceRef + "\x00" + account.ProducerRef + "\x00" + account.ProducerIncarnation + "\x00" + strconv.FormatInt(account.Generation, 10)
		if _, ok := seenAccount[key]; ok {
			out = append(out, issue(IssueRegistryDuplicate, "registry.account", "duplicate account generation"))
		}
		seenAccount[key] = struct{}{}
	}
	seenEnt := map[string]struct{}{}
	for _, ent := range parts.Entitlements {
		if _, ok := seenEnt[ent.EntitlementRef]; ok {
			out = append(out, issue(IssueRegistryDuplicate, "registry.entitlement", "duplicate entitlement"))
		}
		seenEnt[ent.EntitlementRef] = struct{}{}
	}
	seenProj := map[string]struct{}{}
	for _, proj := range parts.Projections {
		if _, ok := seenProj[proj.ID]; ok {
			out = append(out, issue(IssueRegistryDuplicate, "registry.projection", "duplicate projection"))
		}
		seenProj[proj.ID] = struct{}{}
	}
	return out
}

func parseDecimal(s string) (*big.Rat, error) {
	if !decimalPattern.MatchString(s) {
		return nil, issueError("invalid decimal")
	}
	rat, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, issueError("invalid decimal")
	}
	return rat, nil
}

type issueError string

func (e issueError) Error() string { return string(e) }

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func containsStr(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
