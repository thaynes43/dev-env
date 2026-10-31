package pages

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func identityOrder(identity string) string {
	return strings.TrimSuffix(syntheticV1Order, "}") + `,"alertIdentity":` + identity + "}"
}

func TestV1IdentityJoin(t *testing.T) {
	for name, raw := range map[string]string{
		"exact":         `{"fingerprint":"synthetic-fingerprint","startsAt":"2026-10-30T11:00:00Z"}`,
		"decoded equal": `{"fingerprint":"synthetic-\u0066ingerprint","startsAt":"2026-10-30T11:00:00\u005a"}`,
	} {
		t.Run(name, func(t *testing.T) {
			id, policy, at, _, _ := responseFixture()
			source, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": identityOrder(raw)}), at)
			if err != nil {
				t.Fatal(err)
			}
			ref := OrderReference{"rem-responder-0123abcd", "1793358000"}
			claims := &snapshotClaims{observations: map[ClaimLookup]ClaimObservation{
				{Identity: id, Order: ref}: {SourceObservation: SourceObservation{Available: true, Source: "synthetic claims", ObservedAt: at}, Claim: &ClaimEvidence{Identity: id, Order: ref, Reference: CanonicalClaimReference{Kind: "OpenProject", ID: "313"}, Owner: "synthetic-owner", State: "resolved", StateAt: at.Add(-time.Second), EvidenceURL: "https://example.test/313"}},
			}}
			policy.Orders, policy.Claims = source, claims
			got := ProjectResponse(context.Background(), id, policy, at)
			if got.OrderCondition != "Available" || got.State != "resolved" || !reflect.DeepEqual(claims.calls, [][]ClaimLookup{{{Identity: id, Order: ref}}}) {
				t.Fatalf("join/claim path: %+v calls=%v", got, claims.calls)
			}
			b, _ := json.Marshal([]any{got, source.Observations()})
			if strings.Contains(string(b), "PRIVATE-ORDER-TEXT") {
				t.Fatal("order text leaked")
			}
		})
	}
}

func TestV1IdentitySafety(t *testing.T) {
	id, policy, at, _, _ := responseFixture()
	raw, _ := json.Marshal(id)
	for _, tc := range []struct {
		name, identity, created string
		duplicate               bool
		want                    string
	}{
		{"fingerprint case", strings.Replace(string(raw), "synthetic-fingerprint", "Synthetic-fingerprint", 1), "", false, "Unjoined"},
		{"fingerprint suffix", strings.Replace(string(raw), "synthetic-fingerprint", "synthetic-fingerprinx", 1), "", false, "Unjoined"},
		{"start character", strings.Replace(string(raw), "11:00:00", "11:00:01", 1), "", false, "Unjoined"},
		{"start equivalent time", strings.Replace(string(raw), "00Z", "00.0Z", 1), "", false, "Unjoined"},
		{"created too early", string(raw), "1793357969", false, "Unjoined"},
		{"created skew boundary", string(raw), "1793357970", false, "Available"},
		{"duplicate", string(raw), "", true, "Unjoined"},
		{"duplicate old assertion", string(raw), "", true, "Unjoined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := identityOrder(tc.identity)
			if tc.created != "" {
				order = strings.Replace(order, "1793358000", tc.created, 1)
			}
			data := map[string]string{"rem-responder-0123abcd": order}
			if tc.duplicate {
				data["esc-responder-0123abcd"] = order
				if tc.name == "duplicate old assertion" {
					data["esc-responder-0123abcd"] = strings.Replace(order, "1793358000", "1793357969", 1)
				}
			}
			s, err := ParseV1ResponderOrders(v1Fixture(t, data), at)
			if err != nil {
				t.Fatal(err)
			}
			policy.Orders = s
			claims := &snapshotClaims{}
			policy.Claims = claims
			got := ProjectResponse(context.Background(), id, policy, at)
			if got.OrderCondition != tc.want {
				t.Fatalf("%+v", got)
			}
			if tc.want != "Available" && (got.State != "Unknown" || got.Order.Key != "Unknown" || len(claims.calls) != 0) {
				t.Fatalf("unsafe join: %+v", got)
			}
		})
	}
}

