package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// ProviderAllowanceReportsPath is the only aggregate cached-read path.
// It is not /v1/provider-source-reports/codex-current-source.
const ProviderAllowanceReportsPath = "/v1/provider-allowances"

// aggregateAllowanceStore is the one common slot. Tests substitute a double.
// The Kubernetes store is AggregateAllowanceReportStore.
type aggregateAllowanceStore interface {
	Read(ctx context.Context) (AggregateAllowanceView, error)
	Put(ctx context.Context, raw []byte, now time.Time) error
	configured() bool
	trust() AggregateAllowanceTrust
}

// AggregateAllowanceReports serves one disabled-by-default cached read.
// Source is an injected getter and is not mounted together with Store.
// Store is the one common slot. PUT is the selected publisher only.
type AggregateAllowanceReports struct {
	Enabled bool

	// Source accepts the serving clock and returns one immutable read document.
	// The handler does not write that clock into provider freshness.
	Source                  func(context.Context, time.Time) (providerallowance.ReadResponse, error)
	Store                   aggregateAllowanceStore
	Live                    objectGetter
	SessionNamespace        string
	SessionServiceAccount   string
	SessionUIDs             []types.UID
	PublisherSessionUID     types.UID
	PublisherServiceAccount string
	PortalNamespace         string
	PortalServiceAccount    string
	Now                     func() time.Time
}

// RouteEnabled reports whether Handler may mount the path.
// A nil value, Enabled false, a missing getter, or an inexact reader binding
// leaves the route unregistered. A store, live client, or publisher id selects
// the slot and refuses a partial or mixed Source binding. Portal namespace and
// ServiceAccount must be the existing portal reader identity.
func (h *AggregateAllowanceReports) RouteEnabled() bool {
	if h == nil || !h.Enabled {
		return false
	}
	if h.usesStore() {
		return h.storeMode()
	}
	if h.Source == nil {
		return false
	}
	return h.sessionBinding() || h.portalBinding()
}

// PublishEnabled reports whether PUT may be mounted. GET-only source mode is false.
func (h *AggregateAllowanceReports) PublishEnabled() bool {
	return h.storeMode()
}

// PortalAggregateAllowanceGET is the only portal admission for this path.
// PUT and every other route stay false.
func PortalAggregateAllowanceGET(enabled bool, method, path string) bool {
	return enabled && method == http.MethodGet && path == ProviderAllowanceReportsPath
}

func (h *AggregateAllowanceReports) usesStore() bool {
	return h != nil && (h.Store != nil || h.Live != nil || h.PublisherSessionUID != "" || h.PublisherServiceAccount != "")
}

func (h *AggregateAllowanceReports) storeMode() bool {
	if h == nil || !h.Enabled || h.Source != nil || h.Store == nil || h.Live == nil || !h.Store.configured() || !h.publisherBinding() {
		return false
	}
	return h.sessionBinding() || h.portalBinding()
}

func (h *AggregateAllowanceReports) publisherBinding() bool {
	return h != nil && h.PublisherSessionUID != "" && h.PublisherServiceAccount != "" && h.SessionNamespace != ""
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

// Handle serves the exact GET, and PUT when the slot is selected.
// A disabled handler answers 404 and does not call Source or the store.
func (h *AggregateAllowanceReports) Handle(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if !h.RouteEnabled() {
		return 0, nil, allowanceUnmatched(r)
	}
	if err := allowanceExact(r); err != nil {
		return 0, nil, err
	}
	if h.storeMode() {
		return h.handleStored(ctx, w, r, c)
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
	return h.readSource(ctx)
}

func (h *AggregateAllowanceReports) handleStored(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	switch r.Method {
	case http.MethodGet:
		if err := rejectAllowanceBody(w, r); err != nil {
			return 0, nil, err
		}
		if err := h.allow(c); err != nil {
			return 0, nil, err
		}
		return h.readStored(ctx)
	case http.MethodPut:
		if err := h.allowPublisher(ctx, c); err != nil {
			return 0, nil, err
		}
		return h.putStored(ctx, w, r)
	default:
		return 0, nil, newError(http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed, "%s takes GET and PUT", ProviderAllowanceReportsPath)
	}
}

func (h *AggregateAllowanceReports) readSource(ctx context.Context) (int, any, error) {
	if ctx.Err() != nil {
		return 0, nil, allowanceUnavailable()
	}
	doc, err := h.Source(ctx, h.now())
	if err != nil || ctx.Err() != nil {
		return 0, nil, allowanceUnavailable()
	}
	return allowanceDocument(doc)
}

func (h *AggregateAllowanceReports) readStored(ctx context.Context) (int, any, error) {
	if ctx.Err() != nil {
		return 0, nil, allowanceUnavailable()
	}
	view, err := h.Store.Read(ctx)
	if err != nil || ctx.Err() != nil {
		return 0, nil, aggregateAllowanceError(err)
	}
	if view.Suppressed {
		return 0, nil, allowanceUnavailable()
	}
	if !view.Found || len(view.Raw) == 0 {
		return allowanceDocument(UnconfiguredAggregateDocument())
	}
	doc, err := providerallowance.DecodeReadResponse(view.Raw, providerallowance.FixtureParseLimits)
	if err != nil {
		return 0, nil, allowanceUnavailable()
	}
	trust := h.Store.trust()
	providerallowance.ProjectCachedRead(&doc, trust.Contexts, trust.Registry, trust.MaxAge, true, h.now())
	return allowanceDocument(doc)
}

func (h *AggregateAllowanceReports) putStored(ctx context.Context, w http.ResponseWriter, r *http.Request) (int, any, error) {
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	limited := http.MaxBytesReader(w, body, int64(providerallowance.FixtureParseLimits.MaxBytes))
	raw, err := io.ReadAll(limited)
	_ = limited.Close()
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return 0, nil, newError(http.StatusRequestEntityTooLarge, apiv1.CodeTooLarge, "the allowance report is too large")
		}
		return 0, nil, badRequest("the allowance report was refused")
	}
	if err := h.Store.Put(ctx, raw, h.now()); err != nil {
		return 0, nil, aggregateAllowanceError(err)
	}
	return http.StatusNoContent, nil, nil
}

