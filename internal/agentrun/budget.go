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
		return usageError("budget requires create, show or continue; agent-run help budget")
	}
	if args[0] == "continue" {
		return a.continueBudget(ctx, args[1:])
	}
	if args[0] == "native" {
		return a.nativeBudget(ctx, args[1:])
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(a.env.Stdout, commandHelp["budget"])
		return nil
	}
	creating := args[0] == "create"
	if !creating && args[0] != "show" {
		return usageError("budget requires create, show or continue; agent-run help budget")
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

// nativeBudget consumes a complete explicit binding and request from stdin.
// It does not select a new epoch, generate another ID or retry a lost response.
func (a *app) nativeBudget(ctx context.Context, args []string) error {
	cmd := newCommand("budget native", outputJSON)
	var once bool
	cmd.fs.BoolVar(&once, "once", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if !once || len(pos) != 1 || !protocol.ValidTaskBudgetIdentifier(pos[0]) {
		return usageError("budget native requires --once and one task UID; request JSON on stdin")
	}
	raw, err := io.ReadAll(io.LimitReader(a.env.Stdin, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return usageError("native request exceeds 16KiB or could not be read")
	}
	var req apiv1.NativeControlRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || !strictWireJSON(raw, reflect.TypeFor[apiv1.NativeControlRequest]()) || !req.Valid() || req.Binding.TaskUID != pos[0] || !req.Binding.Deadline.After(a.env.Now()) {
		return usageError("native request needs an exact current binding and valid operation/ID")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	one := *c
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one.http = &hc
	one.rejectOversizeResponse = true
	path := apiv1.TaskBudgetPath(pos[0]) + "/native"
	var responseRaw json.RawMessage
	status, err := a.callOnce(ctx, &one, http.MethodPost, path, req, &responseRaw)
	if err != nil {
		return err
	}
	raw = responseRaw
	if req.Operation == "host/stop" {
		var result apiv1.TaskBudgetStatus
		if status != http.StatusAccepted || !strictWireJSON(raw, reflect.TypeFor[apiv1.TaskBudgetStatus]()) || json.Unmarshal(raw, &result) != nil || result.Binding != req.Binding || !result.Observed || !result.NativeStopRequested {
			return unconfirmed(http.MethodPost, path, "native stop request is unconfirmed; do not replay")
		}
		return a.printJSON(result)
	}
	result, decodeErr := apiv1.DecodeNativeControlResponse(raw, req)
	if status != http.StatusOK || decodeErr != nil {
		return unconfirmed(http.MethodPost, path, "native request is unconfirmed; ID consumed, do not replay")
	}
	return a.printJSON(result)
}

func confirmedBudgetStatus(uid string, st apiv1.TaskBudgetStatus) bool {
	b := st.Binding
	return st.Observed && !st.Admitted && b.TaskUID == uid && protocol.ValidTaskBudgetIdentifier(b.HostID) &&
		protocol.ValidTaskBudgetIdentifier(b.PodUID) && b.Epoch > 0 && !b.Deadline.IsZero() &&
		!st.LastProgress.IsZero() && !st.NextCheckpoint.IsZero() && st.EffortMilliseconds >= 0 && st.UsageAccounting != ""
}

func (a *app) continueBudget(ctx context.Context, args []string) error {
	cmd := newCommand("budget continue", outputJSON)
	var decision string
	cmd.fs.StringVar(&decision, "decision", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !protocol.ValidTaskBudgetIdentifier(pos[0]) || !protocol.ValidTaskBudgetIdentifier(decision) {
		return usageError("budget continue requires --decision ID and one task UID")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	one := *c
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one.http = &hc
	one.rejectOversizeResponse = true
	uid := pos[0]
	path := apiv1.TaskBudgetPath(uid)
	var raw json.RawMessage
	status, err := a.callOnce(ctx, &one, http.MethodGet, path, nil, &raw)
	if err != nil {
		return err
	}
	var before apiv1.TaskBudgetStatus
	if status != http.StatusOK || !strictWireJSON(raw, reflect.TypeFor[apiv1.TaskBudgetStatus]()) || json.Unmarshal(raw, &before) != nil || !confirmedBudgetStatus(uid, before) || !before.Latched || before.Reason != "NativeRootStopped" || !before.StopConfirmed {
		return unconfirmed(http.MethodGet, path, "original owned stop is not confirmed")
	}
	body := apiv1.ContinueTaskBudgetRequest{Binding: before.Binding, DecisionID: decision}
	status, err = a.callOnce(ctx, &one, http.MethodPost, path+"/continue", body, &raw)
	if err != nil {
		return err
	}
	var after apiv1.TaskBudgetStatus
	if status != http.StatusOK || !strictWireJSON(raw, reflect.TypeFor[apiv1.TaskBudgetStatus]()) || json.Unmarshal(raw, &after) != nil || !confirmedBudgetStatus(uid, after) || after.Latched || after.Binding.Epoch != before.Binding.Epoch+1 || after.Binding.TaskUID != before.Binding.TaskUID || after.Binding.HostID != before.Binding.HostID || after.Binding.PodUID != before.Binding.PodUID || !after.Binding.Deadline.Equal(before.Binding.Deadline) || after.EffortMilliseconds != before.EffortMilliseconds {
		return unconfirmed(http.MethodPost, path+"/continue", "within-budget continuation is not confirmed")
	}
	return a.printJSON(after)
}
