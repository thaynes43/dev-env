package session

import (
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

// AllowanceReader projects one caller-supplied cached allowance read.
// Allowances uses now only to degrade age and exact reset instants.
// The snapshot's provider observed time stays as stored.
type AllowanceReader interface {
	Allowances(now time.Time) []portal.ProductAllowance
}

// AllowanceReadPolicy is the caller-supplied max age for one cached read.
// MaxAge applies only when Set is true and MaxAge is zero or positive.
// An unset or negative policy expires a cached fresh or stale label.
type AllowanceReadPolicy struct {
	MaxAge time.Duration
	Set    bool
}

// cachedAllowanceReader is an immutable providerallowance.read.v1 snapshot.
// Construction copies the bytes and trust contexts. Allowances decodes those
// bytes again and projects a copy. It does not fetch or write the snapshot.
type cachedAllowanceReader struct {
	raw      []byte
	limits   providerallowance.ParseLimits
	contexts []providerallowance.TrustedContext
	reg      providerallowance.Registry
	policy   AllowanceReadPolicy
}

// NewCachedAllowanceReader stores one cached read for later projection.
// limits, contexts, reg, and policy are caller inputs. Account, workspace,
// producer, and generation come from contexts. Validation and binding labels
// in raw are not authority. A decode failure still returns a reader; its
// projection uses the fixed unsupported-document allowance.
func NewCachedAllowanceReader(raw []byte, limits providerallowance.ParseLimits, contexts []providerallowance.TrustedContext, reg providerallowance.Registry, policy AllowanceReadPolicy) AllowanceReader {
	copied := make([]providerallowance.TrustedContext, len(contexts))
	for i, ctx := range contexts {
		copied[i] = cloneTrustedContext(ctx)
	}
	return &cachedAllowanceReader{
		raw:      append([]byte(nil), raw...),
		limits:   limits,
		contexts: copied,
		reg:      reg,
		policy:   policy,
	}
}

// SetAllowanceReader installs reader for later SessionSource reads.
// Nil leaves the disconnected allowances from the session projection.
// Set it before Read. Assembly and production config do not call it.
func (s *SessionSource) SetAllowanceReader(reader AllowanceReader) {
	if s == nil {
		return
	}
	s.allowance = reader
}

func applyCachedAllowance(page portal.ProductPage, reader AllowanceReader, now func() time.Time) portal.ProductPage {
	if reader == nil || now == nil {
		return page
	}
	allowances := reader.Allowances(now())
	if allowances == nil {
		return page
	}
	page.Allowance = allowances
	if page.Usage != nil {
		page.Usage.Allowance = allowances
	}
	return page
}

func (r *cachedAllowanceReader) Allowances(now time.Time) []portal.ProductAllowance {
	if r == nil {
		return disconnectedAllowances()
	}
	doc, err := providerallowance.DecodeReadResponse(r.raw, r.limits)
	if err != nil {
		return MapProductAllowance(providerallowance.ReadResponse{})
	}
	projectCachedAllowance(&doc, r.contexts, r.reg, r.policy, now)
	return MapProductAllowance(doc)
}

func projectCachedAllowance(doc *providerallowance.ReadResponse, contexts []providerallowance.TrustedContext, reg providerallowance.Registry, policy AllowanceReadPolicy, now time.Time) {
	providerallowance.ProjectCachedRead(doc, contexts, reg, policy.MaxAge, policy.Set, now)
}

func cloneTrustedContext(in providerallowance.TrustedContext) providerallowance.TrustedContext {
	out := in
	out.AccountRef = cloneAllowanceString(in.AccountRef)
	out.WorkspaceRef = cloneAllowanceString(in.WorkspaceRef)
	return out
}

func cloneAllowanceString(in *string) *string {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}