func TestV1IdentityRefireReusedAttempt(t *testing.T) {
	id, policy, at, _, _ := responseFixture()
	refire := id
	refire.StartsAt = "2026-10-30T11:30:00Z"
	oldRaw, _ := json.Marshal(id)
	newRaw, _ := json.Marshal(refire)
	for _, attempt := range []struct {
		identity string
		created  string
		status   string
		joined   AlertIdentity
	}{
		{string(oldRaw), "1793358000", "done", id},
		{string(newRaw), strconv.FormatInt(at.Add(-30*time.Minute).Unix(), 10), "pending", refire},
	} {
		t.Run(attempt.status, func(t *testing.T) {
			order := strings.Replace(identityOrder(attempt.identity), "1793358000", attempt.created, 1)
			order = strings.Replace(order, "1793358060", attempt.created, 1)
			order = strings.Replace(order, "\"done\"", "\""+attempt.status+"\"", 1)
			s, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": order}), at)
			if err != nil {
				t.Fatal(err)
			}
			policy.Orders = s
			got := ProjectResponses(context.Background(), []AlertIdentity{id, refire}, policy, at)
			for _, query := range []AlertIdentity{id, refire} {
				want := "Unjoined"
				if query == attempt.joined {
					want = "Available"
				}
				if got[query].OrderCondition != want {
					t.Fatalf("attempt %v query %v: %+v", attempt, query, got[query])
				}
				if query == attempt.joined && got[query].Order.Created != attempt.created {
					t.Fatal("old creation inherited")
				}
			}
		})
	}
}

