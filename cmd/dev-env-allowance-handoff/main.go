// Command dev-env-allowance-handoff is an explicitly invoked maintenance tool.
// Preparing a hash is not human approval and does not change cluster state.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/allowancecollector"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

var errRefused = errors.New("allowance handoff refused")

type clientFactory func(string, string) (client.Client, error)

func main() {
	if err := run(os.Args[1:], os.Stdout, time.Now, makeClient); err != nil {
		fmt.Fprintln(os.Stderr, errRefused)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer, clock func() time.Time, factory clientFactory) error {
	if len(args) < 1 || out == nil || clock == nil || factory == nil || args[0] != "prepare" && args[0] != "apply" {
		return errRefused
	}
	flags := flag.NewFlagSet("handoff", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "")
	kubeconfig := flags.String("kubeconfig", "", "")
	kubecontext := flags.String("context", "", "")
	namespace := flags.String("namespace", "", "")
	slot := flags.String("slot", "", "")
	approved := flags.String("approved-hash", "", "")
	backup := flags.String("backup-dir", "", "")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*input) || !filepath.IsAbs(*kubeconfig) || *kubecontext == "" || *namespace != apiserver.AggregateAllowanceNamespace || *slot != apiserver.AggregateAllowanceName {
		return errRefused
	}
	if args[0] == "prepare" && (*approved != "" || *backup != "") || args[0] == "apply" && (*approved == "" || !filepath.IsAbs(*backup)) {
		return errRefused
	}
	raw, err := readPrivate(*input, 512<<10)
	if err != nil {
		return errRefused
	}
	snap, err := allowancecollector.DecodeHandoffSnapshot(raw)
	if err != nil {
		return errRefused
	}
	trust, err := providerallowance.ParseAggregateTrust(snap.Trust)
	if err != nil {
		return errRefused
	}
	in := snap.Input(trust, snap.PublisherUID, snap.PublisherSA, *kubeconfig, *kubecontext, clock().UTC())
	plan, err := allowancecollector.PrepareHandoff(in)
	if err != nil {
		return errRefused
	}
	if args[0] == "prepare" {
		_, err = fmt.Fprintln(out, plan.Hash)
		return err
	}
	if *approved != plan.Hash {
		return errRefused
	}
	// No client or kubeconfig is opened before the exact prepared hash matches.
	c, err := factory(*kubeconfig, *kubecontext)
	if err != nil {
		return errRefused
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if allowancecollector.ApplyHandoff(ctx, c, in, *approved, *backup) != nil {
		return errRefused
	}
	_, err = fmt.Fprintln(out, plan.Hash)
	return err
}

func readPrivate(path string, limit int64) ([]byte, error) {
	if filepath.Clean(path) != path {
		return nil, errRefused
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, errRefused
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errRefused
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	st, ok := infoSys(info)
	if err != nil || !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > limit {
		return nil, errRefused
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit || int64(len(raw)) != info.Size() {
		return nil, errRefused
	}
	return raw, nil
}

func makeClient(path, selected string) (client.Client, error) {
	config, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, errRefused
	}
	selectedContext := config.Contexts[selected]
	if selectedContext == nil {
		return nil, errRefused
	}
	cluster := config.Clusters[selectedContext.Cluster]
	auth := config.AuthInfos[selectedContext.AuthInfo]
	if cluster == nil || auth == nil || cluster.ProxyURL != "" || cluster.InsecureSkipTLSVerify || auth.Exec != nil || auth.AuthProvider != nil {
		return nil, errRefused
	}
	rest, err := clientcmd.NewNonInteractiveClientConfig(*config, selected, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, errRefused
	}
	rest.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	scheme := runtime.NewScheme()
	if corev1.AddToScheme(scheme) != nil || v1alpha1.AddToScheme(scheme) != nil {
		return nil, errRefused
	}
	return client.New(rest, client.Options{Scheme: scheme})
}

func infoSys(fi os.FileInfo) (*syscall.Stat_t, bool) {
	if fi == nil {
		return nil, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return st, ok
}
