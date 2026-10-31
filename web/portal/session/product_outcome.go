package session

import (
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// taskOutcomeSection is a task session's retained outcome (#320): the summary
// line, pull requests and head commits the task wrote at its first exit, and
// the original session and pod that wrote it. It reads only the Session
// status the operator sent, so it is the same after the pod is reclaimed. It
// is the task's own claim and stays apart from Process exit, which says only
// how the process ended. No record is Unknown, never success. Only a task has
// this panel.
func taskOutcomeSection(a *apiv1.PortalSessionActivity) (portal.ProductSection, bool) {
	if a.Mode != "task" {
		return portal.ProductSection{}, false
	}
	s := portal.ProductSection{Title: "Task outcome"}
	o := a.TaskOutcome
	if o == nil {
		s.States = []portal.ProductState{{Status: portal.ProductStatus{Text: "Unknown", Tone: "muted"}, Text: "No outcome recorded."}}
		s.Note = "A task can write one when it exits. A clean process exit does not mean the work is done."
		return s, true
	}
	s.States = []portal.ProductState{{Status: portal.ProductStatus{Text: "Recorded", Tone: "info"}, Text: "Written by the task when it exited."}}
	s.Fields = append(s.Fields, portal.ProductField{Label: "Summary", Value: outcomeSummary(o.Record.Summary)})
	for _, u := range o.Record.PRURLs {
		s.Fields = append(s.Fields, portal.ProductField{Label: "Pull request", Value: pullRequestLabel(u), Href: u})
	}
	if len(o.Record.PRURLs) == 0 {
		s.Fields = append(s.Fields, portal.ProductField{Label: "Pull request", Value: "None reported"})
	}
	for _, h := range o.Record.HeadSHAs {
		s.Fields = append(s.Fields, portal.ProductField{Label: "Head commit", Value: strings.ToLower(h[:7]), Mono: true})
	}
	if len(o.Record.HeadSHAs) == 0 {
		s.Fields = append(s.Fields, portal.ProductField{Label: "Head commit", Value: "None reported"})
	}
	s.Fields = append(s.Fields,
		portal.ProductField{Label: "Original session", Value: shortUID(o.SessionUID), Mono: true},
		portal.ProductField{Label: "Original pod", Value: shortUID(o.PodUID), Mono: true})
	s.Note = "What the task reported. The portal does not verify it. Process exit is shown separately."
	return s, true
}

// processExitText words status.taskResult as how the process ended, never as
// whether the work is done. Succeeded is exit 0 with the provider's success
// result event; anything the operator did not classify is Unknown.
func processExitText(result string) string {
	switch result {
	case "Succeeded":
		return "Exited 0"
	case "Failed":
		return "Failed"
	case "TimedOut":
		return "Timed out"
	}
	return "Unknown"
}

// outcomeSummary is the validated summary line, at most 200 printable
// characters. It is not cut: shown's byte bound could split a character.
func outcomeSummary(summary string) string {
	if text := strings.TrimSpace(summary); text != "" {
		return text
	}
	return "Unknown"
}

// pullRequestLabel is owner/repo#number for a URL the decoder already matched
// against the operator's pattern; anything else is shown whole.
func pullRequestLabel(u string) string {
	path, ok := strings.CutPrefix(u, "https://github.com/")
	if !ok {
		return u
	}
	repo, number, ok := strings.Cut(path, "/pull/")
	if !ok {
		return u
	}
	return repo + "#" + number
}

// shortUID is a UID's first eight characters, enough to match it by eye.
func shortUID(uid string) string {
	if len(uid) <= 12 {
		return uid
	}
	return uid[:8] + "…"
}
