package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/thaynes43/dev-env/web/portal/session"
)

func TestPortalCommandVersionAndUsage(t *testing.T) {
	if run([]string{"version"}) != 0 || run([]string{"serve"}) != 2 || run([]string{"version", "extra"}) != 2 {
		t.Fatal("portal command usage changed")
	}
}

func TestPagesSourceCheckCommand(t *testing.T) {
	count, observed := 0, "2026-10-28T12:00:00Z"
	for _, tc := range []struct {
		result session.PagesSourceCheck
		code   int
		want   string
	}{
		{session.PagesSourceCheck{Status: "available", Count: &count, ObservedAt: &observed}, 0, "{\"status\":\"available\",\"count\":0,\"observedAt\":\"2026-10-28T12:00:00Z\"}\n"},
		{session.PagesSourceCheck{Status: "disabled"}, 1, "{\"status\":\"disabled\",\"count\":null,\"observedAt\":null}\n"},
		{session.PagesSourceCheck{Status: "unavailable"}, 1, "{\"status\":\"unavailable\",\"count\":null,\"observedAt\":null}\n"},
	} {
		var out, stderr bytes.Buffer
		checks, serves := 0, 0
		check := func(context.Context) session.PagesSourceCheck { checks++; return tc.result }
		serve := func(context.Context, string) error { serves++; return errors.New("must not initialize server") }
		if got := runCommand([]string{"pages-source-check"}, &out, &stderr, check, serve); got != tc.code || out.String() != tc.want || stderr.Len() != 0 || checks != 1 || serves != 0 {
			t.Fatalf("code=%d out=%q stderr=%q checks=%d serves=%d", got, out.String(), stderr.String(), checks, serves)
		}
	}
}

func TestPagesSourceCheckRejectsSelectorsBeforeWork(t *testing.T) {
	for _, args := range [][]string{{"pages-source-check", "extra"}, {"pages-source-check", "--url=http://example.invalid"}, {"pages-source-check", "--root=/tmp"}, {"--pages-source-check"}, {"pages-source-check", "--help"}} {
		var out, stderr bytes.Buffer
		called := false
		check := func(context.Context) session.PagesSourceCheck { called = true; return session.PagesSourceCheck{} }
		serve := func(context.Context, string) error { called = true; return nil }
		if got := runCommand(args, &out, &stderr, check, serve); got != 2 || called || out.Len() != 0 || stderr.String() != "portal usage unavailable\n" {
			t.Fatalf("args=%q code=%d called=%v out=%q stderr=%q", args, got, called, out.String(), stderr.String())
		}
	}
}

func TestPortalDefaultServerDispatchPreserved(t *testing.T) {
	var out, stderr bytes.Buffer
	served := false
	check := func(context.Context) session.PagesSourceCheck {
		t.Fatal("unexpected diagnostic")
		return session.PagesSourceCheck{}
	}
	serve := func(_ context.Context, root string) error {
		served = root == session.ProductionRoot
		return errors.New("PRIVATE-SERVER-ERROR")
	}
	if got := runCommand(nil, &out, &stderr, check, serve); got != 1 || !served || out.Len() != 0 || stderr.String() != "portal runtime unavailable\n" {
		t.Fatalf("code=%d served=%v out=%q stderr=%q", got, served, out.String(), stderr.String())
	}
}
