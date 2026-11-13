package testenv

import "testing"

// The shared environment always starts a local control plane: an inherited
// USE_EXISTING_CLUSTER=true, which controller-runtime's envtest honours only
// while UseExistingCluster is unset, cannot point a suite at a real cluster
// (story 782 fix round 2). Nothing is started here.
func TestEnvironmentIsAlwaysLocal(t *testing.T) {
	t.Setenv("USE_EXISTING_CLUSTER", "true")
	env := environment("/module", "/assets")
	if env.UseExistingCluster == nil || *env.UseExistingCluster {
		t.Fatalf("UseExistingCluster is %v, want a set false", env.UseExistingCluster)
	}
	if env.BinaryAssetsDirectory != "/assets" || len(env.CRDDirectoryPaths) != 1 || env.CRDDirectoryPaths[0] != "/module/config/crd" || !env.ErrorIfCRDPathMissing {
		t.Fatalf("environment %+v", env)
	}
}
