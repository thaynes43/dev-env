package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// budget creates or inspects retained campaign authority. Native admission is
// still the owned executor's separate, independently checked operation.
func (a *app) budget(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("budget requires create or show; agent-run help budget")
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(a.env.Stdout, commandHelp["budget"])
		return nil
	}
	creating := args[0] == "create"
	if !creating && args[0] != "show" {
		return usageError("budget requires create or show; agent-run help budget")
	}
	cmd := newCommand("budget", outputJSON)
	var success string
	var overall, effort, checkpoint time.Duration
	if creating {
		cmd.fs.StringVar(&success, "success", "", "")
		cmd.fs.DurationVar(&overall, "overall", time.Duration(apiv1.TaskBudgetPilotOverallSeconds)*time.Second, "")
		cmd.fs.DurationVar(&effort, "effort", time.Duration(apiv1.TaskBudgetPilotEffortSeconds)*time.Second, "")
		cmd.fs.DurationVar(&checkpoint, "checkpoint", time.Duration(apiv1.TaskBudgetPilotCheckpointSeconds)*time.Second, "")
	}
	pos, err := cmd.parse(a, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 || !protocol.ValidTaskBudgetIdentifier(pos[0]) {
		return usageError("budget %s requires one task UID", args[0])
	}
	uid := pos[0]
	method, path := http.MethodGet, apiv1.TaskBudgetPath(uid)
	var body any
	var spec apiv1.TaskBudgetSpec
	if creating {
		wholeSeconds := func(d time.Duration, ceiling int64) bool {
			return d > 0 && d%time.Second == 0 && int64(d/time.Second) <= ceiling
		}
		if strings.TrimSpace(success) == "" || len(success) > 1024 || !wholeSeconds(overall, apiv1.TaskBudgetPilotOverallSeconds) ||
			!wholeSeconds(effort, apiv1.TaskBudgetPilotEffortSeconds) || !wholeSeconds(checkpoint, apiv1.TaskBudgetPilotCheckpointSeconds) || checkpoint > overall {
			return usageError("budget create requires --success (1-1024 bytes) and positive whole-second durations: --overall at most 45m, --effort at most 1h, --checkpoint at most 10m and no longer than --overall")
		}
		spec = apiv1.TaskBudgetSpec{SuccessCondition: success, OverallSeconds: int64(overall / time.Second), EffortSeconds: int64(effort / time.Second), CheckpointSeconds: int64(checkpoint / time.Second)}
		method, path = http.MethodPost, apiv1.TaskBudgetsPath
		body = apiv1.CreateTaskBudgetRequest{TaskUID: uid, Spec: spec}
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http, one.rejectOversizeResponse = &hc, true
	var raw json.RawMessage
	status, err := a.callOnce(ctx, &one, method, path, body, &raw)
	if err != nil {
		return err
	}
	wantStatus := http.StatusOK
	if creating {
		wantStatus = http.StatusCreated
	}
	var out apiv1.TaskBudgetStatus
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if status != wantStatus || !strictWireJSON(raw, reflect.TypeFor[apiv1.TaskBudgetStatus]()) || d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || !confirmedBudgetStatus(uid, out) ||
		creating && (out.Binding.Epoch != 1 || out.Latched || out.EffortMilliseconds != 0 || out.Escalation != nil ||
			!out.Binding.Deadline.Equal(out.LastProgress.Add(overall)) || !out.NextCheckpoint.Equal(out.LastProgress.Add(checkpoint))) {
		return unconfirmed(method, path, "invalid task-budget receipt")
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(out)
	}
	a.outf("task: %s\nhost: %s\npod UID: %s\nepoch: %d\ndeadline: %s\ncheckpoint: %s\nlatched: %t\nreason: %s\nadmitted: %t\neffort milliseconds: %d\naccounting: %s\nstop confirmed: %t\n",
		out.Binding.TaskUID, out.Binding.HostID, out.Binding.PodUID, out.Binding.Epoch,
		out.Binding.Deadline.UTC().Format(time.RFC3339Nano), out.NextCheckpoint.UTC().Format(time.RFC3339Nano),
		out.Latched, firstOf(out.Reason, "-"), out.Admitted, out.EffortMilliseconds, out.UsageAccounting, out.StopConfirmed)
	return nil
}

func confirmedBudgetStatus(uid string, st apiv1.TaskBudgetStatus) bool {
	b := st.Binding
	return st.Observed && !st.Admitted && b.TaskUID == uid && protocol.ValidTaskBudgetIdentifier(b.HostID) &&
		protocol.ValidTaskBudgetIdentifier(b.PodUID) && b.Epoch > 0 && !b.Deadline.IsZero() &&
		!st.LastProgress.IsZero() && !st.NextCheckpoint.IsZero() && st.EffortMilliseconds >= 0 && st.UsageAccounting != ""
}
