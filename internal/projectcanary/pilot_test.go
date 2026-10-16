package projectcanary_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestPilotPolicyScopeAndAbsoluteWindow(t *testing.T) {
	f := testutil.NewPilot(t)
	p := f.Policy
	raw, _ := json.Marshal(p)
	parsed, err := projectcanary.ParsePilot(string(raw))
	if err != nil || parsed.Digest() != p.Digest() {
		t.Fatal(err)
	}
	if off, e := projectcanary.ParsePilot(""); e != nil || off != nil {
		t.Fatal("default enabled")
	}
	for _, raw := range []string{string(raw) + `{}`, strings.Replace(string(raw), `"sealUID"`, `"SealUID"`, 1), strings.Replace(string(raw), `"sealUID":`, `"unknown":0,"sealUID":`, 1)} {
		if _, e := projectcanary.ParsePilot(raw); e == nil {
			t.Fatal("non-exact policy accepted")
		}
	}
	for _, at := range []time.Time{p.NotBefore.Add(-time.Nanosecond), p.ExpiresAt, p.ExpiresAt.Add(time.Hour)} {
		if parsed.Open(at) {
			t.Fatal("expired/pre-window admission", at)
		}
	}
	if !parsed.Open(p.NotBefore) || !parsed.Open(p.ExpiresAt.Add(-time.Nanosecond)) {
		t.Fatal("valid boundary refused")
	}
	req := p.Request(0)
	changes := []func(){func() { req.Project = "other" }, func() { req.Repo = "other" }, func() { req.Agent = "claude" }, func() { req.Model = "gpt-6-astra" }, func() { req.Effort = "max" }, func() { req.Profile = "full" }, func() { req.Size = "M" }, func() { req.IdempotencyKey = "third" }, func() { req.Prompt += " override" }, func() { req.Limits.Timeout = "16m" }, func() { req.Mode = "local" }, func() { req.Name = "third" }, func() { req.Restore = "restore" }, func() { req.Base = "main" }}
	for i, change := range changes {
		req = p.Request(0)
		change()
		if _, ok := p.Slot(req); ok {
			t.Fatal("scope widened", i)
		}
	}
	for i := range 2 {
		if slot, ok := p.Slot(p.Request(i)); !ok || slot != i {
			t.Fatal("fixed slot rejected")
		}
		if p.MatchSession(f.Session(t, i), i) != nil {
			t.Fatal("retained fixture invalid")
		}
	}
}
func TestPilotPolicySealAndSourceDriftFailClosed(t *testing.T) {
	for _, kind := range []string{"valid", "missing", "replaced", "mutable", "digest", "config-time", "catalog", "common", "template"} {
		t.Run(kind, func(t *testing.T) {
			f := testutil.NewPilot(t)
			objects := f.Objects()
			switch kind {
			case "missing":
				objects = []client.Object{f.Catalog, f.Template, f.Common, f.Header}
			case "replaced":
				f.Seal.UID = types.UID("44444444-4444-4444-8444-444444444444")
			case "mutable":
				f.Seal.Immutable = nil
			case "digest":
				f.Seal.Data["policySHA256"] = strings.Repeat("f", 64)
			case "config-time":
				f.Policy.ExpiresAt = f.Policy.ExpiresAt.Add(time.Minute)
			case "catalog":
				f.Catalog.Data["catalog.json"] = `{}`
			case "common":
				for k := range f.Common.Data {
					f.Common.Data[k] += " changed"
				}
			case "template":
				f.Template.Data[templates.Key] = "broken"
			}
			c := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(objects...).Build()
			e, err := f.Policy.Resolve(context.Background(), c, client.ObjectKeyFromObject(f.Template), f.Now)
			if kind == "valid" {
				if err != nil || string(e.Snapshot) != string(f.Snapshot) {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("drift admitted")
			}
			var maps corev1.ConfigMapList
			if err = c.List(context.Background(), &maps); err != nil {
				t.Fatal(err)
			}
			if len(maps.Items) != len(objects) {
				t.Fatal("authority reseeded")
			}
		})
	}
}

func TestPilotSpecBindingRejectsTampering(t *testing.T) {
	f := testutil.NewPilot(t)
	s := f.Session(t, 0)
	if err := f.Policy.MatchSession(s, 0); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing", "seal", "policy", "common", "slot", "stripped-markers"} {
		t.Run(kind, func(t *testing.T) {
			c := s.DeepCopy()
			switch kind {
			case "missing":
				c.Spec.PrivatePilot = nil
			case "seal":
				c.Spec.PrivatePilot.SealUID = "66666666-6666-4666-8666-666666666666"
			case "policy":
				c.Spec.PrivatePilot.PolicyDigest = strings.Repeat("a", 64)
			case "common":
				c.Spec.PrivatePilot.CommonRevision = strings.Repeat("a", 64)
			case "slot":
				c.Spec.PrivatePilot.Slot = 1
			case "stripped-markers":
				c.Name = "foreign"
				c.Annotations = nil
				c.Finalizers = nil
				if !projectcanary.IsPilot(c) {
					t.Fatal("typed pilot lost marker")
				}
			}
			if f.Policy.MatchSession(c, 0) == nil {
				t.Fatal("forged binding accepted")
			}
		})
	}
}

