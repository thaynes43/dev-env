package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCoordArchiveCLIOptionalSelectorsAndCorruption(t *testing.T) {
	env, root := coordCLIRig(t)
	m, canonical, _ := coordCLIData(t)
	clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	factory := newCoordArchive
	newCoordArchive = func(s agentd.Settings) agentd.CoordArchive {
		a := factory(s)
		a.Now = func() time.Time { return clock }
		return a
	}
	t.Cleanup(func() { newCoordArchive = factory })
	put := func(uid, rv string) (protocol.CoordArchiveManifest, []byte) {
		t.Helper()
		var object map[string]any
		dec := json.NewDecoder(bytes.NewReader(canonical))
		dec.UseNumber()
		if err := dec.Decode(&object); err != nil {
			t.Fatal(err)
		}
		next := m
		next.UID = uid
		next.ResourceVersion = rv
		metadata := object["metadata"].(map[string]any)
		metadata["uid"] = uid
		metadata["resourceVersion"] = rv
		data, err := protocol.CanonicalJSON(object)
		if err != nil {
			t.Fatal(err)
		}
		// Rebind exact digest through the existing synthetic helper's canonical form.
		sum := coordSelectorDigest(data)
		next.Object = protocol.CoordArchiveObjectDigest{SHA256: sum, Bytes: int64(len(data))}
		packet, err := protocol.CanonicalJSON(protocol.CoordArchivePutRequest{Manifest: next, Object: data})
		if err != nil {
			t.Fatal(err)
		}
		code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "put"}, packet, env)
		if code != exitOK {
			t.Fatalf("put: %d %s", code, out)
		}
		return next, data
	}
	old, oldData := put("ffffffff-ffff-ffff-ffff-ffffffffffff", "zzz-old")
	clock = clock.Add(time.Hour)
	fresh, freshData := put(m.UID, "zzz-first")
	clock = clock.Add(time.Hour)
	oldLatest, oldLatestData := put(old.UID, "zzz-recapture")
	get := func(extra []string, want protocol.CoordArchiveManifest, data []byte) {
		t.Helper()
		args := append([]string{"ctl", "coord-archive", "get", "--resource", m.Resource, "--name", m.Name}, extra...)
		code, out, stderr := runCoordCLI(args, nil, env)
		var got protocol.CoordArchiveGetResult
		if err := json.Unmarshal(out, &got); err != nil || code != exitOK || stderr != "" || got.Manifest.CoordArchiveManifest != want || !bytes.Equal(got.Object, data) {
			t.Fatalf("get: %d %v %s", code, err, out)
		}
	}
	get(nil, fresh, freshData) // Late recapture of old UID must not reorder UID births.
	clock = clock.Add(time.Hour)
	latest, latestData := put(fresh.UID, "aaa-latest")
	get(nil, latest, latestData)
	get([]string{"--uid", old.UID}, oldLatest, oldLatestData)
	get([]string{"--resource-version", fresh.ResourceVersion, "--sha256", fresh.Object.SHA256}, fresh, freshData)
	get([]string{"--uid", old.UID, "--resource-version", old.ResourceVersion, "--sha256", old.Object.SHA256}, old, oldData)
	t.Log("omitted UID uses first-publication time; omitted revision uses recorded archivedAt; opaque UID/version lexical order ignored")
	t.Logf("selected object identical bytes/fullSHA256=%s", latest.Object.SHA256)
	rel, err := protocol.CoordArchiveRevisionDir(latest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel), "object.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "get", "--resource", m.Resource, "--name", m.Name}, nil, env)
	if code != exitFailure || !bytes.Contains(out, []byte(`"error":"corrupt"`)) || bytes.Contains(out, []byte(`"object":`)) {
		t.Fatalf("corrupt default get: %d %s", code, out)
	}
	t.Log("corrupt selected latest: exit1 corrupt; no object and no fallback")
}

func coordSelectorDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestCoordArchiveCLIRequiresPairedRevisionSelector(t *testing.T) {
	env, root := coordCLIRig(t)
	m, _, _ := coordCLIData(t)
	for _, extra := range [][]string{{"--resource-version", "42"}, {"--sha256", m.Object.SHA256}, {"--uid", "../escape"}} {
		args := append([]string{"ctl", "coord-archive", "get", "--resource", m.Resource, "--name", m.Name}, extra...)
		code, out, _ := runCoordCLI(args, nil, env)
		if code != exitFailure || !bytes.Contains(out, []byte(`"error":"invalid"`)) {
			t.Fatalf("invalid selector: %d %s", code, out)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid selector touched archive")
	}
}
