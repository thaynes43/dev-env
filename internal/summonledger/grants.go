package summonledger

import (
	"encoding/json"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// GrantsKey is valid in a ConfigMap but cannot be a CallerPolicy name:
	// policy names match ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$. API ledger writers
	// carry this key through byte-for-byte and never decode it as an Entry.
	GrantsKey    = "_admission"
	GrantsSchema = "dev-env.summon-grants.v1"

	PoolConcurrent  = "concurrent"
	PoolEscalations = "escalations"
)

// Grant reserves capacity before admittedAt is written on the Session. The
// UID prevents a deleted Session's grant from applying to its replacement.
// At is also the admission time: recovery never extends the start deadline.
type Grant struct {
	Name, UID, Lane, Caller, Pool string
	At                            time.Time
	extra                         map[string]json.RawMessage
}

// Grants is the reserved key's document. Unknown fields survive writes,
// just as they do in caller entries, including during a rolling upgrade.
type Grants struct {
	Items []Grant
	extra map[string]json.RawMessage
}

// DecodeGrants treats an absent key as no grants. A malformed present key
// holds all admission; it must never silently turn occupied capacity free.
func DecodeGrants(raw string, present bool) (Grants, error) {
	var out Grants
	if !present {
		return out, nil
	}
	if err := checkMembersUnique([]byte(raw)); err != nil {
		return out, err
	}
	top, err := decodeObject([]byte(raw))
	if err != nil {
		return out, err
	}
	var schema string
	var items []json.RawMessage
	if takeMember(top, "schema", &schema) != nil || schema != GrantsSchema || takeMember(top, "grants", &items) != nil {
		return out, ErrUnreadable
	}
	seen := map[string]bool{}
	for _, item := range items {
		fields, err := decodeObject(item)
		if err != nil {
			return out, err
		}
		var g Grant
		if takeMember(fields, "name", &g.Name) != nil || len(validation.IsDNS1123Label(g.Name)) != 0 ||
			takeMember(fields, "uid", &g.UID) != nil || g.UID == "" ||
			takeMember(fields, "lane", &g.Lane) != nil ||
			takeMember(fields, "caller", &g.Caller) != nil || len(validation.IsDNS1123Subdomain(g.Caller)) != 0 ||
			takeMember(fields, "pool", &g.Pool) != nil || takeTime(fields, "at", &g.At) != nil {
			return out, ErrUnreadable
		}
		switch g.Lane {
		case "remediation", "upgrade", "curation":
			if g.Pool != PoolConcurrent {
				return out, ErrUnreadable
			}
		case "escalation":
			if g.Pool != PoolEscalations {
				return out, ErrUnreadable
			}
		default:
			return out, ErrUnreadable
		}
		if seen[g.UID] {
			return out, ErrUnreadable
		}
		seen[g.UID] = true
		g.extra = fields
		out.Items = append(out.Items, g)
	}
	out.extra = top
	return out, nil
}

// EncodeGrants writes only the reserved document. The ConfigMap's own size
// limit applies; an unsuccessful Update never authorizes an admission.
func EncodeGrants(grants Grants) (string, error) {
	top := map[string]any{}
	for k, v := range grants.extra {
		top[k] = v
	}
	items := make([]map[string]any, 0, len(grants.Items))
	for _, g := range grants.Items {
		m := map[string]any{}
		for k, v := range g.extra {
			m[k] = v
		}
		m["name"], m["uid"], m["lane"] = g.Name, g.UID, g.Lane
		m["caller"], m["pool"], m["at"] = g.Caller, g.Pool, g.At.UTC()
		items = append(items, m)
	}
	top["schema"], top["grants"] = GrantsSchema, items
	b, err := json.Marshal(top)
	return string(b), err
}
