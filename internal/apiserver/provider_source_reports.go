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
)

// ProviderSourceReportPath is the only slot path. Nothing in this package
// registers it. Server.Handler stays unchanged until the connection patch runs.
const ProviderSourceReportPath = "/v1/provider-source-reports/codex-current-source"

// providerSourceStore is the fixed slot. Tests substitute a memory double.
// The Kubernetes store is ProviderSourceReportStore.
type providerSourceStore interface {
	Read(ctx context.Context, now time.Time) (ProviderSourceView, error)
	Put(ctx context.Context, raw []byte, now time.Time) error
	configured() bool
}

// ProviderSourceReports is the unregistered handler for one selected session.
// Enabled is not implied by MaxAge. A historical Pod UID is not a field:
// every call reads the current Pod from Live.
// claimSnapshot is not used.
type ProviderSourceReports struct {
	Enabled                 bool
	Store                   providerSourceStore
	Live                    objectGetter
	SessionNamespace        string
	PublisherSessionUID     types.UID
	PublisherServiceAccount string
	PortalNamespace         string
	PortalServiceAccount    string
	Now                     func() time.Time
}

// RouteEnabled reports whether the later patch may mount the path.
// MaxAge is not an input. A nil handler, a missing dependency, or an empty
// publisher or portal argument keeps the route off.
func (h *ProviderSourceReports) RouteEnabled() bool {
	if h == nil || !h.Enabled || h.Live == nil || h.Store == nil || !h.Store.configured() {
		return false
	}
	if h.PublisherSessionUID == "" || h.PublisherServiceAccount == "" || h.SessionNamespace == "" {
		return false
	}
	return h.PortalNamespace != "" && h.PortalServiceAccount != ""
}

// PortalProviderSourceGET is the only portal admission the connection patch
// may add. PUT, list, inbox, published context, and any other path are false.
func PortalProviderSourceGET(enabled bool, method, path string) bool {
	return enabled && method == http.MethodGet && path == ProviderSourceReportPath
}

// Handle serves one exact GET or PUT. It is not mounted. A disabled handler
// answers 404 and does not read or write the slot, including when the stored
// report is still inside MaxAge.
func (h *ProviderSourceReports) Handle(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if !h.RouteEnabled() {
		return 0, nil, providerSourceUnmatched(r)
	}
	if err := providerSourceExact(r); err != nil {
		return 0, nil, err
	}
	switch r.Method {
	case http.MethodGet:
		if err := h.allowRead(ctx, c); err != nil {
			return 0, nil, err
		}
		return h.read(ctx)
	case http.MethodPut:
		if err := h.allowPublisher(ctx, c); err != nil {
			return 0, nil, err
		}
		return h.put(ctx, w, r)
	default:
		return 0, nil, newError(http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed, "%s takes GET and PUT", ProviderSourceReportPath)
	}
}

func (h *ProviderSourceReports) read(ctx context.Context) (int, any, error) {
	view, err := h.Store.Read(ctx, h.now())
	if err != nil {
		return 0, nil, providerSourceError(err)
	}
	if !view.Fresh || len(view.Raw) == 0 {
		return http.StatusOK, providerSourceUnknown{Availability: "Unknown"}, nil
	}
	return http.StatusOK, json.RawMessage(append([]byte(nil), view.Raw...)), nil
}

func (h *ProviderSourceReports) put(ctx context.Context, w http.ResponseWriter, r *http.Request) (int, any, error) {
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	limited := http.MaxBytesReader(w, body, providerSourceMaxBody)
	raw, err := io.ReadAll(limited)
	_ = limited.Close()
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return 0, nil, newError(http.StatusRequestEntityTooLarge, apiv1.CodeTooLarge, "the source report is too large")
		}
		return 0, nil, badRequest("the source report was refused")
	}
	if err := h.Store.Put(ctx, raw, h.now()); err != nil {
		return 0, nil, providerSourceError(err)
	}
	return http.StatusNoContent, nil, nil
}

func (h *ProviderSourceReports) allowRead(ctx context.Context, c *caller) error {
	if c != nil && c.kind == kindPortalReader {
		return h.allowPortal(ctx, c)
	}
	return h.allowPublisher(ctx, c)
}

