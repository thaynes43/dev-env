package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCodexRCCommandRefusesUnqualifiedAndGenericOperations(t *testing.T) {
	for _, operation := range []string{"preflight", "start", "thread/resume", "remote-control/start", "pair", "proxy"} {
		var out, stderr bytes.Buffer
		code := codexRCCtl(context.Background(), []string{operation}, strings.NewReader(`{}`), &out, &stderr, func(string) string { return "" })
		if code == exitOK || out.Len() != 0 {
			t.Fatal("unqualified command admitted", operation, code, out.String())
		}
	}
}
