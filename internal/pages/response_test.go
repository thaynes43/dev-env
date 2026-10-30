package pages

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeResponderOrders struct {
	observation OrderObservation
	err         error
	lookups     []AlertIdentity
}

func (f *fakeResponderOrders) FindOrder(_ context.Context, id AlertIdentity) (OrderObservation, error) {
	f.lookups = append(f.lookups, id)
	return f.observation, f.err
}

type fakeCanonicalClaims struct {
	observation ClaimObservation
	err         error
	lookups     []OrderReference
}

func (f *fakeCanonicalClaims) FindClaim(_ context.Context, _ AlertIdentity, order OrderReference) (ClaimObservation, error) {
	f.lookups = append(f.lookups, order)
	return f.observation, f.err
}

func responseFixture() (AlertIdentity, ResponseSources, time.Time, *fakeResponderOrders, *fakeCanonicalClaims) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	id := AlertIdentity{Fingerprint: "synthetic-fingerprint", StartsAt: "2026-10-30T11:00:00Z"}
	ref := OrderReference{Key: "rem-responder-0123abcd", Created: strconv.FormatInt(at.Add(-time.Hour).Unix(), 10)}
	orders := &fakeResponderOrders{observation: OrderObservation{
		SourceObservation: SourceObservation{Available: true, Source: "V1 synthetic order", ObservedAt: at},
		Order:             &ResponderOrder{Identity: id, Order: ref, Status: "claimed", Updated: strconv.FormatInt(at.Add(-time.Minute).Unix(), 10)},
	}}
	claims := &fakeCanonicalClaims{observation: ClaimObservation{
		SourceObservation: SourceObservation{Available: true, Source: "Canonical synthetic claim", ObservedAt: at},
		Claim:             &ClaimEvidence{Identity: id, Order: ref, Reference: CanonicalClaimReference{Kind: "OpenProject", ID: "313"}, Owner: "synthetic-owner", State: "claimed", StateAt: at.Add(-time.Minute), EvidenceURL: "https://example.test/work_packages/313"},
	}}
	return id, ResponseSources{Orders: orders, Claims: claims, MaxAge: 5 * time.Minute}, at, orders, claims
}

func TestResponseExplicitStates(t *testing.T) {
	for _, tc := range []struct{ state, status string }{
		{"claimed", "claimed"}, {"responding", "claimed"}, {"responding", "working"},
		{"needs-user", "escalated"}, {"needs-user", "failed"}, {"resolved", "done"},
	} {
		for _, kind := range []string{"OpenProject", "TaskClaim"} {
			t.Run(tc.state+"/"+tc.status+"/"+kind, func(t *testing.T) {
				id, sources, at, orders, claims := responseFixture()
				orders.observation.Order.Status = tc.status
				claims.observation.Claim.State = tc.state
				if kind == "TaskClaim" {
					claims.observation.Claim.Reference = CanonicalClaimReference{Kind: kind, ID: "dev-agents/synthetic-claim/synthetic-uid"}
				}
				got := ProjectResponse(context.Background(), id, sources, at)
				if got.State != tc.state || got.StateAt != responseTime(at.Add(-time.Minute)) || got.Owner != "synthetic-owner" || got.Order != orders.observation.Order.Order || got.Claim != claims.observation.Claim.Reference || got.EvidenceURL != claims.observation.Claim.EvidenceURL || got.OrderSource != orders.observation.Source || got.ClaimSource != claims.observation.Source {
					t.Fatalf("missing explicit evidence: %+v", got)
				}
			})
		}
	}
}