func (h *ProviderSourceReports) allowPublisher(ctx context.Context, c *caller) error {
	if c == nil || c.kind != kindSession || h.PublisherSessionUID == "" || h.PublisherServiceAccount == "" || h.SessionNamespace == "" {
		return forbidden("the source publisher is not the selected session")
	}
	if c.identity.Namespace != h.SessionNamespace || c.identity.ServiceAccount != h.PublisherServiceAccount || c.identity.PodName == "" || c.identity.PodUID == "" {
		return forbidden("the source publisher is not the selected session")
	}
	pod, err := h.currentPod(ctx, h.SessionNamespace, c.identity.PodName, c.identity.PodUID, h.PublisherServiceAccount)
	if err != nil {
		return err
	}
	return h.publisherSession(ctx, c, pod)
}

func (h *ProviderSourceReports) allowPortal(ctx context.Context, c *caller) error {
	if c == nil || c.kind != kindPortalReader || h.PortalNamespace == "" || h.PortalServiceAccount == "" || c.identity.Namespace != h.PortalNamespace || c.identity.ServiceAccount != h.PortalServiceAccount {
		return forbidden("the source reader is not the selected portal")
	}
	_, err := h.currentPod(ctx, h.PortalNamespace, c.identity.PodName, c.identity.PodUID, h.PortalServiceAccount)
	return err
}

func (h *ProviderSourceReports) publisherSession(ctx context.Context, c *caller, pod *corev1.Pod) error {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "AgentSession" || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.UID != h.PublisherSessionUID || owner.Name != c.identity.PodName {
		return forbidden("the source publisher is not the selected session")
	}
	var sess v1alpha1.AgentSession
	if h.Live.Get(ctx, types.NamespacedName{Namespace: h.SessionNamespace, Name: owner.Name}, &sess) != nil {
		return forbidden("the source publisher is not the selected session")
	}
	if sess.Name != c.identity.PodName || sess.UID != h.PublisherSessionUID || sess.UID != owner.UID || !sess.DeletionTimestamp.IsZero() || sess.Status.PodName != c.identity.PodName {
		return forbidden("the source publisher is not the selected session")
	}
	if sess.Spec.OperatingMode != v1alpha1.OperatingModeRunning {
		return forbidden("the source publisher is not the selected session")
	}
	switch sess.Status.Phase {
	case v1alpha1.PhaseRunning, v1alpha1.PhaseIdle:
	default:
		return forbidden("the source publisher is not the selected session")
	}
	if !metav1.IsControlledBy(pod, &sess) {
		return forbidden("the source publisher is not the selected session")
	}
	return nil
}

func (h *ProviderSourceReports) currentPod(ctx context.Context, namespace, name, uid, serviceAccount string) (*corev1.Pod, error) {
	if h.Live == nil || namespace == "" || name == "" || uid == "" || serviceAccount == "" {
		return nil, forbidden("the source caller is not bound to its current pod")
	}
	var pod corev1.Pod
	if err := h.Live.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &pod); err != nil || pod.Name != name || pod.Namespace != namespace || string(pod.UID) != uid || pod.DeletionTimestamp != nil || pod.Spec.ServiceAccountName != serviceAccount || pod.Status.Phase != corev1.PodRunning {
		return nil, forbidden("the source caller is not bound to its current pod")
	}
	return &pod, nil
}

func (h *ProviderSourceReports) now() time.Time {
	if h != nil && h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

type providerSourceUnknown struct {
	Availability string `json:"availability"`
}

func providerSourceUnmatched(r *http.Request) error {
	method, path := "", ""
	if r != nil {
		method = r.Method
		if r.URL != nil {
			path = r.URL.Path
		}
	}
	return notFound("no route %s %s", method, path)
}

func providerSourceExact(r *http.Request) error {
	if r == nil || r.URL == nil || r.URL.Path != ProviderSourceReportPath || (r.URL.RawPath != "" && r.URL.RawPath != ProviderSourceReportPath) {
		return providerSourceUnmatched(r)
	}
	if r.URL.RawQuery != "" {
		return badRequest("the source path is not exact")
	}
	return nil
}

func providerSourceError(err error) error {
	switch {
	case errors.Is(err, ErrProviderSourceAbsent):
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the source slot is absent")
	case errors.Is(err, ErrProviderSourceConflict):
		return newError(http.StatusConflict, apiv1.CodeConflict, "the source slot changed")
	case errors.Is(err, ErrProviderSourceDigest), errors.Is(err, ErrProviderSourceRetrieval), errors.Is(err, ErrProviderSourceRejected):
		return newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "the source report was refused")
	default:
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the source slot is unavailable")
	}
}
