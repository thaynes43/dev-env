package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCoordArchiveCLIMarkedLookupGraceTransitions(t *testing.T) {
	env, root := coordCLIRig(t)
	m, object, _ := coordCLIData(t)
	id := protocol.CoordArchiveIdentity{Resource: m.Resource, Project: m.Project, Name: m.Name, UID: m.UID}
	at := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
	event := protocol.CoordArchiveEvent{Format: protocol.CoordArchiveEventFormat, State: "absent", At: at, Rule: "owner-absent", By: "dev-env-operator fixture", Sessions: []protocol.CoordArchiveObservation{{Role: "owner", Namespace: "dev-agents", Name: "synthetic-source", UID: "44444444-4444-4444-4444-444444444444", Observed: "not-found"}}}
	put := func(e *protocol.CoordArchiveEvent, marked bool) {
		t.Helper()
		packet, err := protocol.CanonicalJSON(protocol.CoordArchivePutRequest{Manifest: m, Object: object, Event: e})
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"ctl", "coord-archive", "put"}
		if marked {
			args = append(args, "--mark")
		}
		code, out, stderr := runCoordCLI(args, packet, env)
		if code != exitOK || stderr != "" {
			t.Fatalf("put: %d %s", code, out)
		}
	}
	lookup := func(want *time.Time) {
		t.Helper()
		packet, err := protocol.CanonicalJSON([]protocol.CoordArchiveIdentity{id})
		if err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runCoordCLI([]string{"ctl", "coord-archive", "lookup"}, packet, env)
		var result []protocol.CoordArchiveLookupResult
		if err := json.Unmarshal(out, &result); err != nil || code != exitOK || stderr != "" || len(result) != 1 || !result[0].Holds {
			t.Fatalf("lookup: %d %v %s", code, err, out)
		}
		if (want == nil) != (result[0].GraceStart == nil) || want != nil && !result[0].GraceStart.Equal(*want) {
			t.Fatalf("wrong grace: %s", out)
		}
	}
	mark := func(e protocol.CoordArchiveEvent) {
		t.Helper()
		packet, err := protocol.CanonicalJSON([]protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: e}})
		if err != nil {
			t.Fatal(err)
		}
		code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "mark"}, packet, env)
		if code != exitOK {
			t.Fatalf("mark: %d %s", code, out)
		}
	}
	put(&event, true)
	lookup(&at)
	t.Logf("put --mark -> lookup: graceStart=%s", at.Format(time.RFC3339))
	event.At = at.Add(time.Hour)
	put(&event, true)
	put(nil, false)
	lookup(&at)
	t.Log("repeat marked put/plain put: original grace preserved")
	present := event
	present.State = "present"
	present.At = at.Add(2 * time.Hour)
	present.Sessions = append([]protocol.CoordArchiveObservation{}, event.Sessions...)
	present.Sessions[0].Observed = "present"
	mark(present)
	lookup(nil)
	t.Log("mark present -> lookup: graceStart=null")
	event.At = at.Add(3 * time.Hour)
	put(&event, true)
	lookup(&event.At)
	reset := event
	reset.State = "reset"
	reset.At = at.Add(4 * time.Hour)
	reset.Sessions = []protocol.CoordArchiveObservation{}
	mark(reset)
	lookup(nil)
	t.Log("mark reset -> lookup: graceStart=null")
	event.At = at.Add(5 * time.Hour)
	put(&event, true)
	lookup(&event.At)
	t.Logf("re-mark -> lookup: graceStart=%s", event.At.Format(time.RFC3339))
	rel, err := id.RecordDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel), "events", "00000005-absent.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	packet, err := protocol.CanonicalJSON([]protocol.CoordArchiveIdentity{id})
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "lookup"}, packet, env)
	if code != exitFailure || !bytes.Contains(out, []byte(`"error":"corrupt"`)) || bytes.Contains(out, []byte(`"graceStart":`)) {
		t.Fatalf("unknown journal refusal: %d %s", code, out)
	}
	t.Log("corrupt journal -> lookup: exit1 corrupt, no grace/result")
}

func TestCoordArchiveCLIRefusesInvalidMarkRequests(t *testing.T) {
	env, root := coordCLIRig(t)
	m, object, plain := coordCLIData(t)
	code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "put", "--mark"}, plain, env)
	if code != exitFailure || !bytes.Contains(out, []byte(`"error":"invalid"`)) {
		t.Fatalf("mark without event: %d %s", code, out)
	}
	event := protocol.CoordArchiveEvent{Format: protocol.CoordArchiveEventFormat, State: "reset", At: time.Now().UTC(), Rule: "owner-absent", By: "fixture", Sessions: []protocol.CoordArchiveObservation{}}
	packet, err := protocol.CanonicalJSON(protocol.CoordArchivePutRequest{Manifest: m, Object: object, Event: &event})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"ctl", "coord-archive", "put"}, {"ctl", "coord-archive", "put", "--mark"}} {
		if code, out, _ := runCoordCLI(args, packet, env); code != exitFailure || !bytes.Contains(out, []byte(`"error":"invalid"`)) {
			t.Fatalf("invalid put event: %d %s", code, out)
		}
	}
	for _, input := range [][]byte{[]byte("[]"), []byte("null"), []byte("[{}]"), []byte("[] []"), []byte("[{\"unknown\":true}]"), bytes.Repeat([]byte(" "), protocol.MaxCoordArchiveRequestBytes+1)} {
		if code, out, _ := runCoordCLI([]string{"ctl", "coord-archive", "mark"}, input, env); code != exitFailure || !bytes.Contains(out, []byte(`"error":"invalid"`)) {
			t.Fatalf("invalid mark: %d %s", code, out)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("bad requests wrote state")
	}
}
