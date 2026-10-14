package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCoordArchiveCLILookupSelectorsRoundTrip(t *testing.T) {
	env, root := coordCLIRig(t)
	m, canonical, packet := coordCLIData(t)
	if code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "put"}, packet, env); code != exitOK {
		t.Fatalf("put: %d %s", code, out)
	}
	ids := []protocol.CoordArchiveIdentity{{Resource: m.Resource, Project: m.Project, Name: m.Name, UID: m.UID}}
	request, err := protocol.CanonicalJSON(ids)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCoordCLI([]string{"ctl", "coord-archive", "lookup"}, request, env)
	var discovered []protocol.CoordArchiveLookupResult
	if err := json.Unmarshal(out, &discovered); err != nil || code != exitOK || stderr != "" || len(discovered) != 1 {
		t.Fatalf("lookup: exit=%d %v %s", code, err, out)
	}
	item := discovered[0]
	if !item.Holds || item.GraceStart != nil || len(item.Revisions) != 1 || item.CoordArchiveIdentity != ids[0] || bytes.Contains(out, []byte("\"object\":")) {
		t.Fatal("discovery shape differs")
	}
	q := m
	q.ResourceVersion = item.Revisions[0].ResourceVersion
	q.Object.SHA256 = item.Revisions[0].SHA256
	code, out, _ = runCoordCLI(coordGetArgs(q), nil, env)
	var get protocol.CoordArchiveGetResult
	if err := json.Unmarshal(out, &get); err != nil || code != exitOK || !bytes.Equal(get.Object, canonical) {
		t.Fatalf("selector get: %d %v", code, err)
	}
	sum := sha256.Sum256(get.Object)
	if hex.EncodeToString(sum[:]) != item.Revisions[0].SHA256 {
		t.Fatal("selector SHA256 differs")
	}
	t.Logf("stdin put -> stdin lookup -> exact get: exit=0; identical bytes; sha256=%s", item.Revisions[0].SHA256)
	rel, err := protocol.CoordArchiveRevisionDir(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel), "object.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Discovery still reports metadata: it does not attest the object's bytes.
	if code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "lookup"}, request, env); code != exitOK || bytes.Contains(out, []byte("\"object\":")) {
		t.Fatalf("metadata-only lookup: %d %s", code, out)
	}
	code, out, _ = runCoordCLI(coordGetArgs(q), nil, env)
	if code != exitFailure || !bytes.Contains(out, []byte("\"error\":\"corrupt\"")) || bytes.Contains(out, []byte("\"object\":")) {
		t.Fatalf("corrupt get: %d %s", code, out)
	}
	t.Log("lookup reads metadata only; corrupt exact get: exit=1; no object")
}

func TestCoordArchiveCLILookupRefusesMalformedBatch(t *testing.T) {
	env, root := coordCLIRig(t)
	for _, input := range [][]byte{[]byte("null"), []byte("[]"), []byte("{}"), []byte("[{}]"), []byte("[] []"), []byte("[{\"unknown\":true}]"), {'[', '"', 0xff, '"', ']'}, bytes.Repeat([]byte(" "), protocol.MaxCoordArchiveRequestBytes+1)} {
		code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "lookup"}, input, env)
		if code != exitFailure || !bytes.Contains(out, []byte("\"error\":\"invalid\"")) {
			t.Fatalf("invalid lookup: %d %s", code, out)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("bad lookup touched archive")
	}
	if code, _, _ := runCoordCLI([]string{"ctl", "coord-archive", "lookup", "--project", "sample"}, nil, env); code != exitUsage {
		t.Fatal("unsupported option accepted")
	}
}
