package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// ProviderAllowanceReportsPath is the only aggregate cached-read path.
// It is not /v1/provider-source-reports/codex-current-source.
const ProviderAllowanceReportsPath = "/v1/provider-allowances"

// AggregateAllowanceReports serves one disabled-by-default cached read.
// Source is an injected getter. A caller can adapt a collection snapshot with a
// closure; this handler neither configures acquisition nor keeps a second cache.
type AggregateAllowanceReports struct {
	Enabled bool

	// Source accepts the serving clock and returns one immutable read document.
	// The handler does not write that clock into provider freshness.
	Source                func(context.Context, time.Time) (providerallowance.ReadResponse, error)
	SessionNamespace      string
	SessionServiceAccount string
	SessionUIDs           []types.UID
	PortalNamespace       string
	PortalServiceAccount  string
	Now                   func() time.Time
}

// RouteEnabled reports whether Handler may mount the exact GET.
// A nil value, Enabled false, a missing getter, or an inexact reader binding
// leaves the route unregistered. Portal namespace and ServiceAccount must be
// the existing portal reader identity. Server.serve still refuses that
// identity on this path until its portal switch admits the route.
func (h *AggregateAllowanceReports) RouteEnabled() bool {
	if h == nil || !h.Enabled || h.Source == nil {
		return false
	}
	return h.sessionBinding() || h.portalBinding()
}

func (h *AggregateAllowanceReports) sessionBinding() bool {
	if h.SessionNamespace == "" || h.SessionServiceAccount == "" || len(h.SessionUIDs) == 0 {
		return false
	}
	seen := make(map[types.UID]struct{}, len(h.SessionUIDs))
	for _, uid := range h.SessionUIDs {
		if uid == "" {
			return false
		}
		if _, ok := seen[uid]; ok {
			return false
		}
		seen[uid] = struct{}{}
	}
	return true
}

func (h *AggregateAllowanceReports) portalBinding() bool {
	return h.PortalNamespace == portalReaderNamespace && h.PortalServiceAccount == portalReaderAccount
}

// Handle serves the exact GET. A disabled handler answers 404 and does not
// call Source. The response is a deep copy of the getter's document.
func (h *AggregateAllowanceReports) Handle(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if !h.RouteEnabled() {
		return 0, nil, allowanceUnmatched(r)
	}
	if err := allowanceExact(r); err != nil {
		return 0, nil, err
	}
	if r.Method != http.MethodGet {
		return 0, nil, newError(http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed, "%s takes GET", ProviderAllowanceReportsPath)
	}
	if err := rejectAllowanceBody(w, r); err != nil {
		return 0, nil, err
	}
	if err := h.allow(c); err != nil {
		return 0, nil, err
	}
	if ctx.Err() != nil {
		return 0, nil, allowanceUnavailable()
	}
	doc, err := h.Source(ctx, h.now())
	if err != nil || ctx.Err() != nil {
		return 0, nil, allowanceUnavailable()
	}
	cloned, err := cloneAllowanceRead(doc)
	if err != nil {
		if errors.Is(err, errAllowanceTooLarge) {
			return 0, nil, newError(http.StatusRequestEntityTooLarge, apiv1.CodeTooLarge, "the allowance snapshot is too large")
		}
		return 0, nil, allowanceUnavailable()
	}
	return http.StatusOK, cloned, nil
}

func (h *AggregateAllowanceReports) allow(c *caller) error {
	if c == nil {
		return forbidden("the allowance reader is not allowlisted")
	}
	switch c.kind {
	case kindPortalReader:
		return h.allowPortal(c)
	case kindSession:
		return h.allowSession(c)
	default:
		return forbidden("the allowance reader is not allowlisted")
	}
}

func (h *AggregateAllowanceReports) allowSession(c *caller) error {
	if !h.sessionBinding() || c.session == nil || c.pod == nil || c.identity.PodName == "" || c.identity.PodUID == "" {
		return forbidden("the allowance reader is not allowlisted")
	}
	if c.identity.Namespace != h.SessionNamespace || c.identity.ServiceAccount != h.SessionServiceAccount {
		return forbidden("the allowance reader is not allowlisted")
	}
	if c.pod.Namespace != h.SessionNamespace || string(c.pod.UID) != c.identity.PodUID || c.pod.Spec.ServiceAccountName != h.SessionServiceAccount || c.pod.DeletionTimestamp != nil {
		return forbidden("the allowance reader is not allowlisted")
	}
	if c.session.Namespace != h.SessionNamespace || !c.session.DeletionTimestamp.IsZero() || c.session.UID == "" {
		return forbidden("the allowance reader is not allowlisted")
	}
	for _, uid := range h.SessionUIDs {
		if uid == c.session.UID {
			return nil
		}
	}
	return forbidden("the allowance reader is not allowlisted")
}

func (h *AggregateAllowanceReports) allowPortal(c *caller) error {
	if !h.portalBinding() || c.pod == nil || c.identity.PodName == "" || c.identity.PodUID == "" {
		return forbidden("the allowance reader is not allowlisted")
	}
	if c.identity.Namespace != h.PortalNamespace || c.identity.ServiceAccount != h.PortalServiceAccount {
		return forbidden("the allowance reader is not allowlisted")
	}
	if c.pod.Namespace != portalReaderNamespace || string(c.pod.UID) != c.identity.PodUID || c.pod.DeletionTimestamp != nil || c.pod.Spec.ServiceAccountName != portalReaderAccount {
		return forbidden("the allowance reader is not allowlisted")
	}
	return nil
}

func (h *AggregateAllowanceReports) now() time.Time {
	if h != nil && h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func allowanceExact(r *http.Request) error {
	if r == nil || r.URL == nil || r.URL.Path != ProviderAllowanceReportsPath || (r.URL.RawPath != "" && r.URL.RawPath != ProviderAllowanceReportsPath) {
		return allowanceUnmatched(r)
	}
	if r.URL.RawQuery != "" {
		return badRequest("the allowance path is not exact")
	}
	return nil
}

func rejectAllowanceBody(w http.ResponseWriter, r *http.Request) error {
	if r == nil || r.Body == nil {
		return nil
	}
	limited := http.MaxBytesReader(w, r.Body, 1)
	raw, err := io.ReadAll(limited)
	_ = limited.Close()
	if err != nil || len(raw) > 0 {
		return badRequest("the allowance read does not accept a body")
	}
	return nil
}

func allowanceUnmatched(r *http.Request) error {
	method, path := "", ""
	if r != nil {
		method = r.Method
		if r.URL != nil {
			path = r.URL.Path
		}
	}
	return notFound("no route %s %s", method, path)
}

func allowanceUnavailable() *apiError {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the allowance snapshot is unavailable")
}

var (
	errAllowanceInvalid  = errors.New("allowance_snapshot_invalid")
	errAllowanceTooLarge = errors.New("allowance_snapshot_too_large")
)

func cloneAllowanceRead(doc providerallowance.ReadResponse) (providerallowance.ReadResponse, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return providerallowance.ReadResponse{}, errAllowanceInvalid
	}
	if len(raw) > providerallowance.FixtureParseLimits.MaxBytes {
		return providerallowance.ReadResponse{}, errAllowanceTooLarge
	}
	cloned, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		return providerallowance.ReadResponse{}, errAllowanceInvalid
	}
	return cloned, nil
}
