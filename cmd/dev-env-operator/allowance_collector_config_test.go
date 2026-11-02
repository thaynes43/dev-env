package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thaynes43/dev-env/internal/allowancecollector"
)

func TestAllowanceCollectorDefaultsOffAndConflictsRefuseBeforeFileIO(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.allowanceCollector != nil {
		t.Fatal("default collector activated", err)
	}
	for _, args := range [][]string{{"--allowance-collector-config-file=/missing/private"}, {"--enable-operator-allowance-collector"}, {"--enable-operator-allowance-collector", "--enable-aggregate-allowance-reports", "--aggregate-allowance-config-file=/missing/private", "--allowance-collector-config-file=/missing/private", "--leader-elect=false"}, {"--enable-operator-allowance-collector", "--enable-aggregate-allowance-reports", "--aggregate-allowance-config-file=/missing/private", "--allowance-collector-config-file=relative"}} {
		if _, err = parseFlags(args); !errors.Is(err, allowancecollector.ErrCollector) {
			t.Fatal("conflicting collector got beyond pre-IO gate", err)
		}
	}
	if cfg, err := parseAllowanceCollector(false, "", nil, true); err != nil || cfg != nil {
		t.Fatal("nil selection", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, []byte("private-canary"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = parseAllowanceCollector(true, path, &aggregateAllowanceSelection{}, true); !errors.Is(err, allowancecollector.ErrCollector) {
		t.Fatal("world-readable configuration accepted", err)
	}
}
