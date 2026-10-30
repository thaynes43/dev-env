package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"
)

// The real pinned CCNP selectors are intentionally broad. Isolation requires the
// review patch to exclude maintenance from both additive allow tiers. This is an
// offline selector check, not a claim of runtime Cilium enforcement.
func TestMaintenanceExcludedFromAdditiveNetworkTiers(t *testing.T) {
	for _, part := range []string{"platform", "web"} {
		t.Run(part, func(t *testing.T) {
			path := filepath.Join("..", "..", ".agents", "sagas", "distributed-dev-env", "verification", "bounded-maintenance-2026-10-30", "maintenance-network-"+part+".yaml")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Spec struct {
					Selector metav1.LabelSelector `json:"endpointSelector"`
				} `json:"spec"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			selector, err := metav1.LabelSelectorAsSelector(&doc.Spec.Selector)
			if err != nil {
				t.Fatal(err)
			}
			normal := labels.Set{"io.kubernetes.pod.namespace": "dev-agents"}
			if !selector.Matches(normal) {
				t.Fatal("normal session baseline selection changed")
			}
			normal[MaintenanceLabel] = "grant-uid"
			if selector.Matches(normal) {
				t.Fatal("maintenance executor still receives additive baseline network allow")
			}
			// Both baseline files must also carry the exclusion in the review patch.
			patch, err := os.ReadFile(filepath.Join(filepath.Dir(path), "haynes-ops-UNAPPLIED.patch"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(patch), "diff --git a/kubernetes/main/apps/dev-env-system/network/app/session-"+part+".yaml") || !strings.Contains(string(patch), "+      - key: "+MaintenanceLabel) {
				t.Fatal("selector exclusion missing from exact review patch")
			}
		})
	}
}
