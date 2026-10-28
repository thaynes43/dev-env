package main

import (
	"bytes"
	"context"
	"testing"
)

func TestOwnedAllowanceDisabledDoesNotReadEnvironment(t *testing.T) {
	var out, errOut bytes.Buffer
	getenv := func(string) string { panic("disabled diagnostic read environment") }
	if got := codexOwnedAllowance(context.Background(), nil, &out, &errOut, getenv); got != exitFailure || out.Len() != 0 {
		t.Fatal("disabled diagnostic exposed data")
	}
}
func TestOwnedAllowanceDoesNotAcceptSelectors(t *testing.T) {
	for _, arg := range []string{"--socket=x", "--home=x", "--pid=1", "--account=x"} {
		var out, errOut bytes.Buffer
		if got := codexOwnedAllowance(context.Background(), []string{"--enabled", arg}, &out, &errOut, func(string) string { return "" }); got != exitUsage || out.Len() != 0 {
			t.Fatal("accepted selector", arg)
		}
	}
}

func TestOwnedAllowanceHelperRefusesMissingControl(t *testing.T) {
	var errOut bytes.Buffer
	if codexOwnedAllowanceHelper(&errOut) != exitFailure || errOut.Len() != 0 {
		t.Fatal("helper accepted missing authenticated control")
	}
}