func allowanceDocument(doc providerallowance.ReadResponse) (int, any, error) {
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

func (h *AggregateAllowanceReports) allowPublisher(ctx context.Context, c *caller) error {
	if c == nil || c.kind != kindSession || !h.publisherBinding() || c.identity.Namespace != h.SessionNamespace || c.identity.ServiceAccount != h.PublisherServiceAccount || c.identity.PodName == "" || c.identity.PodUID == "" {
		return forbidden("the allowance publisher is not the selected session")
	}
	pod, err := h.publisherPod(ctx, c.identity.PodName, c.identity.PodUID)
	if err != nil {
		return err
	}
	return h.publisherSession(ctx, c, pod)
}

func (h *AggregateAllowanceReports) publisherPod(ctx context.Context, name, uid string) (*corev1.Pod, error) {
	if h.Live == nil || h.SessionNamespace == "" || name == "" || uid == "" || h.PublisherServiceAccount == "" {
		return nil, forbidden("the allowance publisher is not the selected session")
	}
	var pod corev1.Pod
	if err := h.Live.Get(ctx, types.NamespacedName{Namespace: h.SessionNamespace, Name: name}, &pod); err != nil || pod.Name != name || pod.Namespace != h.SessionNamespace || string(pod.UID) != uid || pod.DeletionTimestamp != nil || pod.Spec.ServiceAccountName != h.PublisherServiceAccount || pod.Status.Phase != corev1.PodRunning {
		return nil, forbidden("the allowance publisher is not the selected session")
	}
	return &pod, nil
}

func (h *AggregateAllowanceReports) publisherSession(ctx context.Context, c *caller, pod *corev1.Pod) error {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "AgentSession" || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.UID != h.PublisherSessionUID || owner.Name != c.identity.PodName {
		return forbidden("the allowance publisher is not the selected session")
	}
	var sess v1alpha1.AgentSession
	if h.Live.Get(ctx, types.NamespacedName{Namespace: h.SessionNamespace, Name: owner.Name}, &sess) != nil {
		return forbidden("the allowance publisher is not the selected session")
	}
	if sess.Name != c.identity.PodName || sess.UID != h.PublisherSessionUID || sess.UID != owner.UID || !sess.DeletionTimestamp.IsZero() || sess.Status.PodName != c.identity.PodName {
		return forbidden("the allowance publisher is not the selected session")
	}
	if sess.Spec.OperatingMode != v1alpha1.OperatingModeRunning {
		return forbidden("the allowance publisher is not the selected session")
	}
	switch sess.Status.Phase {
	case v1alpha1.PhaseRunning, v1alpha1.PhaseIdle:
	default:
		return forbidden("the allowance publisher is not the selected session")
	}
	if !metav1.IsControlledBy(pod, &sess) {
		return forbidden("the allowance publisher is not the selected session")
	}
	return nil
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

func aggregateAllowanceError(err error) error {
	switch {
	case errors.Is(err, ErrAggregateAbsent):
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the allowance slot is absent")
	case errors.Is(err, ErrAggregateConflict):
		return newError(http.StatusConflict, apiv1.CodeConflict, "the allowance slot changed")
	case errors.Is(err, ErrAggregateDigest), errors.Is(err, ErrAggregateRejected):
		return newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "the allowance report was refused")
	default:
		return allowanceUnavailable()
	}
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
