package apiserver

import (
	"context"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

// NewPortalBootstrapCreator constructs only the fixed target's CREATE path.
// controller-runtime's ordinary Client can retry Retry-After responses; this
// action must not. This client neither retries nor follows redirects.
// Construction performs no API request and adds no authority to the identity.
func NewPortalBootstrapCreator(config *rest.Config) (func(context.Context, *corev1.ConfigMap) error, error) {
	if config == nil {
		return nil, fmt.Errorf("portal bootstrap creator requires operator client configuration")
	}
	baseHTTPClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, err
	}
	httpClient := *baseHTTPClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	core, err := typedcorev1.NewForConfigAndClient(config, &httpClient)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, target *corev1.ConfigMap) error {
		if target == nil || target.Namespace != portalBootstrapTargetNamespace || target.Name != portalBootstrapTargetName || len(target.Data) != 2 || target.Data["schema"] != "1" || target.Data["state"] != "closed" || target.UID != "" || target.ResourceVersion != "" || target.BinaryData != nil || target.Immutable != nil || len(target.Labels) != 0 || len(target.Annotations) != 0 || len(target.OwnerReferences) != 0 || len(target.Finalizers) != 0 || target.GenerateName != "" {
			return fmt.Errorf("portal bootstrap creator requires the exact fixed CLOSED target")
		}
		return core.RESTClient().Post().Namespace(portalBootstrapTargetNamespace).Resource("configmaps").Body(target).MaxRetries(0).Do(ctx).Into(target)
	}, nil
}
