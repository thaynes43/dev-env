package broker

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/grantdecision"
)

func TestManualPodLabelUsesUIDAndRejectsConflict(t *testing.T) {
	f := credentialCase(t)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil { t.Fatal(err) }
	if err := f.b.labelManualPod(context.Background(), &pod); err != nil { t.Fatal(err) }
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil { t.Fatal(err) }
	if pod.Labels[grantdecision.PodUIDLabel] != string(pod.UID) { t.Fatal("reserved label did not match pod UID") }
	pod.Labels[grantdecision.PodUIDLabel] = "another-pod"
	if err := f.b.labelManualPod(context.Background(), &pod); err == nil { t.Fatal("conflicting UID label accepted") }
}
