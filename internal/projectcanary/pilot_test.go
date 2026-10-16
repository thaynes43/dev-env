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
