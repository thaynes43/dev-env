package pages

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type snapshotOrders struct {
	observations map[AlertIdentity]OrderObservation
	calls        [][]AlertIdentity
	err          error
}

func (s *snapshotOrders) FindOrders(_ context.Context, ids []AlertIdentity) (map[AlertIdentity]OrderObservation, error) {
	s.calls = append(s.calls, append([]AlertIdentity(nil), ids...))
	return s.observations, s.err
}

type snapshotClaims struct {
	observations map[ClaimLookup]ClaimObservation
	calls        [][]ClaimLookup
	err          error
}

func (s *snapshotClaims) FindClaims(_ context.Context, lookups []ClaimLookup) (map[ClaimLookup]ClaimObservation, error) {
	s.calls = append(s.calls, append([]ClaimLookup(nil), lookups...))
	return s.observations, s.err
}

func TestResponseEvidenceOriginPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		origins   []string
		allowed   bool
	}{
		{"empty", "https://example.test/a", nil, false},
		{"exact", "https://example.test/a", []string{"https://example.test"}, true},
		{"second origin", "https://example.test/a", []string{"https://other.test", "https://example.test"}, true},
		{"denied", "https://other.test/a", []string{"https://example.test"}, false},
		{"subdomain", "https://sub.example.test/a", []string{"https://example.test"}, false},
		{"host suffix", "https://example.test.attacker.test/a", []string{"https://example.test"}, false},
		{"host prefix", "https://notexample.test/a", []string{"https://example.test"}, false},
		{"uppercase host", "https://Example.test/a", []string{"https://Example.test"}, false},
		{"uppercase scheme", "HTTPS://example.test/a", []string{"HTTPS://example.test"}, false},
		{"uppercase config", "https://example.test/a", []string{"https://Example.test"}, false},
		{"default port", "https://example.test:443/a", []string{"https://example.test:443"}, false},
		{"other port", "https://example.test:8443/a", []string{"https://example.test:8443"}, false},
		{"config port not normalized", "https://example.test/a", []string{"https://example.test:443"}, false},
		{"trailing slash", "https://example.test/a", []string{"https://example.test/"}, false},
		{"path config", "https://example.test/a", []string{"https://example.test/a"}, false},
		{"query config", "https://example.test/a", []string{"https://example.test?x=y"}, false},
		{"fragment config", "https://example.test/a", []string{"https://example.test#x"}, false},
		{"credentials config", "https://example.test/a", []string{"https://user@example.test"}, false},
		{"http config", "https://example.test/a", []string{"http://example.test"}, false},
		{"wildcard", "https://sub.example.test/a", []string{"https://*.example.test"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, sources, at, _, claims := responseFixture()
			sources.EvidenceOrigins = tc.origins
			claims.observation.Claim.EvidenceURL = tc.url
			got := ProjectResponse(context.Background(), id, sources, at)
			if safeResponseURL(tc.url, tc.origins) != tc.allowed {
				t.Fatal("unexpected URL policy result")
			}
			if tc.allowed {
				if got.EvidenceURL != tc.url || got.State != "claimed" || got.ClaimCondition != "Available" {
					t.Fatalf("allowed origin withheld: %+v", got)
				}
			} else if got.EvidenceURL != "Unknown" || got.State != "Unknown" || got.Owner != "Unknown" || got.Claim.ID != "Unknown" || got.ClaimCondition != "Invalid" {
				t.Fatalf("denied origin released evidence: %+v", got)
			}
		})
	}
}

