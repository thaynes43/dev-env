package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func v1Fixture(t *testing.T, data map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]string{"name": "upgrade-work-orders", "namespace": "upgrade-agent", "resourceVersion": "12345"}, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const syntheticV1Order = `{"source":"responder","reason":"PRIVATE-ORDER-TEXT","run_ref":"synthetic-run","diagnosis":"PRIVATE-ORDER-TEXT","alert":"PRIVATE-ORDER-TEXT","sig":"Example@synthetic","class":"remediation","status":"done","created":"1793358000","updated":"1793358060","note":"PRIVATE-ORDER-TEXT","digested":"yes"}`

func TestV1OrdersRealShape(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	data := map[string]string{"rem-responder-0123abcd": syntheticV1Order, "esc-responder-0123abcd": syntheticV1Order, "esc-rem-0123abcd": syntheticV1Order, "esc-rem-watchdog-0123abcd": syntheticV1Order, "wo-example": `{"created":1793358000,"status":"done"}`, "digest.last": `{"last_flush":"1793358060"}`}
	source, err := ParseV1ResponderOrders(v1Fixture(t, data), at)
	if err != nil {
		t.Fatal(err)
	}
	obs := source.Observations()
	if len(obs) != 4 || obs["esc-rem-watchdog-0123abcd"].Order == nil || source.IgnoredKeys() != 0 {
		t.Fatal(obs)
	}
	for key, o := range obs {
		if o.Order.Order.Key != key || o.Order.Order.Created != "1793358000" || o.Order.Updated != "1793358060" || o.Order.Status != "done" || o.Order.Identity != (AlertIdentity{}) || !strings.Contains(o.Source, "12345") {
			t.Fatal(o)
		}
	}
	// Returned observations cannot mutate the immutable batch source.
	obs["rem-responder-0123abcd"].Order.Status = "failed"
	if source.Observations()["rem-responder-0123abcd"].Order.Status != "done" {
		t.Fatal("mutable snapshot")
	}
	b, _ := json.Marshal(source.Observations())
	if strings.Contains(string(b), "PRIVATE-ORDER-TEXT") {
		t.Fatal("order text escaped allowlist")
	}
	id := AlertIdentity{"synthetic", "2026-10-30T11:00:00Z"}
	got := ProjectResponse(context.Background(), id, ResponseSources{Orders: source, MaxAge: 5 * time.Minute}, at)
	if got.OrderCondition != "Unjoined" || got.Order.Key != "Unknown" || got.State != "Unknown" {
		t.Fatal(got)
	}
}

func TestV1OrdersRefireAndReusedKey(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ids := []AlertIdentity{{"synthetic", "2026-10-30T11:00:00Z"}, {"synthetic", "2026-10-30T11:30:00Z"}}
	for _, order := range []string{syntheticV1Order, strings.NewReplacer(`"done"`, `"pending"`, `"1793358000"`, `"1793358060"`).Replace(syntheticV1Order), strings.TrimSuffix(syntheticV1Order, "}") + `,"fingerprint":"synthetic","startsAt":"2026-10-30T11:00:00Z","incidentHash":"invented"}`} {
		source, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": order}), at)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range ProjectResponses(context.Background(), ids, ResponseSources{Orders: source, MaxAge: 5 * time.Minute}, at) {
			if r.OrderCondition != "Unjoined" || r.Order.Key != "Unknown" || r.State != "Unknown" {
				t.Fatal(r)
			}
		}
	}
}

func TestV1OrdersRefuseMalformed(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for name, order := range map[string]string{
		"broken": "{", "double encoded": `"{}"`, "array": `[]`, "null": `null`, "missing": `{}`,
		"numeric created": strings.Replace(syntheticV1Order, `"1793358000"`, `1793358000`, 1),
		"null status":     strings.Replace(syntheticV1Order, `"status":"done"`, `"status":null`, 1),
		"status":          strings.Replace(syntheticV1Order, `"done"`, `"working"`, 1),
		"duplicate":       strings.TrimSuffix(syntheticV1Order, "}") + `,"created":"1793358000"}`,
		"case alias":      strings.TrimSuffix(syntheticV1Order, "}") + `,"Created":"1793358000"}`,
		"reversed time":   strings.Replace(syntheticV1Order, `"1793358060"`, `"1793357999"`, 1),
		"future":          strings.Replace(syntheticV1Order, `"1793358060"`, `"1993358060"`, 1),
		"noncanonical":    strings.Replace(syntheticV1Order, `"1793358000"`, `"01793358000"`, 1),
		"trailing":        syntheticV1Order + `{}`, "deep": `{"x":` + strings.Repeat(`[`, 17) + `0` + strings.Repeat(`]`, 17) + `}`,
		"order bytes": `{"x":"` + strings.Repeat("x", V1OrdersMaxOrderBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": order}), at)
			if err == nil {
				t.Fatal("accepted malformed order")
			}
			if s == nil || len(s.Observations()) != 0 {
				t.Fatal("partial snapshot")
			}
			r := ProjectResponse(context.Background(), AlertIdentity{"synthetic", "2026-10-30T11:00:00Z"}, ResponseSources{Orders: s, MaxAge: 5 * time.Minute}, at)
			if r.OrderCondition != "Invalid" {
				t.Fatal(r)
			}
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("input in error")
			}
		})
	}
}