func TestResponseUnknownDistinctions(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		mutate                         func(*ResponseSources, *fakeResponderOrders, *fakeCanonicalClaims)
		orderCondition, claimCondition string
	}{
		{"unconfigured", func(s *ResponseSources, _ *fakeResponderOrders, _ *fakeCanonicalClaims) { *s = ResponseSources{} }, "Unconfigured", "Unconfigured"},
		{"order unavailable", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) {
			o.err = errors.New("PRIVATE diagnostic")
		}, "Unavailable", "Unconfigured"},
		{"order absent source", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) {
			o.observation.Available = false
		}, "Unavailable", "Unconfigured"},
		{"order empty", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) { o.observation.Order = nil }, "Empty", "Unconfigured"},
		{"order stale", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) {
			o.observation.ObservedAt = o.observation.ObservedAt.Add(-6 * time.Minute)
		}, "Stale", "Unconfigured"},
		{"order future", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) {
			o.observation.ObservedAt = o.observation.ObservedAt.Add(time.Second)
		}, "Invalid", "Unconfigured"},
		{"no age policy", func(s *ResponseSources, _ *fakeResponderOrders, _ *fakeCanonicalClaims) { s.MaxAge = 0 }, "Invalid", "Unconfigured"},
		{"order identity mismatch", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) {
			o.observation.Order.Identity.Fingerprint = "different"
		}, "Unjoined", "Unconfigured"},
		{"bad V1 key", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) {
			o.observation.Order.Order.Key = "invented"
		}, "Invalid", "Unconfigured"},
		{"missing updated", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) {
			o.observation.Order.Updated = ""
		}, "Invalid", "Unconfigured"},
		{"missing created", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) {
			o.observation.Order.Order.Created = ""
		}, "Invalid", "Unconfigured"},
		{"updated after observation", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) {
			o.observation.Order.Updated = strconv.FormatInt(o.observation.ObservedAt.Add(time.Second).Unix(), 10)
		}, "Invalid", "Unconfigured"},
		{"claim unconfigured", func(s *ResponseSources, _ *fakeResponderOrders, _ *fakeCanonicalClaims) { s.Claims = nil }, "Available", "Unconfigured"},
		{"claim unavailable", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.err = errors.New("PRIVATE claim diagnostic")
		}, "Available", "Unavailable"},
		{"claim empty", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) { c.observation.Claim = nil }, "Available", "Empty"},
		{"claim stale", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.observation.ObservedAt = c.observation.ObservedAt.Add(-6 * time.Minute)
		}, "Available", "Stale"},
		{"claim missing source", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) { c.observation.Source = "" }, "Available", "Invalid"},
		{"claim other order", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.observation.Claim.Order.Key = "rem-responder-ffffffff"
		}, "Available", "Unjoined"},
		{"claim old attempt", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.observation.Claim.Order.Created = "1793350000"
		}, "Available", "Unjoined"},
		{"claim other identity", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.observation.Claim.Identity.StartsAt = "2026-10-30T10:00:00Z"
		}, "Available", "Unjoined"},
		{"no owner", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.observation.Claim.Owner = ""
		}, "Available", "Invalid"},
		{"no timestamp", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.observation.Claim.StateAt = time.Time{}
		}, "Available", "Invalid"},
		{"future timestamp", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.observation.Claim.StateAt = c.observation.ObservedAt.Add(time.Second)
		}, "Available", "Invalid"},
		{"no evidence", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.observation.Claim.EvidenceURL = ""
		}, "Available", "Invalid"},
		{"no canonical reference", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.observation.Claim.Reference = CanonicalClaimReference{}
		}, "Available", "Invalid"},
		{"no explicit state", func(_ *ResponseSources, _ *fakeResponderOrders, c *fakeCanonicalClaims) {
			c.observation.Claim.State = ""
		}, "Available", "Unjoined"},
		{"pending is not claimed", func(_ *ResponseSources, o *fakeResponderOrders, _ *fakeCanonicalClaims) {
			o.observation.Order.Status = "pending"
		}, "Available", "Unjoined"},
		{"failed is not resolved", func(_ *ResponseSources, o *fakeResponderOrders, c *fakeCanonicalClaims) {
			o.observation.Order.Status = "failed"
			c.observation.Claim.State = "resolved"
		}, "Available", "Unjoined"},
		{"escalated is not resolved", func(_ *ResponseSources, o *fakeResponderOrders, c *fakeCanonicalClaims) {
			o.observation.Order.Status = "escalated"
			c.observation.Claim.State = "resolved"
		}, "Available", "Unjoined"},
		{"done alone is not resolved", func(_ *ResponseSources, o *fakeResponderOrders, c *fakeCanonicalClaims) {
			o.observation.Order.Status = "done"
			c.observation.Claim = nil
		}, "Available", "Empty"},
		{"stale terminal", func(_ *ResponseSources, o *fakeResponderOrders, c *fakeCanonicalClaims) {
			o.observation.Order.Status = "done"
			c.observation.Claim.State = "resolved"
			c.observation.ObservedAt = c.observation.ObservedAt.Add(-6 * time.Minute)
		}, "Available", "Stale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, sources, at, orders, claims := responseFixture()
			tc.mutate(&sources, orders, claims)
			got := ProjectResponse(context.Background(), id, sources, at)
			if got.State != "Unknown" || got.StateAt != "Unknown" || got.EvidenceURL != "Unknown" || got.OrderCondition != tc.orderCondition || got.ClaimCondition != tc.claimCondition || strings.Contains(got.OrderSource+got.ClaimSource, "PRIVATE") {
				t.Fatalf("Unknown distinctions lost: %+v", got)
			}
			if got.OrderCondition != "Available" && len(claims.lookups) != 0 {
				t.Fatal("looked up a claim without a joined order")
			}
		})
	}
}

