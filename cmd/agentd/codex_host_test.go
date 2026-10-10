package main

import (
	"bytes"
	"context"
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