// One snapshot can contain independent successful joins, missing observations,
// observed empty records, invalid/stale evidence, mismatches and a refire. None
// of those identities may borrow the result or condition of another identity.
func TestResponseSnapshotPerIdentity(t *testing.T) {
	_, policy, at, _, _ := responseFixture()
	orders := &snapshotOrders{observations: map[AlertIdentity]OrderObservation{}}
	claims := &snapshotClaims{observations: map[ClaimLookup]ClaimObservation{}}
	policy.Orders, policy.Claims = orders, claims
	var identities, wantOrders []AlertIdentity
	var wantClaims []ClaimLookup
	want := map[AlertIdentity][2]string{}
	for _, tc := range []struct {
		name, orderCondition, claimCondition string
	}{
		{"joined", "Available", "Available"},
		{"mismatched", "Available", "Mismatched"},
		{"missing order", "Unavailable", "NotChecked"},
		{"empty order", "Empty", "NotChecked"},
		{"stale order", "Stale", "NotChecked"},
		{"invalid order", "Invalid", "NotChecked"},
		{"unjoined order", "Unjoined", "NotChecked"},
		{"missing claim", "Available", "Unavailable"},
		{"empty claim", "Available", "Empty"},
		{"stale claim", "Available", "Stale"},
		{"old attempt claim", "Available", "Unjoined"},
		{"invalid claim", "Available", "Invalid"},
	} {
		id, _, _, o, c := responseFixture()
		id.Fingerprint = tc.name
		o.observation.Order.Identity = id
		c.observation.Claim.Identity = id
		lookup := ClaimLookup{Identity: id, Order: o.observation.Order.Order}
		switch tc.name {
		case "mismatched":
			c.observation.Claim.State = "resolved"
		case "empty order":
			o.observation.Order = nil
		case "stale order":
			o.observation.ObservedAt = at.Add(-time.Hour)
		case "invalid order":
			o.observation.Order.Status = "working"
		case "unjoined order":
			o.observation.Order.Identity.StartsAt = "2026-10-30T10:00:00Z"
		case "empty claim":
			c.observation.Claim = nil
		case "stale claim":
			c.observation.ObservedAt = at.Add(-time.Hour)
		case "old attempt claim":
			c.observation.Claim.Order.Created = "1793357999"
		case "invalid claim":
			c.observation.Claim.EvidenceURL = "https://untrusted.test/a"
		}
		if tc.name != "missing order" {
			orders.observations[id] = o.observation
		}
		if tc.name != "missing claim" {
			claims.observations[lookup] = c.observation
		}
		identities = append(identities, id)
		wantOrders = append(wantOrders, id)
		if tc.orderCondition == "Available" {
			wantClaims = append(wantClaims, lookup)
		}
		want[id] = [2]string{tc.orderCondition, tc.claimCondition}
	}
	// A refire shares a fingerprint and reusable key with the first record,
	// but it cannot inherit that earlier start's order/claim binding.
	original := identities[0]
	refire := AlertIdentity{Fingerprint: original.Fingerprint, StartsAt: "2026-10-30T11:30:00Z"}
	orders.observations[refire] = orders.observations[original]
	identities = append(identities, refire, original) // duplicate is read once
	wantOrders = append(wantOrders, refire)
	want[refire] = [2]string{"Unjoined", "NotChecked"}
	invalid := AlertIdentity{Fingerprint: "invalid", StartsAt: "bad-time"}
	identities = append(identities, invalid)
	want[invalid] = [2]string{"Invalid", "NotChecked"}
	// Unrequested source entries cannot leak into the output.
	orders.observations[AlertIdentity{Fingerprint: "extra"}] = orders.observations[original]
	got := ProjectResponses(context.Background(), identities, policy, at)
	if !reflect.DeepEqual(orders.calls, [][]AlertIdentity{wantOrders}) || !reflect.DeepEqual(claims.calls, [][]ClaimLookup{wantClaims}) {
		t.Fatalf("not one bounded lookup per source: orders=%v claims=%v", orders.calls, claims.calls)
	}
	if len(got) != len(want) {
		t.Fatalf("snapshot identities: got %d want %d", len(got), len(want))
	}
	for id, conditions := range want {
		response := got[id]
		if response.OrderCondition != conditions[0] || response.ClaimCondition != conditions[1] {
			t.Errorf("%+v: %+v, want %v", id, response, conditions)
		}
		if conditions[1] != "Available" && (response.State != "Unknown" || response.Owner != "Unknown" || response.EvidenceURL != "Unknown" || response.Claim.ID != "Unknown") {
			t.Errorf("unconfirmed identity released evidence: %+v", response)
		}
		// The batch and single-identity entry points use one projection.
		if single := ProjectResponse(context.Background(), id, policy, at); !reflect.DeepEqual(single, response) {
			t.Errorf("batch/single drift: batch=%+v single=%+v", response, single)
		}
	}
	// A whole-source failure with otherwise usable records must not expose
	// cached entries. Only joined identities see a claim batch failure.
	claims.err = errors.New("private claim diagnostic")
	failedClaims := ProjectResponses(context.Background(), identities, policy, at)
	for id, conditions := range want {
		response := failedClaims[id]
		expected := conditions[1]
		if conditions[0] == "Available" {
			expected = "Unavailable"
		}
		if response.ClaimCondition != expected || response.Owner != "Unknown" || response.EvidenceURL != "Unknown" {
			t.Errorf("claim batch error leaked evidence: %+v", response)
		}
	}
	orders.err = errors.New("private order diagnostic")
	claimCalls := len(claims.calls)
	failedOrders := ProjectResponses(context.Background(), identities, policy, at)
	if len(claims.calls) != claimCalls {
		t.Fatal("failed order batch triggered claims")
	}
	for id, response := range failedOrders {
		condition := "Unavailable"
		if id == invalid {
			condition = "Invalid"
		}
		if response.OrderCondition != condition || response.ClaimCondition != "NotChecked" || response.Order.Key != "Unknown" || response.Owner != "Unknown" {
			t.Errorf("order batch error leaked evidence: %+v", response)
		}
	}
}

