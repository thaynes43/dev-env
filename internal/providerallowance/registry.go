package providerallowance

// Registry is the caller-supplied trust input for one validation. A
// StaticRegistry value is an in-memory test double. This package does not
// load, watch, or authorize registry data.
type Registry interface {
	Sources() []SourceRule
	Metrics() []MetricRule
	Accounts() []AccountRule
	Entitlements() []EntitlementRule
	Projections() []ProjectionRule
}

// SourceRule is one allowlisted source version.
type SourceRule struct {
	Provider                string
	Kind                    SourceKind
	Version                 string
	CapabilityRef           string
	DirectFields            map[string]DirectFieldRule
	AllowNormalizedMeasures bool
	AllowDerivations        bool
}

// DirectFieldRule names the original type and the evidence class of one field.
type DirectFieldRule struct {
	OriginalType OriginalType
	Class        DirectClass
}

// MetricRule is the source semantic for one validation reference. Windows
// lists the window refs it may describe. An empty list matches nothing.
// Min and Max are decimal strings. AllowOverLimit keeps a value above Max
// when Max is set, and permits a missing Max. HistoricalOnly evidences a
// field without making it a current entitlement. ComplementOf is the decimal
// bound for complement_percent. An empty ComplementOf does not enable that
// formula. ModelScope empty means this package has no served-model claim.
type MetricRule struct {
	ValidationRef  string
	Provider       string
	SourceKind     SourceKind
	SourceVersion  string
	Category       Category
	Unit           string
	Direction      Direction
	Windows        []string
	Currency       *string
	Min            *string
	Max            *string
	AllowOverLimit bool
	AllowUncapped  bool
	ComplementOf   string
	HistoricalOnly bool
	ModelScope     string
}

// AccountRule is one trusted account generation. The observation does not
// create this record.
type AccountRule struct {
	AccountRef          string
	WorkspaceRef        string
	Generation          int64
	ProducerRef         string
	ProducerIncarnation string
}

// EntitlementRule is a verified shared pool. Verified false does not invent
// a pool identity. Products are aliases of that one pool.
type EntitlementRule struct {
	EntitlementRef string
	Products       []string
	Verified       bool
}

// ProjectionRule groups measure ids that are aliases of one projection.
// Those ids are not added together.
type ProjectionRule struct {
	ID            string
	Provider      string
	SourceVersion string
	MeasureIDs    []string
}

// RegistryParts is the value a Registry returns.
type RegistryParts struct {
	Sources      []SourceRule
	Metrics      []MetricRule
	Accounts     []AccountRule
	Entitlements []EntitlementRule
	Projections  []ProjectionRule
}

// StaticRegistry stores caller-built rules.
type StaticRegistry struct {
	parts RegistryParts
}

// NewStaticRegistry copies the supplied rules.
func NewStaticRegistry(parts RegistryParts) *StaticRegistry {
	return &StaticRegistry{parts: cloneParts(parts)}
}

func (r *StaticRegistry) Sources() []SourceRule {
	if r == nil {
		return nil
	}
	return cloneParts(r.parts).Sources
}

func (r *StaticRegistry) Metrics() []MetricRule {
	if r == nil {
		return nil
	}
	return cloneParts(r.parts).Metrics
}

func (r *StaticRegistry) Accounts() []AccountRule {
	if r == nil {
		return nil
	}
	return append([]AccountRule(nil), r.parts.Accounts...)
}

func (r *StaticRegistry) Entitlements() []EntitlementRule {
	if r == nil {
		return nil
	}
	return cloneParts(r.parts).Entitlements
}

func (r *StaticRegistry) Projections() []ProjectionRule {
	if r == nil {
		return nil
	}
	return cloneParts(r.parts).Projections
}

func snapshotRegistry(reg Registry) RegistryParts {
	if reg == nil {
		return RegistryParts{}
	}
	return cloneParts(RegistryParts{
		Sources:      reg.Sources(),
		Metrics:      reg.Metrics(),
		Accounts:     reg.Accounts(),
		Entitlements: reg.Entitlements(),
		Projections:  reg.Projections(),
	})
}

func cloneParts(in RegistryParts) RegistryParts {
	out := RegistryParts{
		Sources:      make([]SourceRule, len(in.Sources)),
		Metrics:      make([]MetricRule, len(in.Metrics)),
		Accounts:     append([]AccountRule(nil), in.Accounts...),
		Entitlements: make([]EntitlementRule, len(in.Entitlements)),
		Projections:  make([]ProjectionRule, len(in.Projections)),
	}
	for i, src := range in.Sources {
		out.Sources[i] = src
		if src.DirectFields != nil {
			out.Sources[i].DirectFields = make(map[string]DirectFieldRule, len(src.DirectFields))
			for k, v := range src.DirectFields {
				out.Sources[i].DirectFields[k] = v
			}
		}
	}
	for i, metric := range in.Metrics {
		out.Metrics[i] = metric
		out.Metrics[i].Windows = append([]string(nil), metric.Windows...)
		out.Metrics[i].Currency = cloneString(metric.Currency)
		out.Metrics[i].Min = cloneString(metric.Min)
		out.Metrics[i].Max = cloneString(metric.Max)
	}
	for i, ent := range in.Entitlements {
		out.Entitlements[i] = ent
		out.Entitlements[i].Products = append([]string(nil), ent.Products...)
	}
	for i, proj := range in.Projections {
		out.Projections[i] = proj
		out.Projections[i].MeasureIDs = append([]string(nil), proj.MeasureIDs...)
	}
	return out
}

func cloneString(in *string) *string {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}