func TestV1OrdersBoundsAndEnvelope(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	data := map[string]string{}
	for i := 0; i < V1OrdersMaxKeys; i++ {
		data[fmt.Sprintf("other-%04d", i)] = `{}`
	}
	if _, err := ParseV1ResponderOrders(v1Fixture(t, data), at); err != nil {
		t.Fatal("key boundary", err)
	}
	data["one-more"] = `{}`
	emptyCM := string(v1Fixture(t, map[string]string{}))
	padded := []byte(emptyCM + strings.Repeat(" ", V1OrdersMaxBytes-len(emptyCM)))
	if _, err := ParseV1ResponderOrders(padded, at); err != nil {
		t.Fatal("byte boundary", err)
	}
	paddedOrder := syntheticV1Order + strings.Repeat(" ", V1OrdersMaxOrderBytes-len(syntheticV1Order))
	if _, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": paddedOrder}), at); err != nil {
		t.Fatal("order byte boundary", err)
	}
	for name, raw := range map[string][]byte{
		"keys": v1Fixture(t, data), "bytes": append(padded, ' '),
		"wrong name":      []byte(strings.Replace(emptyCM, `"name":"upgrade-work-orders"`, `"name":"wrong"`, 1)),
		"wrong namespace": []byte(strings.Replace(emptyCM, `"namespace":"upgrade-agent"`, `"namespace":"wrong"`, 1)),
		"wrong kind":      []byte(strings.Replace(emptyCM, `"kind":"ConfigMap"`, `"kind":"Secret"`, 1)),
		"null data":       []byte(strings.Replace(emptyCM, `"data":{}`, `"data":null`, 1)),
		"data type":       []byte(strings.Replace(emptyCM, `"data":{}`, `"data":{"a":{}}`, 1)),
		"case alias":      []byte(strings.TrimSuffix(emptyCM, "}") + `,"Data":{}}`),
		"duplicate":       []byte(strings.TrimSuffix(emptyCM, "}") + `,"kind":"ConfigMap"}`),
		"trailing":        []byte(emptyCM + `{}`),
		"bad unrelated":   v1Fixture(t, map[string]string{"digest.last": "{"}),
		"bad key":         v1Fixture(t, map[string]string{"rem-responder-0123abcd/x": syntheticV1Order}),
		"illegal key":     v1Fixture(t, map[string]string{"illegal/key": `{}`}),
		"bad version":     []byte(strings.Replace(emptyCM, `"resourceVersion":"12345"`, `"resourceVersion":12345`, 1)),
		"array bound":     v1Fixture(t, map[string]string{"digest.last": `{"x":[` + strings.Repeat(`0,`, V1OrdersMaxKeys) + `0]}`}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseV1ResponderOrders(raw, at); err == nil {
				t.Fatal("accepted invalid envelope")
			}
		})
	}

}

func TestV1OrdersUnknownConditions(t *testing.T) {
	id, sources, at, _, claims := responseFixture()
	valid, _ := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": syntheticV1Order}), at)
	empty, _ := ParseV1ResponderOrders(v1Fixture(t, map[string]string{}), at)
	invalid, _ := ParseV1ResponderOrders([]byte(`broken`), at)
	stale, _ := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": syntheticV1Order}), at.Add(-10*time.Minute))
	for _, tc := range []struct {
		name   string
		source ResponderOrderSource
		at     time.Time
		want   string
	}{
		{"unconfigured", nil, at, "Unconfigured"},
		{"unavailable", &fakeResponderOrders{err: errors.New("read failed")}, at, "Unavailable"}, {"empty", empty, at, "Empty"}, {"invalid", invalid, at, "Invalid"}, {"unjoined", valid, at, "Unjoined"}, {"stale", stale, at, "Stale"},
		{"future capture", valid, at.Add(-time.Minute), "Invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources.Orders = tc.source
			r := ProjectResponse(context.Background(), id, sources, tc.at)
			if r.OrderCondition != tc.want || r.State != "Unknown" || r.Order.Key != "Unknown" {
				t.Fatal(r)
			}
			if len(claims.lookups) != 0 {
				t.Fatal("claim lookup without join")
			}
		})
	}
	// Mismatched is canonical evidence disagreeing with an explicitly joined order.
	id, sources, at, _, claims = responseFixture()
	claims.observation.Claim.State = "resolved"
	if r := ProjectResponse(context.Background(), id, sources, at); r.ClaimCondition != "Mismatched" || r.State != "Unknown" {
		t.Fatal(r)
	}
}

