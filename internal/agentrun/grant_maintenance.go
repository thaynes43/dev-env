package agentrun

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// grantMaintenance uses the existing authenticated connection and grant routes.
// Scope is structured JSON; commands are not an approval allowlist.
func (a *app) grantMaintenance(ctx context.Context, action string, args []string) error {
	cmd := newCommand("grant", outputJSON)
	var task, reason, ttl, uid, digest, report string
	var approve bool
	cmd.fs.StringVar(&task, "task-json", "", "")
	cmd.fs.StringVar(&reason, "reason", "", "")
	cmd.fs.StringVar(&ttl, "ttl", "", "")
	cmd.fs.StringVar(&uid, "grant-uid", "", "")
	cmd.fs.StringVar(&digest, "digest", "", "")
	cmd.fs.StringVar(&report, "restore-evidence", "", "")
	cmd.fs.BoolVar(&approve, "approve", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	var request any
	route := apiv1.GrantsPath
	switch action {
	case "maintenance-request":
		if len(pos) != 0 || strings.TrimSpace(reason) == "" || len(task) > 64*1024 || task == "" {
			return usageError("maintenance-request requires --task-json and --reason; optional --ttl defaults to30m")
		}
		var scope apiv1.MaintenanceTask
		dec := json.NewDecoder(strings.NewReader(task))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&scope); err != nil {
			return usageError("invalid maintenance task: %v", err)
		}
		if scope.TaskClaim != nil && !apiv1.ValidMaintenanceTaskClaim(scope.TaskClaim.Name, scope.TaskClaim.UID, scope.TaskClaim.Project) {
			return usageError("maintenance taskClaim requires a canonical name, immutable UID and project")
		}
		if ttl != "" {
			if d, err := time.ParseDuration(ttl); err != nil || d < 10*time.Minute || d > time.Hour {
				return usageError("maintenance TTL is10m to1h")
			}
		}
		request = apiv1.CreateGrantRequest{Type: "maintenance", Maintenance: &scope, Reason: reason, TTL: ttl}
	case "maintenance-decide":
		if len(pos) != 1 || !validGrantCLIName(pos[0]) || uid == "" || digest == "" {
			return usageError("maintenance-decide requires --grant-uid, --digest, optional --approve and grant name; default is deny")
		}
		request = apiv1.MaintenanceDecisionRequest{GrantUID: uid, Digest: digest, Approve: approve, TTL: ttl}
		route = apiv1.GrantPath(pos[0]) + "/maintenance-decision"
	case "maintenance-exec":
		if len(pos) < 2 || !validGrantCLIName(pos[0]) {
			return usageError("maintenance-exec [--restore-evidence report] <grant> <command> [args...]")
		}
		argv := pos[1:]
		if argv[0] == "--" {
			argv = argv[1:]
		}
		if len(argv) == 0 {
			return usageError("maintenance-exec requires a command")
		}
		request = apiv1.MaintenanceCommand{Command: argv, RestoreEvidence: report}
		route = apiv1.GrantPath(pos[0]) + "/maintenance-exec"
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	if action == "maintenance-exec" {
		var out apiv1.MaintenanceCommandResult
		if _, err := a.call(ctx, c, http.MethodPost, route, nil, request, &out); err != nil {
			return err
		}
		if err := a.printJSON(out); err != nil {
			return err
		}
		if out.Error != "" {
			return fail(ExitFailed, "maintenance command failed: %s", out.Error)
		}
		return nil
	}
	var out apiv1.Grant
	if _, err := a.call(ctx, c, http.MethodPost, route, nil, request, &out); err != nil {
		return err
	}
	return a.printJSON(out)
}
