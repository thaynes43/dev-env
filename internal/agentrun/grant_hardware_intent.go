package agentrun

import (
	"context"
	"net/http"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const hardwareIntentNotice = "Audit intent only: credential authority and keeper approval are unchanged. This receipt does not enforce a command or target, prove human consent, or prove a hardware operation occurred. Copied credentials and persistent effects are not revoked by lease expiry."

func (a *app) grantHardwareIntent(ctx context.Context, args []string) error {
	cmd := newCommand("grant", outputJSON)
	var req apiv1.HardwareIntentRequest
	cmd.fs.StringVar(&req.GrantUID, "grant-uid", "", "")
	cmd.fs.StringVar(&req.Digest, "digest", "", "")
	cmd.fs.StringVar(&req.Operation, "operation", "", "")
	cmd.fs.StringVar(&req.Host, "host", "", "")
	cmd.fs.StringVar(&req.Device, "device", "", "")
	cmd.fs.StringVar(&req.Firmware, "firmware", "", "")
	cmd.fs.StringVar(&req.TomRef, "tom-ref", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !validGrantCLIName(pos[0]) {
		return usageError("hardware-intent requires one grant name and --grant-uid, --digest, --operation, --host, --device, --firmware and --tom-ref")
	}
	if err := apiv1.ValidateHardwareIntentRequest(req); err != nil {
		return usageError("%s", err)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	return a.postHardwareIntent(ctx, c, pos[0], req)
}

func (a *app) postHardwareIntent(ctx context.Context, c *conn, name string, req apiv1.HardwareIntentRequest) error {
	var out apiv1.HardwareIntentReceipt
	status, err := a.callOnce(ctx, c, http.MethodPost, apiv1.GrantHardwareIntentPath(name), req, &out)
	if err != nil {
		return err
	}
	if status != http.StatusCreated || out.HardwareIntentRequest != req || out.SessionUID == "" || out.PodUID == "" || out.RecordedAt.IsZero() {
		return unconfirmed(http.MethodPost, apiv1.GrantHardwareIntentPath(name), "hardware intent receipt did not match the request; read the grant before taking another action")
	}
	out.Notice = hardwareIntentNotice
	return a.printJSON(out)
}
