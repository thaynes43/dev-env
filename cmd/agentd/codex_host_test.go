package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd"
)

func TestCodexHostDisabledAndSessionRefusal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { panic("disabled read environment") }
	if run(context.Background(), []string{"codex-host", "run"}, nil, &stdout, &stderr, getenv, agentd.ExecRunner{}) != exitOK || stdout.String() != "{\"code\":\"Disabled\",\"idle\":false}\n" {
		t.Fatal("disabled route")
	}
	stdout.Reset()
	getenv = func(key string) string {
		if key == "AGENTD_SESSION" {
			return "{}"
		}
		return ""
	}
	if run(context.Background(), []string{"codex-host", "--enabled", "run"}, nil, &stdout, &stderr, getenv, agentd.ExecRunner{}) != exitUsage {
		t.Fatal("Session accepted as host")
	}
}

func TestCodexHostStatusFailureWritesOneUnknownDocument(t *testing.T) {
	// Missing host identity refuses in preflight before any native invocation.
	code, out, errOut := runArgs([]string{"codex-host", "--enabled", "status"}, map[string]string{"HOME": t.TempDir()})
	if code != exitFailure || errOut != "" {
		t.Fatalf("status failure: code=%d stderr=%q", code, errOut)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(out))
	var status agentd.CodexHostStatus
	if decoder.Decode(&status) != nil || status != (agentd.CodexHostStatus{Code: "Unknown"}) {
		t.Fatal("status failure did not publish fixed Unknown metadata")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		t.Fatal("status failure published more than one JSON document")
	}
}