func TestV1IdentityMalformedIsLocal(t *testing.T) {
	id, policy, at, _, _ := responseFixture()
	valid, _ := json.Marshal(id)
	for name, raw := range map[string]string{
		"null": "null", "string": `"context"`, "number": "1", "array": "[]",
		"missing fingerprint": `{"startsAt":"2026-10-30T11:00:00Z"}`,
		"missing start":       `{"fingerprint":"synthetic-fingerprint"}`,
		"fingerprint type":    `{"fingerprint":1,"startsAt":"2026-10-30T11:00:00Z"}`,
		"start type":          `{"fingerprint":"synthetic-fingerprint","startsAt":null}`,
		"extra":               strings.TrimSuffix(string(valid), "}") + `,"extra":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": identityOrder(raw)}), at)
			if err != nil {
				t.Fatal(err)
			}
			// Use the public safe counter, without requiring a new API on the baseline.
			counter, ok := any(s).(interface{ MalformedIdentities() int })
			if !ok || counter.MalformedIdentities() != 1 {
				t.Fatal("malformed identity not counted")
			}
			policy.Orders = s
			got := ProjectResponse(context.Background(), id, policy, at)
			if got.OrderCondition != "Unjoined" || got.State != "Unknown" {
				t.Fatal(got)
			}
			// Another identity remains usable in the same store.
			other := id
			other.Fingerprint = "other"
			otherRaw, _ := json.Marshal(other)
			s, err = ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": identityOrder(raw), "esc-responder-0123abcd": identityOrder(string(otherRaw))}), at)
			if err != nil {
				t.Fatal(err)
			}
			policy.Orders = s
			if got := ProjectResponse(context.Background(), other, policy, at); got.OrderCondition != "Available" {
				t.Fatal(got)
			}
		})
	}
}

func TestV1IdentityAbsentAndIgnored(t *testing.T) {
	id, policy, at, _, _ := responseFixture()
	for _, tc := range []struct{ name, key, order string }{
		{"legacy", "rem-responder-0123abcd", syntheticV1Order},
		{"storm", "rem-responder-0123abcd", strings.Replace(syntheticV1Order, `"source":"responder"`, `"source":"storm"`, 1)},
		{"watchdog", "esc-rem-watchdog-0123abcd", syntheticV1Order},
		{"ignored", "esc-rem-future-0123abcd", syntheticV1Order},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{tc.key: tc.order}), at)
			if err != nil {
				t.Fatal(err)
			}
			policy.Orders = s
			got := ProjectResponse(context.Background(), id, policy, at)
			if got.OrderCondition != "Unjoined" || got.State != "Unknown" {
				t.Fatal(got)
			}
		})
	}
}

// A malformed identity with readable strings is still an assertion: a valid
// order with the same strings must not join alone. It never joins itself.
func TestV1IdentityMalformedReadableStringsStillCount(t *testing.T) {
	id, policy, at, _, _ := responseFixture()
	valid, _ := json.Marshal(id)
	extra := strings.TrimSuffix(string(valid), "}") + `,"extra":true}`
	other := id
	other.Fingerprint = "other"
	otherRaw, _ := json.Marshal(other)
	otherExtra := strings.TrimSuffix(string(otherRaw), "}") + `,"extra":true}`
	for _, tc := range []struct {
		name  string
		data  map[string]string
		query AlertIdentity
		want  string
	}{
		{"valid plus extra-key twin", map[string]string{"rem-responder-0123abcd": identityOrder(string(valid)), "esc-responder-0123abcd": identityOrder(extra)}, id, "Unjoined"},
		{"extra-key twin alone", map[string]string{"esc-responder-0123abcd": identityOrder(extra)}, id, "Unjoined"},
		{"extra-key twin of another identity", map[string]string{"rem-responder-0123abcd": identityOrder(string(valid)), "esc-responder-0123abcd": identityOrder(otherExtra)}, id, "Available"},
		{"unreadable twin", map[string]string{"rem-responder-0123abcd": identityOrder(string(valid)), "esc-responder-0123abcd": identityOrder(`{"fingerprint":1,"startsAt":"2026-10-30T11:00:00Z","extra":true}`)}, id, "Available"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ParseV1ResponderOrders(v1Fixture(t, tc.data), at)
			if err != nil {
				t.Fatal(err)
			}
			policy.Orders = s
			claims := &snapshotClaims{}
			policy.Claims = claims
			got := ProjectResponse(context.Background(), tc.query, policy, at)
			if got.OrderCondition != tc.want {
				t.Fatalf("%+v", got)
			}
			if tc.want != "Available" && (got.State != "Unknown" || got.Order.Key != "Unknown" || len(claims.calls) != 0) {
				t.Fatalf("unsafe join: %+v", got)
			}
		})
	}
}

// The normal promotion path (haynes-ops #4489) copies a parent's alertIdentity
// into its esc-rem child while the parent stays. The pair is a duplicate
// assertion: the Page is Unjoined, with no arbitrary pick and no claim lookup.
func TestV1IdentityEscalatedPromotionPairIsUnjoined(t *testing.T) {
	id, policy, at, _, _ := responseFixture()
	raw, _ := json.Marshal(id)
	parent := identityOrder(string(raw))
	child := strings.Replace(parent, "1793358000", "1793358120", 1)
	child = strings.Replace(child, "1793358060", "1793358180", 1)
	child = strings.Replace(child, `"source":"responder"`, `"source":"escalation"`, 1)
	for name, data := range map[string]map[string]string{
		"parent and child":             {"rem-responder-0123abcd": parent, "esc-rem-0123abcd": child},
		"parent escalated, child done": {"rem-responder-0123abcd": strings.Replace(parent, `"status":"done"`, `"status":"escalated"`, 1), "esc-rem-0123abcd": child},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := ParseV1ResponderOrders(v1Fixture(t, data), at)
			if err != nil {
				t.Fatal(err)
			}
			policy.Orders = s
			claims := &snapshotClaims{}
			policy.Claims = claims
			got := ProjectResponse(context.Background(), id, policy, at)
			if got.OrderCondition != "Unjoined" || got.State != "Unknown" || got.Order.Key != "Unknown" || len(claims.calls) != 0 {
				t.Fatalf("promotion pair joined: %+v", got)
			}
		})
	}
}
