// Command dev-env-allowance-handoff is an explicitly invoked maintenance tool.
// Preparing a hash is not human approval and does not change cluster state.
package main

import (
	"bytes"
	"context"
	"encoding/json"
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

var refused = errors.New("allowance handoff refused")

type snapshot struct {
	OldConfig       []byte `json:"oldConfig"`
	NewConfig       []byte `json:"newConfig"`
	Trust           []byte `json:"trust"`
	PublisherUID    string `json:"publisherUID"`
	PublisherSA     string `json:"publisherSA"`
	SlotUID         string `json:"slotUID"`
	ResourceVersion string `json:"resourceVersion"`
	HomeUID         string `json:"homeUID"`
	Journal         []byte `json:"journal"`
	Report          []byte `json:"report"`
	JournalSHA      string `json:"journalSHA256"`
	ReportSHA       string `json:"reportSHA256"`
	Cycle           int64  `json:"cycle"`
	Nonce           string `json:"nonce"`
}

var snapshotKeys = []string{"oldConfig", "newConfig", "trust", "publisherUID", "publisherSA", "slotUID", "resourceVersion", "homeUID", "journal", "report", "journalSHA256", "reportSHA256", "cycle", "nonce"}

type clientFactory func(string, string) (client.Client, error)

func main() {
	if err := run(os.Args[1:], os.Stdout, time.Now, makeClient); err != nil {
		fmt.Fprintln(os.Stderr, refused)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer, clock func() time.Time, factory clientFactory) error {
	if len(args) < 1 || out == nil || clock == nil || factory == nil || args[0] != "prepare" && args[0] != "apply" {
		return refused
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
		return refused
	}
	if args[0] == "prepare" && (*approved != "" || *backup != "") || args[0] == "apply" && (*approved == "" || !filepath.IsAbs(*backup)) {
		return refused
	}
	raw, err := readPrivate(*input, 512<<10)
	if err != nil || !providerallowance.ValidJSONDocument(raw, 512<<10) || !providerallowance.ExactObjectKeys(raw, snapshotKeys) {
		return refused
	}
	var snap snapshot
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&snap) != nil || dec.Decode(new(any)) != io.EOF {
		return refused
	}
	trust, err := providerallowance.ParseAggregateTrust(snap.Trust)
	if err != nil {
		return refused
	}
	in := allowancecollector.HandoffInput{OldConfig: snap.OldConfig, NewConfig: snap.NewConfig, Trust: trust, PublisherUID: snap.PublisherUID, PublisherSA: snap.PublisherSA, SlotUID: snap.SlotUID, ResourceVersion: snap.ResourceVersion, HomeUID: snap.HomeUID, KubeconfigPath: *kubeconfig, KubeContext: *kubecontext, Journal: snap.Journal, Report: snap.Report, ExpectedJournalSHA: snap.JournalSHA, ExpectedReportSHA: snap.ReportSHA, ExpectedCycle: snap.Cycle, ExpectedNonce: snap.Nonce, Now: clock().UTC()}
	plan, err := allowancecollector.PrepareHandoff(in)
	if err != nil {
		return refused
	}
	if args[0] == "prepare" {
		_, err = fmt.Fprintln(out, plan.Hash)
		return err
	}
	if *approved != plan.Hash {
		return refused
	}
	// Old per-Pod terminal ledger evidence is not yet verifiable. Refuse
	// before opening kubeconfig or constructing a client.
	const applyQualified = false
	if !applyQualified {
		return refused
	}
	// No client or kubeconfig is opened before the exact prepared hash matches.
	c, err := factory(*kubeconfig, *kubecontext)
	if err != nil {
		return refused
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if allowancecollector.ApplyHandoff(ctx, c, in, *approved, *backup) != nil {
		return refused
	}
	_, err = fmt.Fprintln(out, plan.Hash)
	return err
}

func readPrivate(path string, limit int64) ([]byte, error) {
	if filepath.Clean(path) != path {
		return nil, refused
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, refused
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, refused
	}
	defer f.Close()
	info, err := f.Stat()
	st, ok := infoSys(info)
	if err != nil || !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > limit {
		return nil, refused
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit || int64(len(raw)) != info.Size() {
		return nil, refused
	}
	return raw, nil
}

func makeClient(path, selected string) (client.Client, error) {
	config, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, refused
	}
	selectedContext := config.Contexts[selected]
	if selectedContext == nil {
		return nil, refused
	}
	cluster := config.Clusters[selectedContext.Cluster]
	auth := config.AuthInfos[selectedContext.AuthInfo]
	if cluster == nil || auth == nil || cluster.ProxyURL != "" || cluster.InsecureSkipTLSVerify || auth.Exec != nil || auth.AuthProvider != nil {
		return nil, refused
	}
	rest, err := clientcmd.NewNonInteractiveClientConfig(*config, selected, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, refused
	}
	rest.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	scheme := runtime.NewScheme()
	if corev1.AddToScheme(scheme) != nil || v1alpha1.AddToScheme(scheme) != nil {
		return nil, refused
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
