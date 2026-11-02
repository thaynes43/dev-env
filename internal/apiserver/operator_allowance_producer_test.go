package apiserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestOperatorOwnedAllowanceProducerKeepsGETAndRefusesExternalPUT(t *testing.T) {
	mem := &slotMem{cm: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, ResourceVersion: "1"}}}
	store := &AggregateAllowanceReportStore{Writer: mem, Live: mem, Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, DataKey: AggregateAllowanceDataKey, Registry: providerallowance.NewStaticRegistry(providerallowance.RegistryParts{}), MaxAge: time.Minute}
	h := &AggregateAllowanceReports{Enabled: true, Store: store, Live: mem, SessionNamespace: "dev-agents", SessionServiceAccount: "reader", SessionUIDs: []types.UID{"reader-uid"}, PublisherSessionUID: "selected-source-owner", PublisherServiceAccount: "selected-source-account", PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount}
	if !h.RouteEnabled() || !h.PublishEnabled() {
		t.Fatal("existing session publisher mode changed")
	}
	h.OperatorProducer = true
	if !h.RouteEnabled() || h.PublishEnabled() {
		t.Fatal("operator mode lost GET or enabled external PUT")
	}
	_, _, err := h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, ProviderAllowanceReportsPath, nil), nil)
	if err == nil || mem.updates != 0 {
		t.Fatal("operator mode accepted an external publication")
	}
	if h.allowPublisher(context.Background(), nil) == nil {
		t.Fatal("publisher admission ignored operator mode")
	}
	// The flag alone cannot create a legacy Source route or a partial slot.
	partial := &AggregateAllowanceReports{Enabled: true, OperatorProducer: true, Source: func(context.Context, time.Time) (providerallowance.ReadResponse, error) {
		t.Fatal("source called")
		return providerallowance.ReadResponse{}, nil
	}}
	if partial.RouteEnabled() || partial.PublishEnabled() {
		t.Fatal("partial operator mode mounted a route")
	}
}
