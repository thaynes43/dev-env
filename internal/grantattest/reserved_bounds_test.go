package grantattest

import (
	"slices"
	"testing"
)

func TestReservedProposedExclusionsAndExplicitBoundedException(t *testing.T) {
	defaults := ReservedDefaultExclusions()
	if len(defaults) != 13 {
		t.Fatal("proposed DESIGN042 defaults lost", defaults)
	}
	for _, ns := range defaults {
		t.Run(ns, func(t *testing.T) {
			in := reservedTestRD3(false)
			in.Grant.Namespaces = []string{ns}
			in.Request.Namespaces = []string{ns}
			if _, err := EvaluateReserved(in); err == nil {
				t.Fatal("default excluded namespace accepted")
			}
			in = reservedTestRD3(true)
			in.Request.Snapshot = []NamespaceBinding{{Name: ns, UID: "namespace-uid"}}
			if _, err := EvaluateReserved(in); err == nil {
				t.Fatal("default excluded snapshot accepted")
			}
		})
	}
	in := reservedTestRD3(false)
	in.Grant.Namespaces = []string{"kube-public"}
	in.Request.Namespaces = []string{"kube-public"}
	in.Exclusions = []string{"dev-agents"}
	r, err := EvaluateReserved(in)
	if err != nil {
		t.Fatal("explicit bounded namespace exception required a root grant", err)
	}
	if !slices.Equal(r.Exclusions, []string{"dev-agents"}) || r.RootEquivalent {
		t.Fatal("exception widened into root", r)
	}
	changed := r
	changed.Exclusions = []string{"dev-tools"}
	if ReservedBindingDigest(changed) == r.Digest {
		t.Fatal("exclusion change was not bound into digest")
	}
	if _, err := EncodeReserved(changed); err == nil {
		t.Fatal("changed bound encoded with stale digest")
	}
	in.Exclusions = []string{}
	if _, err := EvaluateReserved(in); err != nil {
		t.Fatal("explicit empty bound was replaced with a permanent exclusion", err)
	}
}

func TestReservedBoundsAreCopiedAndRechecked(t *testing.T) {
	in := reservedTestRD3(true)
	in.Exclusions = []string{"dev-tools", "dev-agents"}
	original := slices.Clone(in.Exclusions)
	r, err := EvaluateReserved(in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(in.Exclusions, original) {
		t.Fatal("configuration input mutated")
	}
	if _, err := RecheckSnapshot(r, append(slices.Clone(r.Snapshot), NamespaceBinding{Name: "dev-agents", UID: "excluded-uid"}, NamespaceBinding{Name: "future", UID: "future-uid"})); err != nil {
		t.Fatal(err)
	}
	in.Exclusions = []string{"dev-agents", "dev-agents"}
	if _, err := EvaluateReserved(in); err == nil {
		t.Fatal("duplicate bounds accepted")
	}
	if _, err := ParseReservedExclusions("dev-agents,,dev-tools"); err == nil {
		t.Fatal("ambiguous empty bound accepted")
	}
}

func TestReservedDenialRequiresSameScopeBinding(t *testing.T) {
	in := rd2Input()
	in.Request.Approve = false
	if _, err := EvaluateReserved(in); err != nil {
		t.Fatal("reserved denial cannot validate its own scope", err)
	}
	in.Request.ActorUID = "other-actor"
	if _, err := EvaluateReserved(in); err == nil {
		t.Fatal("wrong actor denial accepted")
	}
}

func reservedTestRD3(all bool) Input {
	in := rd2Input()
	in.Grant.Type = "kube"
	in.Grant.Role = "dev-env-grant-secrets-read"
	if all {
		in.Classes = []string{ClassRD3b}
		in.Request.Class = ClassRD3b
		in.Request.AllNamespaces = true
		in.Request.Snapshot = []NamespaceBinding{{Name: "frontend", UID: "namespace-uid"}}
	} else {
		in.Classes = []string{ClassRD3a}
		in.Request.Class = ClassRD3a
		in.Grant.Namespaces = []string{"frontend"}
		in.Request.Namespaces = []string{"frontend"}
	}
	return in
}
