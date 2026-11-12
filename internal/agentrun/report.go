package agentrun

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var reportPodName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func (a *app) report(ctx context.Context, args []string) error {
	cmd := newCommand("report", outputJSON)
	var req apiv1.ReportOutcomeRequest
	cmd.fs.StringVar(&req.Note, "note", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("report takes working, done, failed or escalate; see agent-run help report")
	}
	req.State = pos[0]
	switch req.State {
	case "working", "done", "failed", "escalate":
	default:
		return usageError("report takes working, done, failed or escalate")
	}
	if !a.inSessionPod() {
		return fail(ExitAuth, "report is only available from the summoned session's own pod")
	}
	// Kubernetes sets HOSTNAME to the pod's name, which is the Session's name.
	// This selects a route only; the API's live token/pod/Session UID chain is
	// the authority. A forged hostname cannot report for another Session.
	name := a.env.Getenv("HOSTNAME")
	if len(name) > 63 || !reportPodName.MatchString(name) {
		return fail(ExitAuth, "report needs this session pod's HOSTNAME")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	one.rejectOversizeResponse = true
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	path := apiv1.SessionOutcomePath(name)
	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	status, raw, err := a.send(callCtx, &one, http.MethodPost, one.url+path, payload)
	if err != nil {
		if nothingSent(err) {
			return err
		}
		return unconfirmed(http.MethodPost, path, "no confirmed outcome response; read agent-run show before retrying")
	}
	var out apiv1.Outcome
	if status == http.StatusOK || status == http.StatusConflict {
		if json.Unmarshal(raw, &out) == nil && out.At != nil && outcomeReportState(out.State) {
			if status == http.StatusConflict {
				return fail(ExitFailed, "outcome is final: %s: %s", out.State, out.Note)
			}
			if cmd.c.output == outputJSON {
				return a.printJSON(out)
			}
			a.outf("%s: %s\n", out.State, out.Note)
			return nil
		}
	}
	if status >= 400 {
		return oneShotRefusal(&one, http.MethodPost, path, status, raw)
	}
	return unconfirmed(http.MethodPost, path, "the answer is not a stored outcome")
}

func outcomeReportState(state string) bool {
	return state == "running" || state == "done" || state == "failed" || state == "escalated"
}
