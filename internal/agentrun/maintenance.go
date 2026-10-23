package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/thaynes43/dev-env/internal/maintenancecontract"
)

// maintenance is the offline upgrade-shepherd check. It does not call the API.
func (a *app) maintenance(ctx context.Context, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		return a.help([]string{"maintenance"})
	}
	if args[0] != "check" {
		return usageError("maintenance takes check, not %q; agent-run help maintenance says more", args[0])
	}
	return a.maintenanceCheck(ctx, args[1:])
}

func (a *app) maintenanceCheck(ctx context.Context, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var input, nowText string
	fs := flag.NewFlagSet("maintenance check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&input, "input", "", "")
	fs.StringVar(&nowText, "now", "", "")
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(a.env.Stdout, commandHelp["maintenance"])
			return &exitError{code: ExitOK}
		}
		return usageError("maintenance check: %v; agent-run help maintenance lists its flags", err)
	}
	if len(fs.Args()) != 0 {
		return usageError("maintenance check: unexpected argument %q", fs.Args())
	}
	if input == "" {
		return usageError("maintenance check: --input is required")
	}
	now := time.Time{}
	if a.env.Now != nil {
		now = a.env.Now()
	}
	if nowText != "" {
		parsed, err := time.Parse(time.RFC3339Nano, nowText)
		if err != nil {
			return usageError("maintenance check: --now must be RFC3339, got %q", nowText)
		}
		now = parsed
	}
	if now.IsZero() {
		now = time.Now()
	}
	data, err := readMaintenanceInput(input, a.env.Stdin)
	if err != nil {
		return usageError("maintenance check: %v", err)
	}
	res, err := maintenancecontract.Check(data, now)
	if err != nil {
		var invalid *maintenancecontract.InvalidInputError
		if errors.As(err, &invalid) {
			if werr := writeMaintenanceJSON(a.env.Stdout, newMaintenanceErrorDocument(invalid)); werr != nil {
				return werr
			}
			return fail(ExitUsage, "%s", invalid.Error())
		}
		return fail(ExitFailed, "maintenance check: %v", err)
	}
	return writeMaintenanceJSON(a.env.Stdout, res)
}

func readMaintenanceInput(input string, stdin io.Reader) ([]byte, error) {
	if input == "-" {
		if stdin == nil {
			return nil, errors.New("--input - needs stdin")
		}
		return io.ReadAll(io.LimitReader(stdin, maintenancecontract.MaxDocumentBytes+1))
	}
	f, err := os.Open(input)
	if err != nil {
		return nil, err
	}
	data, rerr := io.ReadAll(io.LimitReader(f, maintenancecontract.MaxDocumentBytes+1))
	cerr := f.Close()
	if rerr != nil {
		return nil, rerr
	}
	if cerr != nil {
		return nil, cerr
	}
	return data, nil
}

type maintenanceErrorDocument struct {
	Error                          string                           `json:"error"`
	Fields                         []maintenancecontract.FieldError `json:"fields"`
	UnverifiedInput                bool                             `json:"unverifiedInput"`
	OfflineEvidence                string                           `json:"offlineEvidence"`
	CandidateIsOperationalDeferral bool                             `json:"candidateIsOperationalDeferral"`
	AuthorizationGranted           bool                             `json:"authorizationGranted"`
	Notice                         string                           `json:"notice"`
}

func newMaintenanceErrorDocument(err *maintenancecontract.InvalidInputError) maintenanceErrorDocument {
	return maintenanceErrorDocument{
		Error:           "invalid-input",
		Fields:          err.Fields,
		UnverifiedInput: true,
		OfflineEvidence: maintenancecontract.OfflineEvidenceUnverified,
		Notice:          maintenancecontract.Notice,
	}
}

func writeMaintenanceJSON(w io.Writer, value any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}