func TestResponseSnapshotSkipsUnusableLookups(t *testing.T) {
	id, policy, at, _, _ := responseFixture()
	for _, tc := range []string{"empty", "invalid identity", "invalid time", "invalid max age", "orders unconfigured", "order batch error", "order missing"} {
		t.Run(tc, func(t *testing.T) {
			orders := &snapshotOrders{}
			claims := &snapshotClaims{}
			sources := policy
			sources.Orders, sources.Claims = orders, claims
			ids, evaluation := []AlertIdentity{id}, at
			wantOrderCalls := 0
			switch tc {
			case "empty":
				ids = nil
			case "invalid identity":
				ids = []AlertIdentity{{Fingerprint: id.Fingerprint, StartsAt: "bad-time"}}
			case "invalid time":
				evaluation = time.Time{}
			case "invalid max age":
				sources.MaxAge = 0
			case "orders unconfigured":
				sources.Orders = nil
			case "order batch error":
				orders.err = errors.New("private error")
				wantOrderCalls = 1
			case "order missing":
				wantOrderCalls = 1
			}
			got := ProjectResponses(context.Background(), ids, sources, evaluation)
			if len(orders.calls) != wantOrderCalls || len(claims.calls) != 0 || len(got) != len(ids) {
				t.Fatalf("unexpected reads/output: orders=%v claims=%v output=%v", orders.calls, claims.calls, got)
			}
		})
	}
}

// A refire can be explicitly joined, but only to the exact new start and
// creation attempt. Two starts sharing a fingerprint and order key stay apart.
func TestResponseSnapshotRefireAttempts(t *testing.T) {
	id, policy, at, o, c := responseFixture()
	refire := AlertIdentity{Fingerprint: id.Fingerprint, StartsAt: "2026-10-30T11:30:00Z"}
	newOrder := *o.observation.Order
	newOrder.Identity, newOrder.Order.Created = refire, "1793359800"
	newClaim := *c.observation.Claim
	newClaim.Identity, newClaim.Order, newClaim.Owner = refire, newOrder.Order, "refire-owner"
	oldLookup := ClaimLookup{Identity: id, Order: o.observation.Order.Order}
	newLookup := ClaimLookup{Identity: refire, Order: newOrder.Order}
	orders := &snapshotOrders{observations: map[AlertIdentity]OrderObservation{
		id: o.observation, refire: {SourceObservation: o.observation.SourceObservation, Order: &newOrder},
	}}
	claims := &snapshotClaims{observations: map[ClaimLookup]ClaimObservation{
		oldLookup: c.observation, newLookup: {SourceObservation: c.observation.SourceObservation, Claim: &newClaim},
	}}
	policy.Orders, policy.Claims = orders, claims
	got := ProjectResponses(context.Background(), []AlertIdentity{id, refire}, policy, at)
	if got[id].Owner != "synthetic-owner" || got[refire].Owner != "refire-owner" || got[id].Order != oldLookup.Order || got[refire].Order != newLookup.Order || !reflect.DeepEqual(claims.calls, [][]ClaimLookup{{oldLookup, newLookup}}) {
		t.Fatalf("refire attempt association lost: %+v, lookups=%v", got, claims.calls)
	}
}
