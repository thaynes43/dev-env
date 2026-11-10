package agentd

// Passive allowance readings (DESIGN-059): the numbers a running session's own
// CLI already received with a model response. agentd makes no provider
// request, starts no provider client and touches no login to get them.

// The reasons a reading was not sent. A heartbeat names one in its
// allowance-reading step, so the operator's status shows why a session that
// has a reading did not report it. Each is fixed text: nothing from the
// reading itself is ever put in a note.
const (
	// allowanceShapeUnrecognized: the source has a record, and the fields the
	// reader uses do not have the documented shape.
	allowanceShapeUnrecognized = "shape_unrecognized"
	// allowanceOutOfBounds: the record has the shape, and a value is outside
	// the bounds of DESIGN-059 4.4, such as a reset before the reading.
	allowanceOutOfBounds = "out_of_bounds"
)
