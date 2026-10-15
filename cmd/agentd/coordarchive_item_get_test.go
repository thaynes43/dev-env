package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// This test was first run against the accepted base, where get-item was an
// unknown subcommand. It keeps the valid-object/legacy-get control alongside
// the new transport assertion.
func TestCoordArchiveCLIGetItemTransport(t *testing.T) {
	env, _ := coordCLIRig(t)
	m, object, packet := coordCLIData(t)
	if code, out, stderr := runCoordCLI([]string{"ctl", "coord-archive", "put"}, packet, env); code != exitOK || stderr != "" {
		t.Fatalf("put: exit=%d stderr=%q output=%s", code, stderr, out)
	}
	code, out, stderr := runCoordCLI(coordGetArgs(m), nil, env)
	var legacy protocol.CoordArchiveGetResult
	if code != exitOK || stderr != "" || json.Unmarshal(out, &legacy) != nil || !bytes.Equal(legacy.Object, object) {
		t.Fatalf("legacy get control: exit=%d stderr=%q output=%s", code, stderr, out)
	}
	code, out, stderr = runCoordCLI([]string{"ctl", "coord-archive", "get-item", "--resource", m.Resource, "--name", m.Name}, []byte("not JSON and deliberately ignored"), env)
	var item protocol.CoordArchiveGetResult
	if code != exitOK || stderr != "" || json.Unmarshal(out, &item) != nil || item.Manifest.UID != m.UID || !bytes.Equal(item.Object, object) {
		t.Fatalf("get-item: exit=%d stderr=%q output=%s", code, stderr, out)
	}
	for _, extra := range [][]string{{"--resource-version", m.ResourceVersion}, {"--sha256", m.Object.SHA256}, {"--resource-version", m.ResourceVersion, "--sha256", m.Object.SHA256}} {
		args := append([]string{"ctl", "coord-archive", "get-item", "--resource", m.Resource, "--name", m.Name}, extra...)
		if code, out, stderr := runCoordCLI(args, nil, env); code != exitUsage || len(out) != 0 || stderr == "" {
			t.Fatalf("item pin was not a usage refusal: exit=%d stderr=%q output=%s", code, stderr, out)
		}
	}
	if code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "get-item", "--resource", m.Resource}, nil, env); code != exitFailure || !bytes.Contains(out, []byte(`"error":"invalid"`)) {
		t.Fatalf("incomplete item identity: exit=%d output=%s", code, out)
	}
}

