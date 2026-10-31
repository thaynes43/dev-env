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
// producers have no explicit alert identity field, so it cannot join any alert.
// It holds only the allowed order subset, never order text or label signatures.
type V1ResponderOrders struct {
	observation SourceObservation
	orders      map[string]ResponderOrder
	invalid     bool
}

// ParseV1ResponderOrders reads kubectl's ConfigMap JSON, with an explicit caller
// assertion of capture time. On failure it returns an Invalid source and a safe
// error, never a partially usable snapshot. No clock, file or network is read.
func ParseV1ResponderOrders(raw []byte, observedAt time.Time) (*V1ResponderOrders, error) {
	s := &V1ResponderOrders{observation: SourceObservation{Available: true, Source: v1OrdersSource, ObservedAt: observedAt}, invalid: true}
	fail := func() (*V1ResponderOrders, error) { return s, errV1Orders }
	if len(raw) > V1OrdersMaxBytes || !knownResponseTime(observedAt) {
		return fail()
	}
	top, err := v1OrderObject(raw)
	if err != nil || !v1ExactFields(top, "apiVersion", "kind", "metadata", "data", "binaryData") || top["apiVersion"] != "v1" || top["kind"] != "ConfigMap" {
		return fail()
	}
	meta, ok := top["metadata"].(map[string]any)
	if !ok || !v1ExactFields(meta, "name", "namespace", "resourceVersion") || meta["name"] != "upgrade-work-orders" || meta["namespace"] != "upgrade-agent" {
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
		if !validOrderKey(key) {
			for _, prefix := range []string{"rem-responder-", "esc-responder-", "esc-rem-"} {
				if strings.HasPrefix(key, prefix) {
					return fail()
				}
			}
			continue
		}
		if !v1ExactFields(entry, "status", "created", "updated") {
			return fail()
		}
		status, statusOK := entry["status"].(string)
		created, createdOK := entry["created"].(string)
		updated, updatedOK := entry["updated"].(string)
		ct, cok := orderTime(created)
		ut, uok := orderTime(updated)
		if !statusOK || !createdOK || !updatedOK || !validOrderStatus(status) || !cok || !uok || ut.Before(ct) || ut.After(observedAt) {
			return fail()
		}
		orders[key] = ResponderOrder{Order: OrderReference{Key: key, Created: created}, Status: status, Updated: updated}
	}
	s.orders, s.invalid = orders, false
	return s, nil
}

// Observations returns independent copies keyed by the actual ConfigMap data
// key. An identity is intentionally absent; these are not alert associations.
func (s *V1ResponderOrders) Observations() map[string]OrderObservation {
	out := make(map[string]OrderObservation, len(s.orders))
	for key, order := range s.orders {
		copy := order
		out[key] = OrderObservation{SourceObservation: s.observation, Order: &copy, RecordCondition: "Unjoined"}
	}
	return out
}

// FindOrders reads one bounded in-memory batch. No current V1 field supplies an
// exact fingerprint/start association. Even supplied lookalike fields are not a
// supported producer contract; parsing free text or inventing a join is unsafe.
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
	} else if len(s.orders) > 0 {
		condition = "Unjoined"
	}
	out := make(map[AlertIdentity]OrderObservation, len(identities))
	for _, id := range identities {
		out[id] = OrderObservation{SourceObservation: s.observation, RecordCondition: condition}
	}
	return out, nil
}

// Consumed fields are case-sensitive. Unknown fields are bounded and ignored;
// aliases must not be silently accepted by encoding/json's struct matching.
func v1ExactFields(obj map[string]any, fields ...string) bool {
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
