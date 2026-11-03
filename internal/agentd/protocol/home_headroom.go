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
// with an unknown or unreadable state, and has self-consistent figures.
func (h HomeHeadroom) Valid() bool {
	return false // RED stub
}
