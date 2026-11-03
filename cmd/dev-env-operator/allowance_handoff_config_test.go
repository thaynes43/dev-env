package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/allowancecollector"
)

const handoffSnapshotJSON = `{"oldConfig":"e30=","newConfig":"e30=","trust":"e30=","publisherUID":"u","publisherSA":"s","slotUID":"s","resourceVersion":"1","homeUID":"h","journal":"e30=","report":"","evictedPod":"e30=","helperSHA256":"x","journalSHA256":"x","reportSHA256":"x","cycle":56,"nonce":"n"}`

func writeHandoffFile(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAllowanceHandoffDefaultsOffAndNeedsEnabledCollector(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.allowanceHandoff != nil || o.allowanceHandoffRefused {
		t.Fatal("default handoff activated", err)
	}
	for _, args := range [][]string{
		{"--allowance-handoff-file=/missing/private"},
		{"--enable-operator-allowance-collector", "--enable-aggregate-allowance-reports", "--aggregate-allowance-config-file=/missing/private", "--allowance-collector-config-file=/missing/private", "--allowance-handoff-file=relative"},
	} {
		if _, err = parseFlags(args); !errors.Is(err, allowancecollector.ErrCollector) {
			t.Fatal("handoff got beyond the pre-IO gate", err)
		}
	}
	if sel, err := parseAllowanceHandoff("", nil, nil); err != nil || sel != nil {
		t.Fatal("empty path", err)
	}
	path := writeHandoffFile(t, "x", 0600)
	if _, err = parseAllowanceHandoff(path, nil, &aggregateAllowanceSelection{}); err == nil {
		t.Fatal("accepted without a collector")
	}
}

func TestAllowanceHandoffFileIsPrivateAndExact(t *testing.T) {
	collector, sel := &allowancecollector.Config{}, &aggregateAllowanceSelection{}
	hash := strings.Repeat("a", 64)
	good := `{"approvedHash":"` + hash + `","snapshot":` + handoffSnapshotJSON + `}`
	got, err := parseAllowanceHandoff(writeHandoffFile(t, good, 0600), collector, sel)
	if err != nil || got == nil || got.approved != hash || got.snapshot.Cycle != 56 {
		t.Fatal("valid request refused", err)
	}
	for name, body := range map[string]string{
		"extra key":   `{"approvedHash":"` + hash + `","snapshot":` + handoffSnapshotJSON + `,"kubeconfig":"x"}`,
		"short hash":  `{"approvedHash":"abc","snapshot":` + handoffSnapshotJSON + `}`,
		"no snapshot": `{"approvedHash":"` + hash + `"}`,
		"garbage":     `private-canary`,
	} {
		if _, err = parseAllowanceHandoff(writeHandoffFile(t, body, 0600), collector, sel); err == nil {
			t.Fatal("accepted", name)
		} else if strings.Contains(err.Error(), "canary") {
			t.Fatal("refusal leaked content")
		}
	}
	if _, err = parseAllowanceHandoff(writeHandoffFile(t, good, 0644), collector, sel); err == nil {
		t.Fatal("world-readable request accepted")
	}
}
