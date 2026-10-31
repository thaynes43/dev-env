package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestV1Lineage(t *testing.T) {
	id, policy, at, _, canonical := responseFixture()
	raw, _ := json.Marshal(id)
	const root = "rem-responder-0123abcd"
	const child = "esc-rem-0123abcd"
	const leaf = "esc-responder-0123abcd"
	const missing = "rem-responder-ffffffff"
	base := identityOrder(string(raw))
	parent := func(key, created string) string {
		b, _ := json.Marshal(OrderReference{Key: key, Created: created})
		return string(b)
	}
	withParent := func(raw string) string { return strings.TrimSuffix(base, "}") + `,"parentOrder":` + raw + `}` }
	cases := []struct {
		name      string
		data      map[string]string
		want      []string
		malformed int
	}{
		{"parent child", map[string]string{root: base, child: withParent(parent(root, "1793358000"))}, []string{root, child}, 0},
		{"three levels", map[string]string{root: base, child: withParent(parent(root, "1793358000")), leaf: withParent(parent(child, "1793358000"))}, []string{root, child, leaf}, 0},
		{"timestamps do not rank descendants", map[string]string{root: strings.ReplaceAll(base, "1793358000", "1793358060"), child: withParent(parent(root, "1793358060"))}, []string{root, child}, 0},
		{"singleton root", map[string]string{root: base}, []string{root}, 0},
		{"parent belongs to another identity", map[string]string{root: strings.Replace(base, "synthetic-fingerprint", "other-fingerprint", 1), child: withParent(parent(root, "1793358000"))}, nil, 0},
		{"malformed lineage on other identity is local", map[string]string{root: base, child: strings.Replace(withParent("null"), "synthetic-fingerprint", "other-fingerprint", 1)}, []string{root}, 1},
		{"empty reference", map[string]string{root: base, child: withParent(parent("", ""))}, nil, 0},
		{"missing parent", map[string]string{root: base, child: withParent(parent(missing, "1793358000"))}, nil, 0},
		{"singleton missing parent", map[string]string{child: withParent(parent(missing, "1793358000"))}, nil, 0},
		{"created mismatch", map[string]string{root: base, child: withParent(parent(root, "01793358000"))}, nil, 0},
		{"fork", map[string]string{root: base, child: withParent(parent(root, "1793358000")), leaf: withParent(parent(root, "1793358000"))}, nil, 0},
		{"cycle", map[string]string{root: withParent(parent(child, "1793358000")), child: withParent(parent(root, "1793358000"))}, nil, 0},
		{"disconnected cycle", map[string]string{root: base, child: withParent(parent(leaf, "1793358000")), leaf: withParent(parent(child, "1793358000"))}, nil, 0},
		{"unrelated duplicate", map[string]string{root: base, child: withParent(parent(root, "1793358000")), leaf: base}, nil, 0},
	}
	for name, bad := range map[string]string{
		"null": "null", "array": "[]", "string": `"x"`, "number": "1",
		"missing key": `{"created":"1793358000"}`, "missing created": `{"key":"` + root + `"}`,
		"key type": `{"key":1,"created":"1793358000"}`, "created type": `{"key":"` + root + `","created":null}`,
		"extra": `{"key":"` + root + `","created":"1793358000","extra":true}`,
	} {
		cases = append(cases, struct {
			name      string
			data      map[string]string
			want      []string
			malformed int
		}{"malformed " + name, map[string]string{root: base, child: withParent(bad)}, nil, 1})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ParseV1ResponderOrders(v1Fixture(t, tc.data), at)
			if err != nil {
				t.Fatal(err)
			}
			if tc.malformed > 0 {
				c, ok := any(s).(interface{ MalformedLineages() int })
				if !ok || c.MalformedLineages() != tc.malformed {
					t.Error("malformed lineage not counted")
				}
			}
			if tc.malformed > 0 {
				// A malformed lineage cannot become an implicit standalone root.
				for key, value := range tc.data {
					if key == root {
						continue
					}
					singleton, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{key: value}), at)
					if err != nil {
						t.Fatal(err)
					}
					policy.Orders = singleton
					if r := ProjectResponse(context.Background(), id, policy, at); r.OrderCondition != "Unjoined" {
						t.Fatalf("malformed singleton joined: %+v", r)
					}
				}
			}
			policy.Orders = s
			claims := &snapshotClaims{}
			policy.Claims = claims
			if len(tc.want) > 0 {
				ref := OrderReference{Key: tc.want[len(tc.want)-1], Created: "1793358000"}
				observation := canonical.observation
				evidence := *observation.Claim
				evidence.Order = ref
				evidence.State = "resolved"
				observation.Claim = &evidence
				claims.observations = map[ClaimLookup]ClaimObservation{{Identity: id, Order: ref}: observation}
			}
			got := ProjectResponse(context.Background(), id, policy, at)
			if tc.want == nil {
				if got.OrderCondition != "Unjoined" || got.State != "Unknown" || got.Order.Key != "Unknown" || len(claims.calls) != 0 {
					t.Fatalf("unsafe join: %+v", got)
				}
				return
			}
			if got.OrderCondition != "Available" || got.State != "resolved" || got.Order.Key != tc.want[len(tc.want)-1] {
				t.Fatalf("leaf not joined: %+v", got)
			}
			encoded, _ := json.Marshal(got)
			var projection struct {
				OrderChain []string `json:"orderChain"`
			}
			if err := json.Unmarshal(encoded, &projection); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(projection.OrderChain, tc.want) {
				t.Fatalf("chain %v, want %v", projection.OrderChain, tc.want)
			}
			snap := AlertmanagerSnapshot{Alerts: []AlertmanagerAlert{{Response: &got}}}
			var rendered bytes.Buffer
			if err := RenderAlertmanagerHTML(&rendered, snap); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(rendered.String(), "Order chain (root to leaf): "+strings.Join(tc.want, " ")) {
				t.Fatal("HTML chain missing")
			}
			if len(claims.calls) != 1 || len(claims.calls[0]) != 1 || claims.calls[0][0].Order.Key != got.Order.Key {
				t.Fatalf("leaf claim lookup: %+v", claims.calls)
			}
		})
	}
}