func TestV1OrdersSourceBatchBounds(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	source, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{}), at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.FindOrders(context.Background(), make([]AlertIdentity, MaxResponseBatch+1)); err == nil {
		t.Fatal("unbounded lookup")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.FindOrders(ctx, []AlertIdentity{{"synthetic", "2026-10-30T11:00:00Z"}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestV1OrdersRecordConditionFailsClosed(t *testing.T) {
	for _, condition := range []string{"Unjoined", "Invalid", "Available", "resolved"} {
		id, sources, at, orders, claims := responseFixture()
		orders.observation.RecordCondition = condition
		want := condition
		if condition != "Unjoined" && condition != "Invalid" {
			want = "Invalid"
		}
		r := ProjectResponse(context.Background(), id, sources, at)
		if r.OrderCondition != want || r.Order.Key != "Unknown" || r.State != "Unknown" || len(claims.lookups) != 0 {
			t.Fatal(r)
		}
	}
}

// A key under a responder prefix that is not a known producer key is counted
// and ignored; it must never turn the rest of the store Invalid.
func TestV1OrdersUnknownResponderKeyIgnored(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	data := map[string]string{
		"rem-responder-0123abcd": syntheticV1Order,
		"rem-responder-zzzzzzzz": syntheticV1Order, "esc-rem-future-0123abcd": syntheticV1Order,
		"esc-responder-0123ABCD": syntheticV1Order, "esc-rem-watchdog-0123abc": syntheticV1Order,
	}
	source, err := ParseV1ResponderOrders(v1Fixture(t, data), at)
	if err != nil {
		t.Fatal(err)
	}
	obs := source.Observations()
	if len(obs) != 1 || obs["rem-responder-0123abcd"].Order == nil || source.IgnoredKeys() != 4 {
		t.Fatal(len(obs), source.IgnoredKeys())
	}
	r := ProjectResponse(context.Background(), AlertIdentity{"synthetic", "2026-10-30T11:00:00Z"}, ResponseSources{Orders: source, MaxAge: 5 * time.Minute}, at)
	if r.OrderCondition != "Unjoined" {
		t.Fatal(r)
	}
	// Only unknown keys leave an observed store with no responder orders.
	only, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"esc-rem-future-0123abcd": syntheticV1Order}), at)
	if err != nil || len(only.Observations()) != 0 || only.IgnoredKeys() != 1 {
		t.Fatal(err)
	}
	// An ignored value must still be bounded JSON.
	if _, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"esc-rem-future-0123abcd": "{"}), at); err == nil {
		t.Fatal("accepted malformed ignored value")
	}
}

// A malformed file reports Invalid however old its capture time is.
func TestV1OrdersMalformedAndStaleIsInvalid(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	bad, err := ParseV1ResponderOrders([]byte(`broken`), at.Add(-time.Hour))
	if err == nil {
		t.Fatal("accepted malformed file")
	}
	id := AlertIdentity{"synthetic", "2026-10-30T11:00:00Z"}
	r := ProjectResponse(context.Background(), id, ResponseSources{Orders: bad, MaxAge: 5 * time.Minute}, at)
	if r.OrderCondition != "Invalid" || r.State != "Unknown" {
		t.Fatal(r)
	}
	good, _ := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": syntheticV1Order}), at)
	if r := ProjectResponse(context.Background(), id, ResponseSources{Orders: good, MaxAge: 5 * time.Minute}, at.Add(time.Hour)); r.OrderCondition != "Stale" {
		t.Fatal(r)
	}
}

// An order written after the capture instant (between a read and the capture,
// or by a producer clock ahead of the caller) is tolerated up to a fixed skew.
func TestV1OrdersClockSkew(t *testing.T) {
	created := time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)
	capture := created.Add(time.Hour)
	order := func(updated time.Time) string {
		return fmt.Sprintf(`{"status":"done","created":"%d","updated":"%d"}`, created.Unix(), updated.Unix())
	}
	for _, tc := range []struct {
		skew time.Duration
		ok   bool
	}{{0, true}, {30 * time.Second, true}, {31 * time.Second, false}} {
		s, err := ParseV1ResponderOrders(v1Fixture(t, map[string]string{"rem-responder-0123abcd": order(capture.Add(tc.skew))}), capture)
		if (err == nil) != tc.ok {
			t.Fatal(tc.skew, err)
		}
		if tc.ok {
			r := ProjectResponse(context.Background(), AlertIdentity{"synthetic", "2026-10-30T10:00:00Z"}, ResponseSources{Orders: s, MaxAge: 5 * time.Minute}, capture)
			if r.OrderCondition != "Unjoined" {
				t.Fatal(tc.skew, r)
			}
		}
	}
}
