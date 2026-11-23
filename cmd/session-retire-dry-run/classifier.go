package main

// sessionRecord is an offline projection of observation and preservation
// evidence for one session. Producers must verify that every proof applies to
// this session and its stopped worktree. An absent or unexpected value is
// unconfirmed. Neither the output nor the input grants lifecycle authority.
type sessionRecord struct {
	Session            string `json:"session"`
	Phase              string `json:"phase"`
	Execution          string `json:"execution,omitempty"`    // live or stopped
	NativeThread       string `json:"nativeThread,omitempty"` // live or clear
	PendingWork        string `json:"pendingWork,omitempty"`  // present or clear
	Activity           string `json:"activity,omitempty"`     // live or clear
	Hold               string `json:"hold,omitempty"`         // present or clear
	Reap               string `json:"reap,omitempty"`         // pending or clear
	Retirement         string `json:"retirement,omitempty"`
	Checkpoint         string `json:"checkpoint,omitempty"`
	BranchAndHead      string `json:"branchAndHead,omitempty"`
	UnpushedCommits    string `json:"unpushedCommits,omitempty"`
	UncommittedChanges string `json:"uncommittedChanges,omitempty"`
	Successor          string `json:"successor,omitempty"` // acknowledged or not-required
	Retention          string `json:"retention,omitempty"`
}

type classification struct {
	Session  string   `json:"session"`
	Class    string   `json:"class"`
	Decision string   `json:"decision"`
	Reasons  []string `json:"reasons"`
}

func classify(record sessionRecord) classification {
	result := classification{Session: record.Session, Class: "blocked", Decision: "keep"}
	switch record.Phase {
	case "Pending", "Running", "Idle", "Draining":
		result.Reasons = append(result.Reasons, "live-or-pending-phase")
	}
	if record.Execution == "live" {
		result.Reasons = append(result.Reasons, "live-execution")
	}
	if record.Hold == "present" {
		result.Reasons = append(result.Reasons, "explicit-hold")
	}
	for _, observation := range []struct {
		value, active, reason string
	}{
		{record.NativeThread, "live", "native-thread"},
		{record.PendingWork, "present", "pending-work"},
		{record.Activity, "live", "activity"},
	} {
		switch observation.value {
		case observation.active:
			result.Reasons = append(result.Reasons, observation.reason+"-observed")
		case "clear":
		default:
			result.Reasons = append(result.Reasons, observation.reason+"-unconfirmed")
		}
	}
	if len(result.Reasons) > 0 {
		result.Class = "active"
		if record.Reap == "pending" {
			result.Reasons = append(result.Reasons, "reap-unconfirmed")
		}
		return result
	}

	stoppedPhase := record.Phase == "Suspended" || record.Phase == "Failed"
	stopped := stoppedPhase && record.Execution == "stopped"
	successorConfirmed := record.Successor == "acknowledged" || record.Successor == "not-required"
	checks := []struct {
		confirmed bool
		reason    string
	}{
		{stopped, "execution-unconfirmed"},
		{record.Hold == "clear", "hold-unconfirmed"},
		{record.Reap == "clear", "reap-unconfirmed"},
		{record.Retirement == "verified", "retirement-unconfirmed"},
		{record.Checkpoint == "verified", "checkpoint-unconfirmed"},
		{record.BranchAndHead == "verified", "branch-head-unconfirmed"},
		{record.UnpushedCommits == "verified", "unpushed-commits-unconfirmed"},
		{record.UncommittedChanges == "verified", "uncommitted-changes-unconfirmed"},
		{successorConfirmed, "successor-unconfirmed"},
		{record.Retention == "verified", "retention-unconfirmed"},
	}
	for _, check := range checks {
		if !check.confirmed {
			result.Reasons = append(result.Reasons, check.reason)
		}
	}
	if len(result.Reasons) == 0 {
		result.Class = "retire candidate"
		result.Decision = "review"
		result.Reasons = []string{"retirement-and-preservation-verified"}
	} else if stopped && record.Hold == "clear" && record.Reap == "clear" &&
		(record.Retirement != "verified" || !successorConfirmed) {
		result.Class = "abandoned"
	}
	return result
}
