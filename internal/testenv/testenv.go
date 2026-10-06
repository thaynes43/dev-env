// Package testenv starts a real kube-apiserver and etcd (controller-runtime's
// envtest) with this repo's CRDs from config/crd/ installed. Tests use it to prove
// what the API server does with the schema, and later suites (the controllers)
// run their reconcilers against it.
//
// The binaries come from setup-envtest, which `make test` runs first; it puts
// them in bin/tools/envtest/ and passes their directory in KUBEBUILDER_ASSETS.
// A plain `go test` finds them there too once `make envtest` has run.
//
// Each suite starts its own API server. Keep one TestMain per package and run the
// suites through `make test`, which caps Go at two packages at a time and runs
// under `nice -n 19` (CLAUDE.md, "No CPU burners").
package testenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// assetsEnv is the variable setup-envtest's output goes into.
const assetsEnv = "KUBEBUILDER_ASSETS"

// Env is a running API server with the CRDs installed.
type Env struct {
	// Config reaches the API server as an administrator.
	Config *rest.Config

	env *envtest.Environment
}

// Start starts the API server and installs every CRD in config/crd/. It fails if
// the binaries or the CRD directory cannot be found, so a suite never passes by
// not running.
func Start() (*Env, error) {
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	assets, err := assetsDir(root)
	if err != nil {
		return nil, err
	}

	env := &envtest.Environment{
		BinaryAssetsDirectory: assets,
		CRDDirectoryPaths:     []string{filepath.Join(root, "config", "crd")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		return nil, fmt.Errorf("start envtest (binaries in %s): %w", assets, err)
	}
	return &Env{Config: cfg, env: env}, nil
}

// Stop stops the API server and etcd.
func (e *Env) Stop() error {
	return e.env.Stop()
}

// assetsDir returns the directory that holds kube-apiserver, etcd and kubectl:
// KUBEBUILDER_ASSETS if set, else the only version under bin/tools/envtest/k8s/.
func assetsDir(root string) (string, error) {
	if dir := os.Getenv(assetsEnv); dir != "" {
		return dir, nil
	}
	matches, err := filepath.Glob(filepath.Join(root, "bin", "tools", "envtest", "k8s", "*"))
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("%s is unset and bin/tools/envtest/ has no binaries: run `make test`, or `make envtest` once before `go test`", assetsEnv)
	default:
		return "", fmt.Errorf("%s is unset and bin/tools/envtest/ holds %d versions: run `make test`, which picks the pinned one", assetsEnv, len(matches))
	}
}

// moduleRoot walks up from the working directory (go test runs in the package's
// directory) to the directory that holds go.mod.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}
