package apiv1

// ReportOutcomeRequest reports the summoned session's own work. The response
// is Outcome, also on a terminal conflict; other refusals use ErrorResponse.
type ReportOutcomeRequest struct {
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

func SessionOutcomePath(name string) string { return SessionPath(name) + "/outcome" }
