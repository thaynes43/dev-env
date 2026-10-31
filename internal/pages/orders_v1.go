package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	V1OrdersMaxBytes      = 2 << 20
	V1OrdersMaxKeys       = 1000
	V1OrdersMaxOrderBytes = 64 << 10
	v1OrdersSource        = "ConfigMap upgrade-agent/upgrade-work-orders"
)

var errV1Orders = errors.New("invalid or over-limit V1 work-order ConfigMap snapshot")

var v1ConfigMapDataKey = regexp.MustCompile(`^[-._a-zA-Z0-9]{1,253}$`)

// V1ResponderOrders is an immutable, caller-supplied offline snapshot. The real
// producer contract optionally supplies an exact alertIdentity binding.
// It holds only the allowed order subset, never order text or label signatures.
type V1ResponderOrders struct {
	observation         SourceObservation
	orders              map[string]ResponderOrder
	invalid             bool
	ignored             int
	malformedIdentities int
	// malformedClaims counts malformed identities whose fingerprint and startsAt
	// are still readable strings. They never join, but they are assertions.
	malformedClaims map[AlertIdentity]int
}

// ParseV1ResponderOrders reads kubectl's ConfigMap JSON, with an explicit caller
// assertion of capture time. On failure it returns an Invalid source and a safe
// error, never a partially usable snapshot. No clock, file or network is read.
func ParseV1ResponderOrders(raw []byte, observedAt time.Time) (*V1ResponderOrders, error) {
	// A failed parse keeps Available with the caller's capture time; projection
	// checks the record's Invalid condition before freshness.
	s := &V1ResponderOrders{observation: SourceObservation{Available: true, Source: v1OrdersSource, ObservedAt: observedAt}, invalid: true}
	fail := func() (*V1ResponderOrders, error) { return s, errV1Orders }
	if len(raw) > V1OrdersMaxBytes || !knownResponseTime(observedAt) {
		return fail()
	}
	top, err := v1OrderObject(raw)
	if err != nil || !v1RejectCaseAliases(top, "apiVersion", "kind", "metadata", "data", "binaryData") || top["apiVersion"] != "v1" || top["kind"] != "ConfigMap" {
		return fail()
	}
	meta, ok := top["metadata"].(map[string]any)
	if !ok || !v1RejectCaseAliases(meta, "name", "namespace", "resourceVersion") || meta["name"] != "upgrade-work-orders" || meta["namespace"] != "upgrade-agent" {
		return fail()
	}
	if rv, present := meta["resourceVersion"]; present {
		text, ok := rv.(string)
		if !ok || len(text) > 128 || !responseText(text) {
			return fail()
		}
		s.observation.Source += "@" + text
	}
	if binary, present := top["binaryData"]; present {
		m, ok := binary.(map[string]any)
		if !ok || len(m) != 0 {
			return fail()
		}
	}
	data := map[string]any{}
	if value, present := top["data"]; present {
		data, ok = value.(map[string]any)
		if !ok {
			return fail()
		}
	}
	if len(data) > V1OrdersMaxKeys {
		return fail()
	}
	orders := make(map[string]ResponderOrder)
	for key, value := range data {
		text, ok := value.(string)
		if !ok || len(text) > V1OrdersMaxOrderBytes || !v1ConfigMapDataKey.MatchString(key) {
			return fail()
		}
		entry, err := v1OrderObject([]byte(text))
		if err != nil {
			return fail()
		}
		// The store also contains other work lanes and digest bookkeeping. Validate
		// their JSON shape but do not apply responder status/timestamp semantics.
		// A key under a responder prefix that is not a known responder order key
		// (for example a future producer's key) is unrelated: it is counted, never
		// trusted, and cannot invalidate the other orders.
		if !validOrderKey(key) {
			for _, prefix := range []string{"rem-responder-", "esc-responder-", "esc-rem-"} {
				if strings.HasPrefix(key, prefix) {
					s.ignored++
					break
				}
			}
			continue
		}
		if !v1RejectCaseAliases(entry, "status", "created", "updated") {
			return fail()
		}
		status, statusOK := entry["status"].(string)
		created, createdOK := entry["created"].(string)
		updated, updatedOK := entry["updated"].(string)
		ct, cok := orderTime(created)
		ut, uok := orderTime(updated)
		if !statusOK || !createdOK || !updatedOK || !validOrderStatus(status) || !cok || !uok || ut.Before(ct) || ut.After(observedAt.Add(orderClockSkew)) {
			return fail()
		}
		identity := AlertIdentity{}
		if value, present := entry["alertIdentity"]; present {
			obj, ok := value.(map[string]any)
			fingerprint, fingerprintOK := obj["fingerprint"].(string)
			startsAt, startsAtOK := obj["startsAt"].(string)
			if !ok || len(obj) != 2 || !fingerprintOK || !startsAtOK {
				s.malformedIdentities++
				// An extra key makes the identity unusable, not unasserted: another
				// order with the same strings must not join alone.
				if ok && fingerprintOK && startsAtOK {
					if s.malformedClaims == nil {
						s.malformedClaims = map[AlertIdentity]int{}
					}
					s.malformedClaims[AlertIdentity{Fingerprint: fingerprint, StartsAt: startsAt}]++
				}
				continue
			}
			identity = AlertIdentity{Fingerprint: fingerprint, StartsAt: startsAt}
		}
		orders[key] = ResponderOrder{Identity: identity, Order: OrderReference{Key: key, Created: created}, Status: status, Updated: updated}
	}
	s.orders, s.invalid = orders, false
	return s, nil
}

