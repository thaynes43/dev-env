package usagecollector

import (
	"bytes"
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
	case "capture_refused", "command_start_failed", "command_read_failed", "command_output_oversized", "command_wait_failed", "command_failed", "deadline", "cancelled", "cleanup_unproven", "version_unqualified", "metadata_not_json", "metadata_unqualified", "not_logged_in":
		return d.ClientVersion == "" || d.Reason == "version_unqualified"
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
