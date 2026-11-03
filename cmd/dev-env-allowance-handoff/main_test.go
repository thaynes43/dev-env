package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/allowancecollector"
	"github.com/thaynes43/dev-env/internal/apiserver"
	pa "github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestRefusalNeverConstructsClientOrEchoesInput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "snapshot")
	if err := os.WriteFile(input, []byte(`{"secret-canary":"do-not-echo"}`), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	factory := func(string, string) (client.Client, error) { calls++; t.Fatal("unexpected client"); return nil, nil }
	base := []string{"--input", input, "--kubeconfig", "/fixture/kubeconfig", "--context", "fixture", "--namespace", "dev-env-system", "--slot", "dev-env-provider-allowances"}
	for _, mode := range []string{"prepare", "apply"} {
		args := append([]string{mode}, base...)
		if mode == "apply" {
			args = append(args, "--approved-hash", strings.Repeat("a", 64), "--backup-dir", dir)
		}
		var out bytes.Buffer
		if err := run(args, &out, time.Now, factory); err == nil || out.Len() != 0 || strings.Contains(err.Error(), "canary") {
			t.Fatal("refusal leaked input or accepted malformed snapshot")
		}
	}
	if calls != 0 {
		t.Fatal("client constructed")
	}
}
func TestPrivateSnapshotPermissionsAndSymlink(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "input")
	if err := os.WriteFile(p, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(p, 100); err == nil {
		t.Fatal("world-readable input accepted")
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(p, 100); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(link, 100); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestKubeconfigRefusesExecProxyAndInsecureTransport(t *testing.T) {
	for name, extra := range map[string]string{
		"exec":     "    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: fixture\n      interactiveMode: Never\n",
		"proxy":    "    proxy-url: http://127.0.0.1:8123\n",
		"insecure": "    insecure-skip-tls-verify: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			clusterExtra, authExtra := "", ""
			if name == "exec" {
				authExtra = extra
			} else {
				clusterExtra = extra
			}
			raw := "apiVersion: v1\nkind: Config\nclusters:\n- name: fixture\n  cluster:\n    server: https://127.0.0.1:6443\n" + clusterExtra + "users:\n- name: fixture\n  user:\n" + authExtra + "    token: synthetic\ncontexts:\n- name: fixture\n  context:\n    cluster: fixture\n    user: fixture\ncurrent-context: fixture\n"
			p := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := makeClient(p, "fixture"); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

func syntheticSHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Exercise the serialized CLI input and real prepare/apply logic with only
// synthetic metadata and an in-memory API. No kubeconfig is opened.
func TestCLIQualifiedSyntheticPrepareAndApply(t *testing.T) {
	now := time.Now().UTC()
	deadline := time.Date(2026, 11, 4, 15, 33, 23, 0, time.UTC)
	if !now.Before(deadline) || now.Before(deadline.Add(-6*24*time.Hour)) {
		t.Skip("outside one-time approval window")
	}
	const sessionUID = "11111111-1111-1111-1111-111111111111"
	const oldPodUID = "22222222-2222-2222-2222-222222222222"
	const slotUID = "33333333-3333-3333-3333-333333333333"
	const newPodUID = "44444444-4444-4444-4444-444444444444"
	const nonce = "55555555-5555-5555-5555-555555555555"
	const homeUID = "66666666-6666-6666-6666-666666666666"
	host := allowancecollector.Host{Name: "fixture-owner", SessionUID: sessionUID, Generation: 1, PodUID: oldPodUID, ServiceAccount: "dev-env-workbench", Image: "ghcr.io/thaynes43/dev-env:fixture@sha256:" + strings.Repeat("a", 64)}
	old := allowancecollector.Config{Version: 1, Host: host, SlotUID: slotUID, ValidUntil: now.Add(-time.Hour).Format(time.RFC3339Nano), CadenceSeconds: 300, BackoffSeconds: 600}
	next := old
	next.Host.PodUID, next.Host.Generation = newPodUID, 2
	next.ValidUntil = deadline.Format(time.RFC3339Nano)
	oldRaw, _ := json.Marshal(old)
	nextRaw, _ := json.Marshal(next)
	contexts, sources, metrics, accounts := []any{}, []any{}, []any{}, []any{}
	for _, item := range []struct{ provider, kind, version, capability string }{
		{"openai", "native_rpc", pa.CodexRPCSourceVersion, pa.CodexRPCCapability},
		{"anthropic", "native_rpc", pa.ClaudeSDKSourceVersion, "claude-sdk:usage-report"},
		{"cursor", "native_display", pa.CursorCLISourceVersion, pa.CursorCLICapability},
	} {
		account := item.provider + "-email-sha256:" + strings.Repeat("a", 64)
		producer := "fixture-" + item.provider
		var workspace any
		if item.provider == "anthropic" {
			workspace = "anthropic-org-sha256:" + strings.Repeat("b", 64)
		}
		contexts = append(contexts, map[string]any{"provider": item.provider, "producerRef": producer, "producerIncarnation": "logical-stream", "bindingGeneration": 1, "accountRef": account, "workspaceRef": workspace, "bindingState": "verified", "sourceKind": item.kind, "sourceVersion": item.version})
		sources = append(sources, map[string]any{"provider": item.provider, "kind": item.kind, "version": item.version, "capabilityRef": item.capability, "allowNormalizedMeasures": true, "allowDerivations": true, "directFields": map[string]any{}})
		metrics = append(metrics, map[string]any{"validationRef": "fixture:" + item.provider, "provider": item.provider, "sourceKind": item.kind, "sourceVersion": item.version, "category": "included_quota", "unit": "percent", "direction": "used", "windows": []string{"fixture-window"}, "currency": nil, "min": "0", "max": "100", "allowOverLimit": false, "allowUncapped": false, "complementOf": "", "historicalOnly": true, "modelScope": ""})
		accounts = append(accounts, map[string]any{"accountRef": account, "workspaceRef": workspace, "generation": 1, "producerRef": producer, "producerIncarnation": "logical-stream", "accountScopeOnly": workspace == nil})
	}
	trustRaw, _ := json.Marshal(map[string]any{"maxAgeSeconds": 1800, "contexts": contexts, "registry": map[string]any{"sources": sources, "metrics": metrics, "accounts": accounts, "entitlements": []any{}, "projections": []any{}}})
	trust, err := pa.ParseAggregateTrust(trustRaw)
	if err != nil {
		t.Fatal("trust fixture rejected")
	}
	fingerprintConfig := old
	fingerprintConfig.ValidUntil = ""
	parts := pa.RegistryParts{Sources: trust.Registry.Sources(), Metrics: trust.Registry.Metrics(), Accounts: trust.Registry.Accounts(), Entitlements: trust.Registry.Entitlements(), Projections: trust.Registry.Projections()}
	canonical, _ := json.Marshal(struct {
		Config   allowancecollector.Config
		Contexts []pa.TrustedContext
		Registry pa.RegistryParts
		MaxAge   int64
	}{fingerprintConfig, trust.Contexts, parts, trust.MaxAge})
	helper := strings.Repeat("c", 64)
	report := []byte{}
	journal, _ := json.Marshal(map[string]any{"version": 1, "fingerprint": syntheticSHA(canonical), "helperSHA256": helper, "cycle": 56, "nonce": nonce, "phase": "idle", "startedAt": now.Add(-time.Minute).Format(time.RFC3339Nano), "notBefore": now.Add(time.Minute).Format(time.RFC3339Nano), "reportSHA256": syntheticSHA(report), "captures": []any{}})
	yes := true
	owner := metav1.OwnerReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: host.Name, UID: types.UID(sessionUID), Controller: &yes}
	oldPod := corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Namespace: allowancecollector.Namespace, Name: host.Name, UID: types.UID(oldPodUID), ResourceVersion: "42", OwnerReferences: []metav1.OwnerReference{owner}}, Spec: corev1.PodSpec{ServiceAccountName: host.ServiceAccount, Containers: []corev1.Container{{Name: "agent", Image: host.Image}}}, Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}}
	oldPodRaw, _ := json.Marshal(oldPod)
	session := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: allowancecollector.Namespace, Name: host.Name, UID: types.UID(sessionUID), Generation: 2}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, OperatingMode: v1alpha1.OperatingModeRunning}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning, PodName: host.Name, Agent: &v1alpha1.AgentStatus{LastHeartbeat: &metav1.Time{Time: now.Add(-time.Second)}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: allowancecollector.Namespace, Name: host.Name, UID: types.UID(newPodUID), OwnerReferences: []metav1.OwnerReference{owner}}, Spec: corev1.PodSpec{ServiceAccountName: host.ServiceAccount, Containers: []corev1.Container{{Name: "agent", Image: host.Image, VolumeMounts: []corev1.VolumeMount{{Name: "home", MountPath: "/home/dev"}, {Name: "claude", MountPath: "/opt/dev-env/claude-access", ReadOnly: true}, {Name: "codex", MountPath: "/opt/dev-env/codex-access", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "home-" + host.Name}}}, {Name: "claude", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "dev-env-claude-live"}}}, {Name: "codex", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "dev-env-codex-live"}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: allowancecollector.Namespace, Name: "home-" + host.Name, UID: types.UID(homeUID), OwnerReferences: []metav1.OwnerReference{owner}}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: apiserver.AggregateAllowanceNamespace, Name: apiserver.AggregateAllowanceName, UID: types.UID(slotUID), ResourceVersion: "7"}, Data: map[string]string{allowancecollector.StateKey: string(journal), apiserver.AggregateAllowanceDataKey: string(report), "unrelated": "preserved"}}
	scheme := runtime.NewScheme()
	if corev1.AddToScheme(scheme) != nil || v1alpha1.AddToScheme(scheme) != nil {
		t.Fatal("scheme")
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session, pod, pvc, cm).Build()
	snap := snapshot{OldConfig: oldRaw, NewConfig: nextRaw, Trust: trustRaw, PublisherUID: sessionUID, PublisherSA: host.ServiceAccount, SlotUID: slotUID, ResourceVersion: "7", HomeUID: homeUID, Journal: journal, Report: report, EvictedPod: oldPodRaw, HelperSHA: helper, JournalSHA: syntheticSHA(journal), ReportSHA: syntheticSHA(report), Cycle: 56, Nonce: nonce}
	if _, err := allowancecollector.ParseConfig(oldRaw, trust, sessionUID, host.ServiceAccount, now); err != nil {
		t.Fatal("old config fixture rejected")
	}
	if _, err := allowancecollector.ParseConfig(nextRaw, trust, sessionUID, host.ServiceAccount, now); err != nil {
		t.Fatal("new config fixture rejected")
	}
	plan, err := allowancecollector.PrepareHandoff(allowancecollector.HandoffInput{OldConfig: oldRaw, NewConfig: nextRaw, Trust: trust, PublisherUID: sessionUID, PublisherSA: host.ServiceAccount, SlotUID: slotUID, ResourceVersion: "7", HomeUID: homeUID, KubeconfigPath: "/fixture/kubeconfig", KubeContext: "fixture", Journal: journal, Report: report, EvictedPod: oldPodRaw, ExpectedHelperSHA: helper, ExpectedJournalSHA: syntheticSHA(journal), ExpectedReportSHA: syntheticSHA(report), ExpectedCycle: 56, ExpectedNonce: nonce, Now: now})
	if err != nil {
		t.Fatal("direct prepare fixture rejected")
	}
	raw, _ := json.Marshal(snap)
	dir := t.TempDir()
	input := filepath.Join(dir, "snapshot")
	backup := filepath.Join(dir, "backup")
	if os.WriteFile(input, raw, 0600) != nil || os.Mkdir(backup, 0700) != nil {
		t.Fatal("private fixture setup")
	}
	loaded, e := readPrivate(input, 512<<10)
	if e != nil {
		t.Fatal("private read refused")
	}
	if !pa.ExactObjectKeys(loaded, snapshotKeys) {
		t.Fatal("snapshot keys refused")
	}
	var round snapshot
	if json.Unmarshal(loaded, &round) != nil {
		t.Fatal("snapshot decode refused")
	}
	parsed, e := pa.ParseAggregateTrust(round.Trust)
	if e != nil {
		t.Fatal("snapshot trust refused")
	}
	if _, e = allowancecollector.PrepareHandoff(allowancecollector.HandoffInput{OldConfig: round.OldConfig, NewConfig: round.NewConfig, Trust: parsed, PublisherUID: round.PublisherUID, PublisherSA: round.PublisherSA, SlotUID: round.SlotUID, ResourceVersion: round.ResourceVersion, HomeUID: round.HomeUID, KubeconfigPath: "/fixture/kubeconfig", KubeContext: "fixture", Journal: round.Journal, Report: round.Report, EvictedPod: round.EvictedPod, ExpectedHelperSHA: round.HelperSHA, ExpectedJournalSHA: round.JournalSHA, ExpectedReportSHA: round.ReportSHA, ExpectedCycle: round.Cycle, ExpectedNonce: round.Nonce, Now: time.Now().UTC()}); e != nil {
		t.Fatal("roundtrip prepare refused")
	}
	args := []string{"--input", input, "--kubeconfig", "/fixture/kubeconfig", "--context", "fixture", "--namespace", apiserver.AggregateAllowanceNamespace, "--slot", apiserver.AggregateAllowanceName}
	called := 0
	factory := func(path, selected string) (client.Client, error) {
		if path != "/fixture/kubeconfig" || selected != "fixture" {
			t.Fatal("wrong selection")
		}
		called++
		return c, nil
	}
	for label, bad := range map[string][]byte{
		"duplicate":    append([]byte(`{"cycle":56,`), raw[1:]...),
		"unknown":      append([]byte(`{"unknown":1,`), raw[1:]...),
		"invalid utf8": append([]byte{0xff}, raw...),
		"trailing":     append(append([]byte(nil), raw...), []byte(`{}`)...),
		"oversized":    bytes.Repeat([]byte("x"), (512<<10)+1),
	} {
		t.Run(label, func(t *testing.T) {
			if os.WriteFile(input, bad, 0600) != nil {
				t.Fatal("write malformed fixture")
			}
			var denied bytes.Buffer
			if run(append([]string{"prepare"}, args...), &denied, time.Now, factory) == nil || denied.Len() != 0 || called != 0 {
				t.Fatal("malformed snapshot accepted")
			}
		})
	}
	if os.WriteFile(input, raw, 0600) != nil {
		t.Fatal("restore fixture")
	}
	var prepared bytes.Buffer
	if err := run(append([]string{"prepare"}, args...), &prepared, time.Now, factory); err != nil || called != 0 {
		t.Fatal("prepare failed")
	}
	hash := strings.TrimSpace(prepared.String())
	var applied bytes.Buffer
	applyArgs := append(append([]string{"apply"}, args...), "--approved-hash", hash, "--backup-dir", backup)
	if err := run(applyArgs, &applied, time.Now, factory); err != nil || called != 1 || strings.TrimSpace(applied.String()) != hash {
		t.Fatal("apply failed")
	}
	var after corev1.ConfigMap
	if c.Get(context.Background(), types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, &after) != nil || after.Data[allowancecollector.StateKey] != string(plan.NewJournal) || after.Data[apiserver.AggregateAllowanceDataKey] != string(report) || after.Data["unrelated"] != "preserved" {
		t.Fatal("unexpected CAS result")
	}
	backupRaw, err := os.ReadFile(filepath.Join(backup, hash+".json"))
	if err != nil {
		t.Fatal("backup missing")
	}
	var saved struct{ OldConfig, Journal, Report []byte }
	if json.Unmarshal(backupRaw, &saved) != nil || !bytes.Equal(saved.OldConfig, oldRaw) || !bytes.Equal(saved.Journal, journal) || !bytes.Equal(saved.Report, report) {
		t.Fatal("backup bytes differ")
	}
}

type snapshot = allowancecollector.HandoffSnapshot

var snapshotKeys = []string{"oldConfig", "newConfig", "trust", "publisherUID", "publisherSA", "slotUID", "resourceVersion", "homeUID", "journal", "report", "evictedPod", "helperSHA256", "journalSHA256", "reportSHA256", "cycle", "nonce"}
