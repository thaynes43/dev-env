package protocol

import (
	"errors"
	"strings"
	"time"
)

// StartDeadlineEnv carries a scheduled session's start deadline (DESIGN-054):
// its due time plus grace, copied from the session's scheduled-work
// start-deadline annotation. The controller sets it only on the pod of a
// session that has the annotation, so every other pod keeps its bytes. agentd
// refuses the session's first launch at or after the deadline, and never
// refuses a boot after the first launch.
const StartDeadlineEnv = "AGENTD_START_DEADLINE"

// The agent error agentd reports when it refuses a first launch.
const (
	StartDeadlinePassed  = "start deadline passed"
	StartDeadlineInvalid = "invalid start deadline"
)

// maxStartDeadline bounds the value; RFC 3339 with nanoseconds is 30 bytes.
const maxStartDeadline = 64

// ParseStartDeadline reads a start deadline: an RFC 3339 time written in UTC
// ("Z"), as the annotation carries it. The controller and agentd both parse
// with it, so a value the controller passed is a value agentd accepts.
func ParseStartDeadline(raw string) (time.Time, error) {
	if len(raw) > maxStartDeadline || !strings.HasSuffix(raw, "Z") {
		return time.Time{}, errors.New("start deadline is not an RFC 3339 UTC time")
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, errors.New("start deadline is not an RFC 3339 UTC time")
	}
	return t, nil
}
