package usagecollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const DiagnosticMaxBytes = 2048
const diagnosticSchema = "dev-env.allowance-diagnostic.v1"

// AcquisitionDiagnostic contains only closed codes and a public software version.
// Vendor output, identities, URLs and error strings never enter this channel.
// It is separate from the unchanged durable receipt and journal protocols.
type AcquisitionDiagnostic struct {
	Schema        string `json:"schema"`
	Provider      string `json:"provider"`
	Stage         string `json:"stage"`
	Reason        string `json:"reason"`
	ClientVersion string `json:"clientVersion"`
}

var publicVersion = regexp.MustCompile(`^(?:[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}|20[0-9]{2}\.[0-9]{2}\.[0-9]{2}-[a-f0-9]{7,40})$`)

type diagnosticError struct {
	stage, reason, version string
	cause                  error
}

func (*diagnosticError) Error() string   { return ErrCapture.Error() }
func (e *diagnosticError) Unwrap() error { return e.cause }

func refused(reason string, cause error) error {
	return &diagnosticError{reason: reason, cause: cause}
}

// A cancelled command closes its pipes too. Observe the context before calling
// that a terminal or stream fault; cleanup uncertainty is handled separately.
func refusedWithContext(ctx context.Context, reason string) error {
	if ctx != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			reason = "cancelled"
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			reason = "deadline"
		}
	}
	return refused(reason, ErrCapture)
}

func unqualifiedVersion(version string) error {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "codex-cli ")
	version = strings.TrimSuffix(version, " (Claude Code)")
	if !publicVersion.MatchString(version) {
		version = ""
	}
	return &diagnosticError{reason: "version_unqualified", version: version, cause: ErrCapture}
}

func atAcquisitionStage(stage string, cause error) error {
	e := &diagnosticError{stage: stage, reason: "capture_refused", cause: cause}
	var detail *diagnosticError
	if errors.As(cause, &detail) {
		e.reason, e.version = detail.reason, detail.version
	}
	if errors.Is(cause, ErrCleanup) {
		e.reason = "cleanup_unproven"
	}
	return e
}

func (d AcquisitionDiagnostic) valid() bool {
	if d.Schema != diagnosticSchema || d.Provider != "openai" && d.Provider != "anthropic" && d.Provider != "cursor" || d.ClientVersion != "" && !publicVersion.MatchString(d.ClientVersion) {
		return false
	}
	switch d.Stage {
	case "version", "identity_before", "binding_before", "builtin", "identity_after", "binding_after", "workspace":
	default:
		return false
	}
	switch d.Reason {
	case "builtin_terminal_checkmark_unqualified", "builtin_terminal_star_unqualified", "builtin_terminal_miscwin1_unqualified", "builtin_terminal_miscwin2_unqualified", "builtin_terminal_miscwin3_unqualified", "builtin_terminal_miscwin4_unqualified", "builtin_terminal_miscwin5_unqualified", "builtin_terminal_miscwin6_unqualified", "builtin_terminal_miscwin7_unqualified", "builtin_terminal_miscwin8_unqualified", "builtin_terminal_dingbat_unqualified", "builtin_terminal_c1_unqualified", "builtin_terminal_letter_unqualified", "builtin_terminal_number_unqualified", "builtin_terminal_dash_unqualified", "builtin_terminal_quote_unqualified", "builtin_terminal_punct_unqualified", "builtin_terminal_math_unqualified", "builtin_terminal_currency_unqualified", "builtin_terminal_shape_unqualified", "builtin_terminal_pictograph_unqualified", "builtin_terminal_symbol_unqualified", "builtin_terminal_dotset_unqualified", "builtin_terminal_encoding_unqualified", "builtin_terminal_combining_unqualified", "builtin_terminal_spacing_unqualified", "builtin_terminal_format_unqualified":
		return d.Provider == "cursor" && d.Stage == "builtin" && d.ClientVersion == ""
	case "capture_refused", "command_start_failed", "command_read_failed", "command_output_oversized", "command_wait_failed", "command_failed", "deadline", "cancelled", "cleanup_unproven", "version_unqualified", "metadata_not_json", "metadata_unqualified", "not_logged_in":
		return d.ClientVersion == "" || d.Reason == "version_unqualified"
	case "builtin_event_invalid", "builtin_event_unsupported", "builtin_system_unqualified", "builtin_report_repeated", "builtin_report_unqualified", "builtin_report_missing", "builtin_result_unqualified", "builtin_result_error", "builtin_turns_unqualified", "builtin_cost_unqualified", "builtin_model_usage_unqualified", "builtin_tokens_unqualified", "builtin_extra_event", "builtin_incomplete", "builtin_stream_failed", "builtin_terminal_unqualified", "builtin_terminal_unavailable", "builtin_prompt_refused", "builtin_input_unqualified", "builtin_display_unqualified", "builtin_workspace_unqualified", "builtin_state_unqualified", "builtin_write_failed", "builtin_required_tokens_unqualified", "builtin_extra_tokens_unqualified", "builtin_nested_tokens_unqualified", "builtin_terminal_escape_unqualified", "builtin_terminal_mode_unqualified", "builtin_terminal_params_unqualified", "builtin_terminal_operation_unqualified", "builtin_terminal_rune_unqualified", "builtin_terminal_control_unqualified", "builtin_terminal_bounds_unqualified":
		return d.ClientVersion == "" && d.Stage == "builtin"
	}
	return false
}

func emitAcquisitionDiagnostic(out io.Writer, provider string, err error) {
	var detail *diagnosticError
	if !errors.As(err, &detail) {
		return
	}
	d := AcquisitionDiagnostic{diagnosticSchema, provider, detail.stage, detail.reason, detail.version}
	if d.valid() {
		_ = json.NewEncoder(out).Encode(d)
	}
}

// DecodeAcquisitionDiagnostics drops the whole side channel on any unknown or
// malformed frame. No caller may log its original bytes or a decoding error.
func DecodeAcquisitionDiagnostics(raw []byte) []AcquisitionDiagnostic {
	if len(raw) == 0 || len(raw) > DiagnosticMaxBytes || !bytes.HasSuffix(raw, []byte("\n")) {
		return nil
	}
	lines := bytes.Split(raw[:len(raw)-1], []byte("\n"))
	if len(lines) > 3 {
		return nil
	}
	result := make([]AcquisitionDiagnostic, 0, len(lines))
	for _, line := range lines {
		if !providerallowance.ValidJSONDocument(line, 512) || !providerallowance.ExactObjectKeys(line, []string{"schema", "provider", "stage", "reason", "clientVersion"}) {
			return nil
		}
		var d AcquisitionDiagnostic
		if json.Unmarshal(line, &d) != nil || !d.valid() {
			return nil
		}
		result = append(result, d)
	}
	return result
}
