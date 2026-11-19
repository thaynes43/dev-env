package protocol

import "time"

// CodexHostMemory is a passive cgroup v2 container sample. It is not a count
// of native threads and conveys no lifecycle or admission authority.
type CodexHostMemory struct {
	CurrentBytes int64     `json:"currentBytes"`
	LimitBytes   int64     `json:"limitBytes"`
	ObservedAt   time.Time `json:"observedAt"`
}

// Valid rejects unknown/unlimited or malformed observations.
func (m CodexHostMemory) Valid() bool {
	return m.CurrentBytes >= 0 && m.LimitBytes > 0 && !m.ObservedAt.IsZero()
}