func TestResponseRefireAndTwoReaders(t *testing.T) {
	id, sources, at, orders, claims := responseFixture()
	first := ProjectResponse(context.Background(), id, sources, at)
	second := ProjectResponse(context.Background(), id, sources, at)
	if !reflect.DeepEqual(first, second) || len(orders.lookups) != 2 || len(claims.lookups) != 2 || orders.lookups[0] != id || claims.lookups[0] != first.Order {
		t.Fatal("readers did not resolve the existing owner and attempt")
	}
	refire := AlertIdentity{Fingerprint: id.Fingerprint, StartsAt: "2026-10-30T11:30:00Z"}
	got := ProjectResponse(context.Background(), refire, sources, at)
	if got.Identity == first.Identity || got.Identity != refire || got.State != "Unknown" || got.OrderCondition != "Unjoined" || len(claims.lookups) != 2 {
		t.Fatal("refire inherited an earlier response", got)
	}
	// A source may explicitly associate the new firing with an existing order,
	// but both source bindings must identify that new start.
	orders.observation.Order.Identity = refire
	claims.observation.Claim.Identity = refire
	got = ProjectResponse(context.Background(), refire, sources, at)
	if got.State != "claimed" || got.Order != first.Order || got.Owner != first.Owner {
		t.Fatal("explicit refire join failed", got)
	}
}

func TestResponseUnsafeEvidence(t *testing.T) {
	for _, url := range []string{"http://example.test/a", "javascript:bad", "https://user:pass@example.test/a", "https://example.test/a?token=SECRET", "https://example.test/a#secret=SECRET", "https://example.test/a?other=SECRET", "https://example.test/a\n", "https://example.test/\u202e", "https://exam%C2%ADple.test/a", "https://exam%E2%80%AEple.test/a", "https://example.test/%0a", "https://example.test/%FF", strings.Repeat("x", 513)} {
		t.Run(url, func(t *testing.T) {
			id, sources, at, _, claims := responseFixture()
			claims.observation.Claim.EvidenceURL = url
			got := ProjectResponse(context.Background(), id, sources, at)
			if got.State != "Unknown" || got.EvidenceURL != "Unknown" || got.ClaimCondition != "Invalid" {
				t.Fatal("unsafe evidence released", got)
			}
		})
	}
	for _, field := range []string{"owner", "source", "identity"} {
		id, sources, at, _, claims := responseFixture()
		switch field {
		case "owner":
			claims.observation.Claim.Owner = "token=SECRET"
		case "source":
			claims.observation.Source = "token=SECRET"
		case "identity":
			id.Fingerprint = "token=SECRET"
		}
		got := ProjectResponse(context.Background(), id, sources, at)
		if got.State != "Unknown" || strings.Contains(got.Owner+got.ClaimSource+got.Identity.Fingerprint, "SECRET") {
			t.Fatalf("unsafe %s released: %+v", field, got)
		}
	}
}

func TestResponseTimestampBounds(t *testing.T) {
	for _, stamp := range []time.Time{
		{},
		time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("offset", -3600)),
	} {
		for _, field := range []string{"evaluation", "order observation", "claim observation", "state"} {
			t.Run(field+"/"+stamp.String(), func(t *testing.T) {
				id, sources, at, orders, claims := responseFixture()
				switch field {
				case "evaluation":
					at = stamp
				case "order observation":
					orders.observation.ObservedAt = stamp
				case "claim observation":
					claims.observation.ObservedAt = stamp
				case "state":
					claims.observation.Claim.StateAt = stamp
				}
				got := ProjectResponse(context.Background(), id, sources, at)
				if got.State != "Unknown" || got.StateAt != "Unknown" || got.EvidenceURL != "Unknown" || got.OrderCondition != "Invalid" && got.ClaimCondition != "Invalid" {
					t.Fatal("out-of-range timestamp accepted", got)
				}
			})
		}
	}
	// Reproduce the review finding with all observation and state times outside
	// the renderer's range, yet mutually fresh and ordered.
	id, sources, _, orders, claims := responseFixture()
	at := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	orders.observation.ObservedAt = at
	orders.observation.Order.Status = "done"
	claims.observation.ObservedAt = at
	claims.observation.Claim.StateAt = at
	claims.observation.Claim.State = "resolved"
	got := ProjectResponse(context.Background(), id, sources, at)
	if got.State != "Unknown" || got.StateAt != "Unknown" || got.OrderCondition != "Invalid" || len(orders.lookups) != 0 {
		t.Fatal("MCP could emit resolved while UI renders Unknown", got)
	}
}
