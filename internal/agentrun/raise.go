package agentrun

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// These match internal/sessionraise. agent-run does not import that package:
// the binary may link only the standard library, apiv1 and agentd's protocol.
const (
	raiseWherePhone       = "phone-prompt"
	raiseWhereOwn         = "own-call"
	raiseWhereChat        = "chat"
	raiseStatePending     = "pending"
	raiseStateActive      = "active"
	raiseStateUnannounced = "unannounced"
	raiseStateTakenBack   = "taken-back"
	raiseNoticeAttempts   = 12
)

// raise performs a raise or takes one back. This build does not restart the
// session: --restart is not a flag here.
func (a *app) raise(ctx context.Context, args []string) error {
	cmd := newCommand("raise", outputJSON)
	profile := cmd.fs.String("profile", "", "")
	start := cmd.fs.Bool("start-sessions", false, "")
	words := cmd.fs.String("words", "", "")
	saidAt := cmd.fs.String("said-at", "", "")
	ref := cmd.fs.String("ref", "", "")
	takeBack := cmd.fs.Bool("take-back", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("raise takes a session; agent-run help raise")
	}
	name := pos[0]
	if *takeBack && (*profile != "" || *start || *words != "" || *saidAt != "" || *ref != "") {
		return usageError("take-back takes only the session; agent-run help raise")
	}
	if !*takeBack && (*words == "" || *saidAt == "" || (*profile != "") == *start) {
		return usageError("a raise names a profile or --start-sessions, Tom's words and when he said them; agent-run help raise")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var cur apiv1.Session
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionPath(url.PathEscape(name)), nil, nil, &cur); err != nil {
		if missingSession(err) {
			return fail(ExitNotFound, "no such session")
		}
		return err
	}
	path := apiv1.SessionRaisePath(url.PathEscape(name))
	var next apiv1.Session
	var status int
	if *takeBack {
		status, err = a.call(ctx, c, http.MethodDelete, path, nil, apiv1.TakeBackRequest{ExpectUID: cur.UID}, &next)
	} else {
		where := raiseWhereOwn
		if a.inSessionPod() {
			where = raiseWherePhone
		}
		status, err = a.call(ctx, c, http.MethodPost, path, nil, apiv1.RaiseRequest{
			ExpectUID: cur.UID, Profile: *profile, StartSessions: *start,
			Owner: apiv1.RaiseOwner{Words: *words, Where: where, SaidAt: *saidAt, Ref: *ref},
		}, &next)
	}
	if status == http.StatusNotFound {
		return fail(ExitNotFound, "This operator does not have the raise routes.")
	}
	if err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(next)
	}
	if *takeBack {
		a.outf("Took back the raise of %s.\n", name)
		return nil
	}
	a.outf("Raised %s. Tom is being told; it takes effect two minutes after the notice is accepted, unless taken back.\n", name)
	return nil
}

func (a *app) printRaise(row func(k, format string, args ...any), s apiv1.Session) {
	r := s.Raise
	if r == nil {
		return
	}
	if r.Ignored != "" || r.State == "ignored" {
		reason := r.Ignored
		if reason == "" {
			reason = "the record does not count"
		}
		row("raise record ignored", "%s", reason)
		return
	}
	switch r.State {
	case raiseStatePending:
		if r.NoticeSentAt != "" {
			row("raise pending", "notice accepted at %s, takes effect at %s", r.NoticeSentAt, r.ActiveFrom)
			return
		}
		row("raise pending", "notice not yet accepted (attempt %d of %d)", r.NoticeAttempt, raiseNoticeAttempts)
		return
	case raiseStateUnannounced:
		row("raise unannounced", "the notice could not be sent, so it never took effect")
		return
	case raiseStateTakenBack:
		row("raise taken back by", "%s at %s", r.TakenBackBy, r.TakenBackAt)
		return
	}
	what := r.Profile
	if r.StartSessions {
		if what != "" {
			what += "; may start sessions"
		} else {
			what = "may start sessions"
		}
	}
	row("raised", "%s (performed by %s at %s; active from %s)", what, r.PerformedBy, r.PerformedAt, r.ActiveFrom)
	if r.Owner != nil {
		ref := ""
		if r.Owner.Ref != "" {
			ref = "; " + r.Owner.Ref
		}
		row("on Tom's word", "%q (%s in %s, %s%s)", r.Owner.Words, raiseWhere(r.Owner.Where), r.Owner.Session, r.Owner.SaidAt, ref)
	}
	// A start-sessions raise has no profile to apply at the next pod. The
	// depth exemption already honours it. --restart is slice (b).
	if r.Profile == "" {
		return
	}
	if r.PodAccount != "" {
		row("pod runs as", "%s; the raised profile applies at its next start (agent-run raise %s --restart)", r.PodAccount, s.Name)
		return
	}
	row("pod runs as", "the raised profile applies at its next start (agent-run raise %s --restart)", s.Name)
}

func raiseWhere(where string) string {
	switch where {
	case raiseWherePhone:
		return "phone prompt"
	case raiseWhereOwn:
		return "Tom's own call"
	case raiseWhereChat:
		return "chat"
	default:
		return where
	}
}

func raiseNote(r *apiv1.SessionRaise) string {
	if r == nil || r.Ignored != "" || r.State == "ignored" {
		return ""
	}
	var parts []string
	switch r.State {
	case raiseStatePending:
		return "raise pending"
	case raiseStateActive:
		if r.Profile != "" {
			parts = append(parts, "raised: "+r.Profile)
		}
		if r.StartSessions {
			parts = append(parts, "raised: may start sessions")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return joinNotes(parts)
}

func joinNotes(parts []string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		out += "; " + p
	}
	return out
}

func fleetRaised(sessions []apiv1.FleetSession) string {
	var parts []string
	for _, s := range sessions {
		r := s.Raise
		if r == nil || r.State != raiseStateActive || r.Ignored != "" {
			continue
		}
		if r.Profile != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", s.Name, r.Profile))
		}
		if r.StartSessions {
			parts = append(parts, s.Name+" (may start sessions)")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return joinNotes(parts)
}
