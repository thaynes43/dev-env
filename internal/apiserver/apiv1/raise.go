package apiv1

// SessionRaise is the raise record a view shows. The signature and the key
// id are never included. Ignored is empty only when the record counts.
type SessionRaise struct {
	ID            string      `json:"id,omitempty"`
	State         string      `json:"state,omitempty"`
	Profile       string      `json:"profile,omitempty"`
	FromProfile   string      `json:"fromProfile,omitempty"`
	StartSessions bool        `json:"startSessions,omitempty"`
	Owner         *RaiseOwner `json:"owner,omitempty"`
	PerformedBy   string      `json:"performedBy,omitempty"`
	PerformedAt   string      `json:"performedAt,omitempty"`
	NoticeSentAt  string      `json:"noticeSentAt,omitempty"`
	ActiveFrom    string      `json:"activeFrom,omitempty"`
	TakenBackBy   string      `json:"takenBackBy,omitempty"`
	TakenBackAt   string      `json:"takenBackAt,omitempty"`
	// NoticeAttempt is the larger of the finished notice attempts and the
	// aged reservations, the same count the activator uses.
	// It is omitted once the raise is no longer pending.
	NoticeAttempt int `json:"noticeAttempt,omitempty"`
	// PodAccount is the current pod's ServiceAccount, and only on the
	// single-session view.
	PodAccount string `json:"podAccount,omitempty"`
	Ignored    string `json:"ignored,omitempty"`
}

// RaiseOwner is Tom's words as the view shows them.
type RaiseOwner struct {
	Words   string `json:"words,omitempty"`
	Where   string `json:"where,omitempty"`
	Session string `json:"session,omitempty"`
	SaidAt  string `json:"saidAt,omitempty"`
	Ref     string `json:"ref,omitempty"`
}

// RaiseStatus is GET /v1/sessions/{name}/raise: the name, UID, depth label,
// creation profile, pod profile and the raise record.
type RaiseStatus struct {
	Name       string        `json:"name"`
	UID        string        `json:"uid"`
	Depth      string        `json:"depth"`
	Profile    string        `json:"profile"`
	PodProfile string        `json:"podProfile,omitempty"`
	Raise      *SessionRaise `json:"raise,omitempty"`
}

// RaiseRequest is POST /v1/sessions/{name}/raise.
type RaiseRequest struct {
	ExpectUID     string     `json:"expectUID"`
	Profile       string     `json:"profile,omitempty"`
	StartSessions bool       `json:"startSessions,omitempty"`
	Owner         RaiseOwner `json:"owner"`
}

// TakeBackRequest is DELETE /v1/sessions/{name}/raise.
type TakeBackRequest struct {
	ExpectUID string `json:"expectUID"`
}

// SessionRaisePath is one session's raise route.
func SessionRaisePath(name string) string { return SessionPath(name) + "/raise" }
