package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// worstCaseCoordClaim is one canonical task claim at the archive object ceiling.
// The pad is ASCII, so the canonical size moves one byte per added character.
func worstCaseCoordClaim(t *testing.T) (protocol.CoordArchiveManifest, []byte, []byte) {
	t.Helper()
	m := protocol.CoordArchiveManifest{
		Format: protocol.CoordArchiveFormat, Resource: "taskclaims", Kind: "TaskClaim",
		APIVersion: protocol.CoordArchiveAPIVersion, Namespace: "dev-env-system", Project: "sample",
		Name:            "task-claim." + strings.Repeat("c", 32) + "." + strings.Repeat("d", 32),
		UID:             "44444444-4444-4444-8444-444444444444",
		ResourceVersion: "42", ArchivedBy: "dev-env-operator fixture",
	}
	spec := map[string]any{"identity": map[string]any{"project": m.Project}, "text": ""}
	object := map[string]any{
		"apiVersion": m.APIVersion, "kind": m.Kind,
		"metadata": map[string]any{"namespace": m.Namespace, "name": m.Name, "uid": m.UID, "resourceVersion": m.ResourceVersion},
		"spec":     spec,
	}
	canonical, err := protocol.CanonicalJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	pad := protocol.MaxCoordArchiveObjectBytes - len(canonical)
	if pad < 0 {
		t.Fatalf("empty claim is already %d bytes", len(canonical))
	}
	spec["text"] = strings.Repeat("a", pad)
	canonical, err = protocol.CanonicalJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != protocol.MaxCoordArchiveObjectBytes {
		delta := protocol.MaxCoordArchiveObjectBytes - len(canonical)
		spec["text"] = strings.Repeat("a", pad+delta)
		canonical, err = protocol.CanonicalJSON(object)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(canonical) != protocol.MaxCoordArchiveObjectBytes {
		t.Fatalf("canonical object is %d bytes, ceiling %d", len(canonical), protocol.MaxCoordArchiveObjectBytes)
	}
	sum := sha256.Sum256(canonical)
	m.Object = protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(canonical))}
	packet, err := protocol.CanonicalJSON(protocol.CoordArchivePutRequest{Manifest: m, Object: canonical})
	if err != nil {
		t.Fatal(err)
	}
	if len(packet) > protocol.MaxCoordArchiveRequestBytes {
		t.Fatalf("put request %d exceeds %d", len(packet), protocol.MaxCoordArchiveRequestBytes)
	}
	return m, canonical, packet
}

func TestCoordArchiveWorstCaseGetRoundTrip(t *testing.T) {
	env, _ := coordCLIRig(t)
	m, canonical, packet := worstCaseCoordClaim(t)
	code, _, stderr := runCoordCLI([]string{"ctl", "coord-archive", "put"}, packet, env)
	if code != exitOK || stderr != "" {
		t.Fatalf("put: exit=%d stderr=%s", code, stderr)
	}
	var out []byte
	code, out, stderr = runCoordCLI(coordGetArgs(m), nil, env)
	var got protocol.CoordArchiveGetResult
	if err := json.Unmarshal(out, &got); err != nil || code != exitOK || stderr != "" {
		t.Fatalf("get: exit=%d err=%v stderr=%s", code, err, stderr)
	}
	if got.Manifest.Object.SHA256 != m.Object.SHA256 || got.Manifest.Object.Bytes != int64(protocol.MaxCoordArchiveObjectBytes) || !bytes.Equal(got.Object, canonical) {
		t.Fatalf("worst-case get mismatch: sha %s bytes %d", got.Manifest.Object.SHA256, len(got.Object))
	}
}

// TestCoordArchiveWorstCasePrepare writes one ceiling-sized revision for the
// shelf-limit measurement. The measurement container copies that directory
// onto its own tmpfs and runs agentd; this test does not measure memory.
func TestCoordArchiveWorstCasePrepare(t *testing.T) {
	root := os.Getenv("DEV_ENV_COORD_PREPARE_ROOT")
	home := os.Getenv("DEV_ENV_COORD_PREPARE_HOME")
	receiptPath := os.Getenv("DEV_ENV_COORD_PREPARE_RECEIPT")
	if root == "" && home == "" && receiptPath == "" {
		t.Skip("set DEV_ENV_COORD_PREPARE_ROOT, DEV_ENV_COORD_PREPARE_HOME and DEV_ENV_COORD_PREPARE_RECEIPT")
	}
	if root == "" || home == "" || receiptPath == "" {
		t.Fatal("prepare requires root, home and receipt path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	a := agentd.NewCoordArchive(agentd.Settings{Home: home})
	a.Root = root
	if err := a.CheckMounted(); err != nil {
		t.Fatal(err)
	}
	m, canonical, _ := worstCaseCoordClaim(t)
	if _, err := a.Put(m, bytes.NewReader(canonical)); err != nil {
		t.Fatal(err)
	}
	receipt := map[string]string{
		"resource": m.Resource, "name": m.Name, "uid": m.UID,
		"resourceVersion": m.ResourceVersion, "sha256": m.Object.SHA256,
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestConcurrentMeasureWaiterPinsTheWorstCaseGet keeps the offline four-get
// waiter on the same claim identity Prepare writes. The waiter is build-ignored
// and is not an agentd command.
func TestConcurrentMeasureWaiterPinsTheWorstCaseGet(t *testing.T) {
	m, canonical, _ := worstCaseCoordClaim(t)
	if m.Resource != "taskclaims" || m.ResourceVersion != "42" || m.UID != "44444444-4444-4444-8444-444444444444" {
		t.Fatalf("worst-case identity changed: %+v", m)
	}
	if m.Name != "task-claim."+strings.Repeat("c", 32)+"."+strings.Repeat("d", 32) {
		t.Fatalf("name %q", m.Name)
	}
	if len(canonical) != protocol.MaxCoordArchiveObjectBytes {
		t.Fatalf("canonical %d", len(canonical))
	}
	src, err := os.ReadFile("coordarchive_shelf_measure_concurrent_wait.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"//go:build ignore",
		`strings.Repeat("c", 32)`,
		`strings.Repeat("d", 32)`,
		m.UID,
		"268435456",
		"rescue/measure-old/20260801-0000",
		`{"olderThan":"720h","keep":[]}`,
		"0.25",
	} {
		if !bytes.Contains(src, []byte(needle)) {
			t.Fatalf("waiter source missing %q", needle)
		}
	}
}
