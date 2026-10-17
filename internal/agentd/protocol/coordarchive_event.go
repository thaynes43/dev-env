package protocol

import (
	"regexp"
	"time"
	"unicode/utf8"
)

const (
	CoordArchiveEventFormat   = "dev-env.coordination-archive.event.v1"
	MaxCoordArchiveEventBytes = 8 << 10
)

// These are recorded observations, not authenticated absence or release proof.
type CoordArchiveObservation struct {
	Role      string `json:"role"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Observed  string `json:"observed"`
	AckUID    string `json:"ackUID,omitempty"`
}

type CoordArchiveEvent struct {
	Format   string                    `json:"format"`
	State    string                    `json:"state"`
	At       time.Time                 `json:"at"`
	Rule     string                    `json:"rule"`
	Sessions []CoordArchiveObservation `json:"sessions"`
	By       string                    `json:"by"`
}

type CoordArchiveMarkRequest struct {
	CoordArchiveIdentity
	Event CoordArchiveEvent `json:"event"`
}

type CoordArchiveMarkResult struct {
	CoordArchiveIdentity
	Appended   bool       `json:"appended"`
	GraceStart *time.Time `json:"graceStart"`
}

var coordObservationName = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

func (e CoordArchiveEvent) Validate(resource string) error {
	invalid := func() error { return HistoryErrorf(HistoryErrInvalid, "", "invalid coordination event observation") }
	if e.Format != CoordArchiveEventFormat || e.At.IsZero() || e.At.Location() != time.UTC || e.By == "" || len(e.By) > 256 || !utf8.ValidString(e.By) || len(e.Sessions) > 2 {
		return invalid()
	}
	rule, roles := "", map[string]bool{}
	switch resource {
	case "taskclaims":
		rule = "owner-absent"
		roles["owner"] = true
	case "publishedcontexts":
		rule = "source-absent"
		roles["source"] = true
	case "inboxmessages":
		rule = "sender-absent-recipient-done"
		roles["sender"] = true
		roles["recipient"] = true
	case "inboxreadacks":
		rule = "message-released"
		roles["message"] = true
	default:
		return invalid()
	}
	if e.Rule != rule {
		return invalid()
	}
	seen := map[string]CoordArchiveObservation{}
	present := false
	for _, s := range e.Sessions {
		if !roles[s.Role] || !coordArchiveLabel.MatchString(s.Namespace) || !coordObservationName.MatchString(s.Name) || !ValidKubeUID(s.UID) {
			return invalid()
		}
		if _, ok := seen[s.Role]; ok {
			return invalid()
		}
		seen[s.Role] = s
		switch s.Observed {
		case "not-found", "uid-replaced":
			if s.AckUID != "" {
				return invalid()
			}
		case "present":
			present = true
			if s.AckUID != "" {
				return invalid()
			}
		case "acknowledged":
			if s.Role != "recipient" || !ValidKubeUID(s.AckUID) {
				return invalid()
			}
		case "message-released":
			if s.Role != "message" || s.AckUID != "" {
				return invalid()
			}
		default:
			return invalid()
		}
	}
	switch e.State {
	case "absent":
		if len(seen) != len(roles) || present {
			return invalid()
		}
		for role, s := range seen {
			if role == "message" {
				if s.Observed != "message-released" {
					return invalid()
				}
			} else if role == "recipient" {
				if s.Observed != "not-found" && s.Observed != "uid-replaced" && s.Observed != "acknowledged" {
					return invalid()
				}
			} else if s.Observed != "not-found" && s.Observed != "uid-replaced" {
				return invalid()
			}
		}
	case "present", "hold":
		if !present {
			return invalid()
		}
	case "reset": // Lost archive data needs no new Session observation.
	default:
		return invalid()
	}
	raw, err := CanonicalJSON(e)
	if err != nil || len(raw) > MaxCoordArchiveEventBytes {
		return invalid()
	}
	return nil
}
