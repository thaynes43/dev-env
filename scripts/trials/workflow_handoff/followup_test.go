package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestScopeCycleDoesNotFallBackToOlderScope(t *testing.T) {
	for _, name := range []string{"followup-scope-cycle.json", "followup-scope-cycle-reversed.json", "followup-scope-three-cycle.json"} {
		got := mustRun(t, name, 3)
		if got.CurrentScope != nil || !has(got.Conflicts, "equal-time supersession cycle") || got.MergeRelease != "blocked" || countPrefix(got.Recommendations, "recommend existing normal") != 0 || holdState(got, "media-work") != "pending" {
			t.Fatalf("%s %#v", name, got)
		}
		if len(got.RetainedScope) < 3 {
			t.Fatalf("%s lost retained scopes", name)
		}
	}
	// Existing noncyclic latest-scope and strictly later supersession stay valid.
	got := mustRun(t, "c-scope-holds.json", 0)
	if got.CurrentScope == nil || got.CurrentScope.Identity != "scope-merge" {
		t.Fatalf("noncyclic scope %#v", got)
	}
}

func TestLatestMatchingPassControlsWriteAdvice(t *testing.T) {
	for _, name := range []string{"followup-pass-revokes.json", "followup-pass-revokes-reversed.json"} {
		got := mustRun(t, name, 0)
		if got.WritesRecommended || len(got.Passes) != 2 || got.IgnoredReplayCount != 1 || got.Outcome != "recommend-normal-merge-release" || got.Executor.Identity != "executor-a" {
			t.Fatalf("%s %#v", name, got)
		}
	}
	allowed := mustRun(t, "followup-pass-allows.json", 0)
	if !allowed.WritesRecommended || len(allowed.Passes) != 2 || !has(allowed.MergeBlockers, "current executor writes remain recommended") {
		t.Fatalf("latest matching true %#v", allowed)
	}
	mismatch := mustRun(t, "followup-pass-mismatch.json", 0)
	if mismatch.WritesRecommended || len(mismatch.Passes) != 1 || mismatch.Executor.Identity != "executor-a" || !has(mismatch.Refusals, "worker pass executor mismatch pass-2") {
		t.Fatalf("mismatched pass widened advice %#v", mismatch)
	}
}

func TestSchemaTagsCoverInputStructs(t *testing.T) {
	// Explicit event types keep this tied to the finite input schema. Walk nested
	// document/event structs, including embedded headers, binding and provenance.
	inputs := []any{
		document{}, header{}, passEvent{}, execEvent{}, deadlineEvent{}, stopEvent{},
		ackEvent{}, reviewEvent{}, scopeEvent{}, holdEvent{}, dispEvent{}, assignEvent{},
		leaseEvent{}, reconnectEvent{}, gateEvent{}, claimEvent{},
	}
	tags := map[string]bool{}
	for _, tag := range schemaTags {
		tags[tag] = true
	}
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag != "" && tag != "-" && !tags[tag] {
				t.Errorf("%s.%s tag %q missing from schemaTags", typ.Name(), field.Name, tag)
			}
			walk(field.Type)
		}
	}
	for _, in := range inputs {
		walk(reflect.TypeOf(in))
	}
}