func TestCoordArchiveCLIGetItemSelectsFirstUIDReceiptAndRefusesDamage(t *testing.T) {
	env, root := coordCLIRig(t)
	m, raw, _ := coordCLIData(t)
	clock := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	factory := newCoordArchive
	newCoordArchive = func(s agentd.Settings) agentd.CoordArchive {
		a := factory(s)
		a.Now = func() time.Time { return clock }
		return a
	}
	t.Cleanup(func() { newCoordArchive = factory })
	put := func(uid, rv string) (protocol.CoordArchiveManifest, []byte, protocol.CoordArchiveStoredManifest) {
		t.Helper()
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		next := m
		next.UID, next.ResourceVersion = uid, rv
		metadata := object["metadata"].(map[string]any)
		metadata["uid"], metadata["resourceVersion"] = uid, rv
		data, err := protocol.CanonicalJSON(object)
		if err != nil {
			t.Fatal(err)
		}
		next.Object = protocol.CoordArchiveObjectDigest{SHA256: coordSelectorDigest(data), Bytes: int64(len(data))}
		packet, err := protocol.CanonicalJSON(protocol.CoordArchivePutRequest{Manifest: next, Object: data})
		if err != nil {
			t.Fatal(err)
		}
		code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "put"}, packet, env)
		var result protocol.CoordArchivePutResult
		if code != exitOK || json.Unmarshal(out, &result) != nil {
			t.Fatalf("put: exit=%d output=%s", code, out)
		}
		return next, data, result.Manifest
	}
	old, _, oldFirst := put("ffffffff-ffff-ffff-ffff-ffffffffffff", "old")
	clock = clock.Add(time.Hour)
	fresh, _, freshFirst := put(m.UID, "fresh")
	clock = clock.Add(time.Hour)
	oldLatest, oldRaw, _ := put(old.UID, "old-latest")
	clock = clock.Add(time.Hour)
	freshLatest, freshRaw, _ := put(fresh.UID, "fresh-latest")
	getItem := func(extra []string, want protocol.CoordArchiveManifest, wantRaw []byte, receipt time.Time) {
		t.Helper()
		args := append([]string{"ctl", "coord-archive", "get-item", "--resource", m.Resource, "--name", m.Name}, extra...)
		code, out, _ := runCoordCLI(args, nil, env)
		var got protocol.CoordArchiveGetResult
		if code != exitOK || json.Unmarshal(out, &got) != nil || got.Manifest.CoordArchiveManifest != want || !bytes.Equal(got.Object, wantRaw) || !got.Manifest.ArchivedAt.Equal(receipt) || got.Manifest.ArchivedAt.Location() != time.UTC {
			t.Fatalf("get-item selection: exit=%d output=%s", code, out)
		}
	}
	getItem(nil, freshLatest, freshRaw, freshFirst.ArchivedAt)
	getItem([]string{"--uid", old.UID}, oldLatest, oldRaw, oldFirst.ArchivedAt)
	code, out, _ := runCoordCLI(coordGetArgs(oldLatest), nil, env)
	var legacy protocol.CoordArchiveGetResult
	if code != exitOK || json.Unmarshal(out, &legacy) != nil || !legacy.Manifest.ArchivedAt.Equal(clock.Add(-time.Hour)) || !bytes.Equal(legacy.Object, oldRaw) {
		t.Fatalf("legacy exact recovery changed: exit=%d output=%s", code, out)
	}
	latestDir, err := protocol.CoordArchiveRevisionDir(freshLatest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(latestDir), "object.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runCoordCLI([]string{"ctl", "coord-archive", "get-item", "--resource", m.Resource, "--name", m.Name, "--uid", fresh.UID}, nil, env)
	if code != exitFailure || !bytes.Contains(out, []byte(`"error":"corrupt"`)) || bytes.Contains(out, []byte(`"object":`)) {
		t.Fatalf("corrupt latest did not refuse item: exit=%d output=%s", code, out)
	}
	tied, tiedRaw, _ := put(fresh.UID, "tied")
	code, out, _ = runCoordCLI([]string{"ctl", "coord-archive", "get-item", "--resource", m.Resource, "--name", m.Name, "--uid", fresh.UID}, nil, env)
	if code != exitFailure || !bytes.Contains(out, []byte(`"error":"ambiguous"`)) || bytes.Contains(out, []byte(`"object":`)) || bytes.Contains(out, []byte("revision/hash")) {
		t.Fatalf("tied item did not refuse safely: exit=%d output=%s", code, out)
	}
	code, out, _ = runCoordCLI([]string{"ctl", "coord-archive", "get", "--resource", m.Resource, "--name", m.Name, "--uid", fresh.UID}, nil, env)
	if code != exitFailure || !bytes.Contains(out, []byte("supply revision/hash")) {
		t.Fatalf("legacy ambiguity advice changed: exit=%d output=%s", code, out)
	}
	code, out, _ = runCoordCLI(coordGetArgs(tied), nil, env)
	if code != exitOK || json.Unmarshal(out, &legacy) != nil || !bytes.Equal(legacy.Object, tiedRaw) {
		t.Fatalf("legacy pinned tied recovery changed: exit=%d output=%s", code, out)
	}
	firstIndex := filepath.Join(root, "v1", m.Resource, m.Project, "index", "all", freshFirst.ArchivedAt.Format("2006-01"), freshFirst.ArchivedAt.Format("20060102T150405.000000000Z")+"-"+fresh.UID+".json")
	if err := os.Remove(firstIndex); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runCoordCLI([]string{"ctl", "coord-archive", "get-item", "--resource", m.Resource, "--name", m.Name, "--uid", fresh.UID}, nil, env)
	if code != exitFailure || !bytes.Contains(out, []byte(`"error":"corrupt"`)) || bytes.Contains(out, []byte(`"object":`)) {
		t.Fatalf("missing birth index did not refuse item: exit=%d output=%s", code, out)
	}
	code, out, _ = runCoordCLI(coordGetArgs(tied), nil, env)
	if code != exitOK || json.Unmarshal(out, &legacy) != nil || !bytes.Equal(legacy.Object, tiedRaw) {
		t.Fatalf("missing birth changed legacy pinned recovery: exit=%d output=%s", code, out)
	}
	if err := os.WriteFile(firstIndex, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runCoordCLI([]string{"ctl", "coord-archive", "get-item", "--resource", m.Resource, "--name", m.Name, "--uid", fresh.UID}, nil, env)
	if code != exitFailure || !bytes.Contains(out, []byte(`"error":"corrupt"`)) || bytes.Contains(out, []byte(`"object":`)) {
		t.Fatalf("damaged birth index did not refuse item: exit=%d output=%s", code, out)
	}
}
