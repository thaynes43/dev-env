package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorityFlagIsOptional(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--authority-file", filepath.Join(t.TempDir(), "missing")}, io.Discard, &stderr)
	if code != 1 || strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("code %d stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "bearer token file and the tracker credential file are required") {
		t.Fatalf("stderr %s", stderr.String())
	}
}
