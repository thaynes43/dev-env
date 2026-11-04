package protocol

import "time"

// HomeHeadroomVersion is the only version agentd writes.
const HomeHeadroomVersion = 1

// Closed state words for HomeHeadroom.State and HeadroomFigure.State. They
// describe a measurement; none of them is an action.
const (
	HeadroomOK         = "ok"
	HeadroomLow        = "low"
	HeadroomExhausted  = "exhausted"
	HeadroomUnknown    = "unknown"
	HeadroomUnreadable = "unreadable"
)

// Closed reason words for an unknown or unreadable HomeHeadroom.
const (
	HeadroomReasonPermission    = "permission"
	HeadroomReasonNotFound      = "not-found"
	HeadroomReasonIO            = "io"
	HeadroomReasonTimeout       = "timeout"
	HeadroomReasonOther         = "other"
	HeadroomReasonBytesInvalid  = "bytes-invalid"
	HeadroomReasonInodesInvalid = "inodes-invalid"
	HeadroomReasonBothInvalid   = "both-invalid"
)

// HeadroomFigure is one measured dimension of the HOME filesystem. Available is
// what the agent's own user can still use (bytes: Bavail; inodes: Ffree).
type HeadroomFigure struct {
	State     string `json:"state"`
	Total     uint64 `json:"total"`
	Available uint64 `json:"available"`
}

// HomeHeadroom is agentd's bounded statfs observation of the filesystem that
// holds $HOME. It carries numbers and a closed state word only: no path, no
// error text, no file names. An absent HomeHeadroom on Status means "not
// measured", never zero headroom. An operator that does not know the field
// ignores it.
type HomeHeadroom struct {
	Version    int       `json:"version"`
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
	// Bytes and Inodes are omitted when that dimension could not be measured.
	Bytes  *HeadroomFigure `json:"bytes,omitempty"`
	Inodes *HeadroomFigure `json:"inodes,omitempty"`
}

// Valid reports whether h uses only the closed words, pairs a reason exactly
// with an unknown or unreadable state, has self-consistent figures, and names
// the state that its figures and missing dimensions imply.
func (h HomeHeadroom) Valid() bool {
	if h.Version != HomeHeadroomVersion || h.ObservedAt.IsZero() {
		return false
	}
	for _, f := range []*HeadroomFigure{h.Bytes, h.Inodes} {
		if f != nil && !f.valid() {
			return false
		}
	}
	switch h.State {
	case HeadroomUnreadable:
		switch h.Reason {
		case HeadroomReasonPermission, HeadroomReasonNotFound, HeadroomReasonIO, HeadroomReasonTimeout, HeadroomReasonOther:
		default:
			return false
		}
		return h.Bytes == nil && h.Inodes == nil
	case HeadroomOK, HeadroomLow, HeadroomExhausted, HeadroomUnknown:
	default:
		return false
	}
	want := HeadroomOK
	for _, f := range []*HeadroomFigure{h.Bytes, h.Inodes} {
		switch {
		case f == nil:
			if want == HeadroomOK {
				want = HeadroomUnknown
			}
		case f.State == HeadroomExhausted:
			want = HeadroomExhausted
		case f.State == HeadroomLow && want != HeadroomExhausted:
			want = HeadroomLow
		}
	}
	if h.State != want {
		return false
	}
	if h.State != HeadroomUnknown {
		return h.Reason == ""
	}
	switch h.Reason {
	case HeadroomReasonBytesInvalid:
		return h.Bytes == nil && h.Inodes != nil
	case HeadroomReasonInodesInvalid:
		return h.Inodes == nil && h.Bytes != nil
	case HeadroomReasonBothInvalid:
		return h.Bytes == nil && h.Inodes == nil
	}
	return false
}

func (f HeadroomFigure) valid() bool {
	switch f.State {
	case HeadroomOK, HeadroomLow, HeadroomExhausted:
	default:
		return false
	}
	return f.Total > 0 && f.Available <= f.Total
}