func TestPilotCohortLegacyCompatibilityAndDistinctBindings(t *testing.T) {
	legacy := testutil.NewPilot(t)
	// Captured before the cohort field existed, from the accepted API fixture.
	if legacy.Policy.Digest() != "fe1974daeb0dc9fc7ab23bcc459d50847a3ad0a0686055350c58ffbf78ee242c" {
		t.Fatal("legacy digest changed")
	}
	raw, _ := json.Marshal(legacy.Policy)
	if strings.Contains(string(raw), `"cohort"`) || legacy.Policy.SealName() != projectcanary.PilotSealName {
		t.Fatal("legacy encoding or seal changed")
	}
	fresh := testutil.NewPilot(t)
	fresh.SetCohort("c7-20261016-1800")
	if fresh.Policy.Validate() != nil || fresh.Policy.Digest() == legacy.Policy.Digest() || fresh.Policy.SealName() == legacy.Policy.SealName() {
		t.Fatal("cohort authority aliases legacy")
	}
	for slot := range 2 {
		if legacy.Policy.SessionName(slot) != projectcanary.PilotSessionName(slot) || legacy.Policy.Key(slot) != projectcanary.PilotKey(slot) {
			t.Fatal("legacy identity changed")
		}
		s := fresh.Session(t, slot)
		if fresh.Policy.SessionName(slot) == legacy.Policy.SessionName(slot) || fresh.Policy.Key(slot) == legacy.Policy.Key(slot) {
			t.Fatal("cohort aliases old slot")
		}
		if fresh.Policy.MatchSession(s, slot) != nil {
			t.Fatal("new binding refused")
		}
		record, err := projectcanary.BoundPilot(s)
		if err != nil || record.Policy.Cohort != fresh.Policy.Cohort {
			t.Fatal("retained cohort/slot not recovered", err)
		}
		if legacy.Policy.MatchSession(s, slot) == nil || fresh.Policy.MatchSession(legacy.Session(t, slot), slot) == nil {
			t.Fatal("cross-cohort rebound")
		}
		s.Spec.PrivatePilot = nil
		s.Annotations = nil
		s.Finalizers = nil
		if !projectcanary.IsPilot(s) {
			t.Fatal("reserved named cohort escaped pilot guards")
		}
	}
	if fresh.Policy.SessionName(2) != "" || fresh.Policy.Key(2) != "" {
		t.Fatal("third identity")
	}
	if _, ok := fresh.Policy.Slot(legacy.Policy.Request(0)); ok {
		t.Fatal("old key admitted to fresh cohort")
	}
	for slot := range 2 {
		if _, err := projectcanary.BoundPilot(legacy.Session(t, slot)); err != nil {
			t.Fatal("legacy retained record lost", err)
		}
	}
}

func TestPilotCohortSyntaxAndAuthoritySeparation(t *testing.T) {
	for _, cohort := range []string{"v1", "UPPER", "-leading", "trailing-", "a/b", "a.b", strings.Repeat("a", 33), " old", "é"} {
		t.Run(cohort, func(t *testing.T) {
			f := testutil.NewPilot(t)
			f.Policy.Cohort = cohort
			if f.Policy.Validate() == nil {
				t.Fatal("invalid cohort accepted")
			}
		})
	}
	for _, cohort := range []string{"a", "cohort-2", strings.Repeat("a", 32)} {
		f := testutil.NewPilot(t)
		f.SetCohort(cohort)
		if f.Policy.Validate() != nil {
			t.Fatal("bounded cohort refused")
		}
	}
	for _, kind := range []string{"valid", "old-seal-only", "wrong-digest", "wrong-uid", "cohort-change"} {
		t.Run(kind, func(t *testing.T) {
			f := testutil.NewPilot(t)
			oldSeal := f.Seal.DeepCopy()
			f.SetCohort("fresh-1800")
			objects := f.Objects()
			switch kind {
			case "old-seal-only":
				objects[2] = oldSeal
			case "wrong-digest":
				f.Seal.Data["policySHA256"] = oldSeal.Data["policySHA256"]
			case "wrong-uid":
				f.Seal.UID = "44444444-4444-4444-8444-444444444444"
			case "cohort-change":
				f.Policy.Cohort = "other-1800"
			}
			c := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(objects...).Build()
			_, err := f.Policy.Resolve(context.Background(), c, client.ObjectKeyFromObject(f.Template), f.Now)
			if (err == nil) != (kind == "valid") {
				t.Fatal("cohort seal authority not exact", err)
			}
			var maps corev1.ConfigMapList
			if e := c.List(context.Background(), &maps); e != nil || len(maps.Items) != len(objects) {
				t.Fatal("authority mutated", e)
			}
		})
	}
}
