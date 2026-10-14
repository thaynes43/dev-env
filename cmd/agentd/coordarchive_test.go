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
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func coordCLIData(t *testing.T) (protocol.CoordArchiveManifest, []byte, []byte) {
	t.Helper()
	m := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "taskclaims", Kind: "TaskClaim",
		APIVersion: protocol.CoordArchiveAPIVersion, Namespace: "dev-env-system", Project: "sample",
		Name: "task-claim." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32),
		UID:  "11111111-1111-1111-1111-111111111111", ResourceVersion: "42", ArchivedBy: "dev-env-operator fixture"}
	object := map[string]any{"apiVersion": m.APIVersion, "kind": m.Kind,
		"metadata": map[string]any{"namespace": m.Namespace, "name": m.Name, "uid": m.UID, "resourceVersion": m.ResourceVersion},
		"spec":     map[string]any{"identity": map[string]any{"project": m.Project}},
		"future":   json.Number("90071992547409931234567890"), "text": "<synthetic>&"}
	canonical, err := protocol.CanonicalJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	m.Object = protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(canonical))}
	packet, err := json.Marshal(protocol.CoordArchivePutRequest{Manifest: m, Object: canonical})
	if err != nil {
		t.Fatal(err)
	}
	return m, canonical, packet
}

func coordCLIRig(t *testing.T) (map[string]string, string) {
	t.Helper()
	home := t.TempDir()
	// Existing tmpfs only: create synthetic temporary files, never mount
	// anything. The real production filesystem-device check remains enabled.
	root, err := os.MkdirTemp("/dev/shm", "dev-env-coord-cli-")
	if err != nil {
		t.Skipf("separate existing local filesystem unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	var ar, hm syscall.Stat_t
	if syscall.Stat(root, &ar) != nil || syscall.Stat(home, &hm) != nil || ar.Dev == hm.Dev {
		t.Skip("local fixture requires two existing filesystem devices")
	}
	old := newCoordArchive
	newCoordArchive = func(s agentd.Settings) agentd.CoordArchive {
		a := old(s)
		a.Root = root
		a.Now = func() time.Time { return time.Date(2026, 10, 14, 7, 0, 0, 0, time.UTC) }
		return a
	}
	t.Cleanup(func() { newCoordArchive = old })
	return map[string]string{"HOME": home, "AGENTD_GH_TOKEN_FILE": filepath.Join(home, "unused-token")}, root
}

func runCoordCLI(args []string, input []byte, env map[string]string) (int, []byte, string) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, bytes.NewReader(input), &stdout, &stderr, func(k string) string { return env[k] }, noRunner{})
	return code, stdout.Bytes(), stderr.String()
}

func coordGetArgs(m protocol.CoordArchiveManifest) []string {
	return []string{"ctl", "coord-archive", "get", "--resource", m.Resource, "--name", m.Name,
		"--uid", m.UID, "--resource-version", m.ResourceVersion, "--sha256", m.Object.SHA256}
}

func TestCoordArchiveCLIRoundTripAndCorruption(t *testing.T) {
	env, root := coordCLIRig(t)
	m, canonical, packet := coordCLIData(t)
	code, out, stderr := runCoordCLI([]string{"ctl", "coord-archive", "put"}, packet, env)
	if code != exitOK || stderr != "" {
		t.Fatalf("put: exit=%d, stderr=%s, response=%s", code, stderr, out)
	}
	var put protocol.CoordArchivePutResult
	if err := json.Unmarshal(out, &put); err != nil || put.Manifest.Object != m.Object || put.GraceStart != nil {
		t.Fatalf("put receipt differs: %v", err)
	}
	args := coordGetArgs(m)
	code, out, stderr = runCoordCLI(args, nil, env)
	var got protocol.CoordArchiveGetResult
	if err := json.Unmarshal(out, &got); err != nil || code != exitOK || stderr != "" || !bytes.Equal(got.Object, canonical) {
		t.Fatalf("get round trip: exit=%d, error=%v, stderr=%s", code, err, stderr)
	}
	sum := sha256.Sum256(got.Object)
	if hex.EncodeToString(sum[:]) != m.Object.SHA256 {
		t.Fatal("round-trip SHA256 differs")
	}
	t.Logf("agentd ctl coord-archive put: exit=0; graceStart=null; sha256=%s", m.Object.SHA256)
	t.Logf("agentd %s: exit=0; identical canonical bytes and digest", strings.Join(args, " "))
	rel, err := protocol.CoordArchiveRevisionDir(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel), "object.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runCoordCLI(args, nil, env)
	var refused protocol.HistoryError
	if err := json.Unmarshal(out, &refused); err != nil || code != exitFailure || refused.Code != protocol.HistoryErrCorrupt || bytes.Contains(out, []byte("\"object\":")) {
		t.Fatalf("corruption refusal: exit=%d, error=%v, response=%s", code, err, out)
	}
	t.Log("same exact get after synthetic corruption: exit=1; error=corrupt; no object returned")
}

func TestCoordArchiveCLIRefusesIncompleteOrUnboundedRequests(t *testing.T) {
	env, root := coordCLIRig(t)
	_, _, packet := coordCLIData(t)
	for _, data := range [][]byte{
		[]byte("{}"), append(append([]byte{}, packet...), packet...),
		bytes.Repeat([]byte(" "), protocol.MaxCoordArchiveRequestBytes+1),
		bytes.Replace(packet, []byte("dev-env-operator fixture"), []byte{0xff}, 1),
	} {
		code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "put"}, data, env)
		if code != exitFailure || !bytes.Contains(out, []byte("\"error\":\"invalid\"")) {
			t.Fatalf("bad put: %d, %s", code, out)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("refused envelope published archive files: %v", err)
		}
	}
	for _, args := range [][]string{
		{"ctl", "coord-archive", "put", "--mark"}, {"ctl", "coord-archive", "list"},
		{"ctl", "coord-archive", "get", "--mount-bypass"},
	} {
		if code, _, _ := runCoordCLI(args, nil, env); code != exitUsage {
			t.Fatalf("unsupported arguments returned %d", code)
		}
	}
	code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "get", "--resource", "taskclaims"}, nil, env)
	if code != exitFailure || !bytes.Contains(out, []byte("\"error\":\"invalid\"")) {
		t.Fatalf("incomplete exact get: %d, %s", code, out)
	}
}

func TestCoordArchiveCLIDefaultRequiresArchiveFilesystem(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, protocol.CoordArchiveDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home, "AGENTD_GH_TOKEN_FILE": filepath.Join(home, "unused")}
	_, _, packet := coordCLIData(t)
	code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "put"}, packet, env)
	if code != exitFailure || !bytes.Contains(out, []byte("\"error\":\"no_archive\"")) {
		t.Fatalf("unmounted directory accepted: %d, %s", code, out)
	}
}
