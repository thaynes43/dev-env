package keeper

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"time"

	kubejson "sigs.k8s.io/json"
)

const (
	claudeProvenanceKey = "refresh-provenance.json"
	claudeHistoryLimit  = 3
	claudeProvenanceMax = 2048
)

var errClaudeProvenance = errors.New("claude refresh provenance is invalid")

type claudeOperation uint8

const (
	claudeAdoption         claudeOperation = 1
	claudeScheduledRefresh claudeOperation = 2
	// Operation code 3 is reserved for keeper-only early recovery.
)

type claudeExpirySource uint8

const (
	claudeStagedExpiry   claudeExpirySource = 1 // absolute timestamp on the staged login
	claudeResponseExpiry claudeExpirySource = 2 // response time plus the provider's lifetime
)

// These are the only values that can enter the provenance key. Time fields
// use positive Unix nanoseconds, with zero meaning unknown. An adoption's
// lifetime measures what remained when the staged login was read. Its request
// timestamps refer to profile verification.
type claudeTransition struct {
	Generation           uint64             `json:"generation"`
	IncarnationHigh      uint64             `json:"incarnationHigh"`
	IncarnationLow       uint64             `json:"incarnationLow"`
	Operation            claudeOperation    `json:"operation"`
	DispatchedAt         int64              `json:"dispatchedAtUnixNano"`
	RespondedAt          int64              `json:"respondedAtUnixNano"`
	PublishedAt          int64              `json:"publishedAtUnixNano"`
	AccessLifetime       int64              `json:"accessLifetimeNanos"`
	AccessExpiresAt      int64              `json:"accessExpiresAtUnixNano"`
	AccessExpirySource   claudeExpirySource `json:"accessExpirySource"`
	RefreshRotated       bool               `json:"refreshRotated"`
	RefreshExpiryPresent bool               `json:"refreshExpiryPresent"`
}

type claudeProvenance struct {
	Version     uint8              `json:"version"`
	Transitions []claudeTransition `json:"transitions"` // oldest first
}

func claudeObservedTime(t time.Time) int64 {
	if !t.After(claudeEpoch) || t.After(time.Unix(0, math.MaxInt64)) {
		return 0
	}
	return t.UnixNano()
}

func newClaudeIncarnation() (high, low uint64) {
	for high == 0 && low == 0 {
		var entropy [16]byte
		_, _ = rand.Read(entropy[:]) // crypto/rand.Read always fills the buffer
		high, low = binary.BigEndian.Uint64(entropy[:8]), binary.BigEndian.Uint64(entropy[8:])
	}
	return high, low
}

func (p claudeProvenance) valid() bool {
	if p.Version != 1 || len(p.Transitions) == 0 || len(p.Transitions) > claudeHistoryLimit {
		return false
	}
	var prev uint64
	for _, t := range p.Transitions {
		if t.Generation <= prev || t.DispatchedAt < 0 || t.RespondedAt < 0 || t.PublishedAt < 0 || t.AccessExpiresAt < 0 ||
			t.AccessLifetime <= 0 || t.AccessLifetime > int64(claudeMaxLifetime) {
			return false
		}
		switch t.Operation {
		case claudeAdoption:
			if (t.IncarnationHigh == 0 && t.IncarnationLow == 0) || t.AccessExpirySource != claudeStagedExpiry || t.RefreshRotated || t.RefreshExpiryPresent {
				return false
			}
		case claudeScheduledRefresh:
			if t.AccessExpirySource != claudeResponseExpiry {
				return false
			}
		default:
			return false
		}
		prev = t.Generation
	}
	return true
}

func decodeClaudeProvenance(raw []byte) (claudeProvenance, error) {
	var p claudeProvenance
	// The schema has no string values or nullable fields. A JSON null would
	// otherwise be decoded as zero or false and conceal malformed evidence.
	if len(raw) == 0 || len(raw) > claudeProvenanceMax || bytes.Contains(raw, []byte("null")) {
		return p, errClaudeProvenance
	}
	if strict, err := kubejson.UnmarshalStrict(raw, &p); err != nil || len(strict) != 0 || !p.valid() {
		return claudeProvenance{}, errClaudeProvenance
	}
	return p, nil
}

func (p claudeProvenance) encode() []byte {
	if !p.valid() {
		return nil
	}
	raw, _ := json.Marshal(p) // fixed, validated primitive types
	return raw
}

// appendClaudeTransition treats unavailable diagnostics as missing. Only a
// preceding observation for the exact journal generation can supply a login
// incarnation. A keeper upgraded in place must wait for the next adoption
// to learn an incarnation.
func appendClaudeTransition(raw []byte, previous uint64, t claudeTransition) []byte {
	p, _ := decodeClaudeProvenance(raw)
	p.Version = 1
	for len(p.Transitions) > 0 && p.Transitions[len(p.Transitions)-1].Generation > previous {
		p.Transitions = p.Transitions[:len(p.Transitions)-1]
	}
	if n := len(p.Transitions); n != 0 && t.Operation == claudeScheduledRefresh && p.Transitions[n-1].Generation == previous {
		t.IncarnationHigh = p.Transitions[n-1].IncarnationHigh
		t.IncarnationLow = p.Transitions[n-1].IncarnationLow
	}
	p.Transitions = append(p.Transitions, t)
	if len(p.Transitions) > claudeHistoryLimit {
		p.Transitions = p.Transitions[len(p.Transitions)-claudeHistoryLimit:]
	}
	return p.encode()
}
