package main

import (
	"errors"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/taskbudget"
)

// Explicit operator/GitOps configuration selects only the isolated fixture;
// ordinary native hosts stay closed and the default factory remains nil.
// Signature/source verification precedes supplying this immutable image value.
func newNativeFixtureInspector(reader client.Reader, image string, enabled bool) (taskbudget.NativeAdmissionInspector, error) {
	if !enabled {
		if image != "" {
			return nil, errors.New("native fixture image requires explicit fixture enablement")
		}
		return nil, nil
	}
	if reader == nil || !taskbudget.ValidNativeFixtureImage(image) {
		return nil, errors.New("native fixture requires an uncached reader and reviewed immutable agent image")
	}
	return &taskbudget.KubeNativeFixtureInspector{Reader: reader, Image: image}, nil
}

func validateNativeFixtureOptions(o options) error {
	if !o.nativeFixtureEnabled {
		if o.nativeFixtureImage != "" {
			return errors.New("native fixture image requires enable-native-lifecycle-fixture")
		}
		return nil
	}
	if !o.taskBudgetsEnabled || !taskbudget.ValidNativeFixtureImage(o.nativeFixtureImage) || len(o.coordinatorHosts) != 1 || len(o.assignedTaskBudgets) != 1 {
		return errors.New("native fixture requires task budgets, one isolated assigned coordinator and reviewed agent digest")
	}
	host := o.coordinatorHosts[0]
	if host.HostID != taskbudget.NativeFixtureHostID || host.PodName != taskbudget.NativeFixtureName ||
		host.ServiceAccount != taskbudget.NativeFixtureNamespace+"/"+taskbudget.NativeFixtureServiceAccount || o.assignedTaskBudgets[host.HostID] == "" {
		return errors.New("native fixture coordinator must match the fixed isolated profile")
	}
	return nil
}