// IgnoredKeys counts data keys under a responder prefix that matched no known
// responder order key grammar. They contribute nothing to the snapshot.
func (s *V1ResponderOrders) IgnoredKeys() int { return s.ignored }

// MalformedIdentities counts orders excluded for an unusable alertIdentity.
// Absence is legacy data, not malformed. Existing whole-store bounds still apply.
func (s *V1ResponderOrders) MalformedIdentities() int { return s.malformedIdentities }

// Observations returns independent copies of the safe subset keyed by data key.
// A stored identity is not itself proof that the order can join an episode.
func (s *V1ResponderOrders) Observations() map[string]OrderObservation {
	out := make(map[string]OrderObservation, len(s.orders))
	for key, order := range s.orders {
		copy := order
		out[key] = OrderObservation{SourceObservation: s.observation, Order: &copy, RecordCondition: "Unjoined"}
	}
	return out
}

// FindOrders reads one bounded in-memory batch. Only decoded string equality
// on both alertIdentity fields binds an order. Duplicate identities and attempts
// created before the episode (allowing clock skew) remain unjoined. A malformed
// identity with readable fingerprint and startsAt strings still counts as an
// assertion for duplicates, though it can never join.
func (s *V1ResponderOrders) FindOrders(ctx context.Context, identities []AlertIdentity) (map[AlertIdentity]OrderObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(identities) > MaxResponseBatch {
		return nil, errResponseBatchTooLarge
	}
	condition := ""
	if s.invalid {
		condition = "Invalid"
	} else if len(s.orders) > 0 || s.ignored > 0 || s.malformedIdentities > 0 {
		condition = "Unjoined"
	}
	// Count every identity assertion before checking creation time: even an old
	// or unusable attempt conflicts with another order claiming the same identity.
	byIdentity := make(map[AlertIdentity]ResponderOrder, len(s.orders))
	counts := make(map[AlertIdentity]int, len(s.orders))
	if !s.invalid {
		for _, order := range s.orders {
			if !validResponseIdentity(order.Identity) {
				continue
			}
			counts[order.Identity]++
			byIdentity[order.Identity] = order
		}
		for id, n := range s.malformedClaims {
			counts[id] += n
		}
	}
	out := make(map[AlertIdentity]OrderObservation, len(identities))
	for _, id := range identities {
		observation := OrderObservation{SourceObservation: s.observation, RecordCondition: condition}
		if counts[id] == 1 {
			order := byIdentity[id]
			created, _ := orderTime(order.Order.Created)
			start, known := SnapshotTime(id.StartsAt)
			if known && !created.Before(start.Add(-orderClockSkew)) {
				observation.Order = &order
				observation.RecordCondition = ""
			}
		}
		out[id] = observation
	}
	return out, nil
}

// Consumed fields are case-sensitive. Unknown fields are bounded and ignored;
// this only rejects case aliases of the consumed names, which must not be
// silently accepted by encoding/json's struct matching.
func v1RejectCaseAliases(obj map[string]any, fields ...string) bool {
	for key := range obj {
		for _, field := range fields {
			if strings.EqualFold(key, field) && key != field {
				return false
			}
		}
	}
	return true
}

func v1OrderObject(raw []byte) (map[string]any, error) {
	if !utf8.Valid(raw) {
		return nil, errV1Orders
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := decodeV1OrderValue(d, 0)
	obj, ok := value.(map[string]any)
	if err != nil || !ok || d.Decode(new(any)) != io.EOF {
		return nil, errV1Orders
	}
	return obj, nil
}

// Token decoding refuses duplicate keys before a map could overwrite them and
// bounds ignored fields too. Error messages never include untrusted order text.
func decodeV1OrderValue(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, errV1Orders
	}
	token, err := d.Token()
	if err != nil {
		return nil, errV1Orders
	}
	if text, ok := token.(string); ok && len(text) > V1OrdersMaxOrderBytes {
		return nil, errV1Orders
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return nil, errV1Orders
			}
			key, ok := token.(string)
			if !ok || len(key) > V1OrdersMaxOrderBytes || len(obj) >= V1OrdersMaxKeys {
				return nil, errV1Orders
			}
			if _, seen := obj[key]; seen {
				return nil, errV1Orders
			}
			value, err := decodeV1OrderValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			obj[key] = value
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errV1Orders
		}
		return obj, nil
	case '[':
		array := []any{}
		for d.More() {
			if len(array) >= V1OrdersMaxKeys {
				return nil, errV1Orders
			}
			value, err := decodeV1OrderValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errV1Orders
		}
		return array, nil
	}
	return nil, errV1Orders
}
