package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/internal/usageconsolidation"
)

const usageExportManifestMaxBytes = 64 << 10

type usageExportManifest struct {
	Selections []usageExportSelection `json:"selections"`
}

// usageExportSelection deliberately contains only caller-provided selectors
// and labels. It is not a Session projection and carries no authority.
type usageExportSelection struct {
	Name        string `json:"name"`
	ExpectedUID string `json:"expectedUID"`
	Project     string `json:"project,omitempty"`
	Repository  string `json:"repository,omitempty"`
	Task        string `json:"task,omitempty"`
}

type usageExportDocument struct {
	Report    usageconsolidation.Report `json:"report"`
	Selection usageExportSelectionView  `json:"selection"`
}

// usageExportSelectionView is a field allowlist for exporter metadata. It
// never embeds an API Session, raw response, request token, or error document.
type usageExportSelectionView struct {
	AllowanceInput string                  `json:"allowanceInput"`
	Reads          []usageExportRead       `json:"reads"`
	Gaps           []string                `json:"gaps"`
	Failure        *usageExportSafeFailure `json:"failure,omitempty"`
}

type usageExportRead struct {
	Name              string             `json:"name"`
	ExpectedUID       string             `json:"expectedUID"`
	DirectObservation string             `json:"directObservation"`
	Capture           usageExportCapture `json:"capture"`
}

type usageExportCapture struct {
	State     string                     `json:"state"`
	Reason    string                     `json:"reason,omitempty"`
	Scope     string                     `json:"scope,omitempty"`
	Coverage  string                     `json:"coverage,omitempty"`
	Retention string                     `json:"retention,omitempty"`
	Overflow  bool                       `json:"overflow,omitempty"`
	Receipts  []usageExportCaptureSource `json:"receipts,omitempty"`
}

// usageExportCaptureSource preserves exact captured-source UID and receipt
// provenance without serializing child names or raw Session content.
type usageExportCaptureSource struct {
	SourceUID  string `json:"sourceUID"`
	ParentUID  string `json:"parentUID"`
	CapturedAt string `json:"capturedAt"`
}

type usageExportSafeFailure struct {
	Stage          string `json:"stage"`
	SelectionIndex int    `json:"selectionIndex"`
}

func (a *app) usageExport(ctx context.Context, args []string) error {
	cmd := newCommand("usage-export")
	var manifestPath, outputDir string
	var allowancePaths stringList
	cmd.fs.StringVar(&manifestPath, "manifest", "", "")
	cmd.fs.StringVar(&outputDir, "output-dir", "", "")
	cmd.fs.Var(&allowancePaths, "allowance-read", "")
	counts := flagCounts(args, "manifest", "output-dir")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || counts["manifest"] != 1 || counts["output-dir"] != 1 || manifestPath == "" || outputDir == "" {
		return usageError("usage-export requires exactly one --manifest and --output-dir, and no positional arguments")
	}
	manifest, err := readUsageExportManifest(manifestPath)
	if err != nil {
		return usageError("usage-export manifest is invalid")
	}
	allowances, err := readAllowanceSnapshots(allowancePaths)
	if err != nil {
		return usageError("usage-export allowance input is invalid")
	}
	if err := createPrivateOutputDir(outputDir); err != nil {
		return usageError("usage-export output directory is unsafe or already exists")
	}
	view := usageExportSelectionView{AllowanceInput: "Unconfigured", Gaps: []string{
		"selection is finite and explicitly named; no list, descendant scan, history, log, console, provider, or auth-store read occurred",
		"a missing, unavailable, disabled, malformed, or overflowed capture is a coverage gap, not zero usage",
	}}
	if len(allowances) > 0 {
		view.AllowanceInput = "CallerProvidedSnapshots"
	} else {
		view.Gaps = append(view.Gaps, "allowance input is Unconfigured; no provider read occurred")
	}

	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	observations := make([]protocol.UsageObservation, 0, len(manifest.Selections))
	associations := make([]usageconsolidation.UIDAssociation, 0, len(manifest.Selections))
	for i, selection := range manifest.Selections {
		read, direct, captured, err := a.usageExportRead(ctx, c, selection)
		view.Reads = append(view.Reads, read)
		if err != nil {
			view.Failure = &usageExportSafeFailure{Stage: "selected_session_read", SelectionIndex: i}
			view.Gaps = append(view.Gaps, "selected read stopped before complete coverage; no later selected session was read")
			return a.finishUsageExport(outputDir, allowances, observations, associations, view, ExitFailed)
		}
		if direct != nil {
			observations = append(observations, *direct)
		}
		observations = append(observations, captured...)
		associations = append(associations, usageconsolidation.UIDAssociation{SessionUID: selection.ExpectedUID, Project: selection.Project, Repository: selection.Repository, Task: selection.Task})
	}
	return a.finishUsageExport(outputDir, allowances, observations, associations, view, ExitOK)
}

func (a *app) usageExportRead(ctx context.Context, c *conn, selection usageExportSelection) (usageExportRead, *protocol.UsageObservation, []protocol.UsageObservation, error) {
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	one.rejectOversizeResponse = true
	var raw json.RawMessage
	status, err := a.callWith(withUsageCapture(ctx), &one, http.MethodGet, apiv1.SessionPath(url.PathEscape(selection.Name)), nil, nil, &raw, false)
	if err != nil {
		return usageExportRead{Name: selection.Name, ExpectedUID: selection.ExpectedUID, DirectObservation: "NotRead", Capture: usageExportCapture{State: "NotRead"}}, nil, nil, err
	}
	var session apiv1.Session
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if status != http.StatusOK || !strictSessionJSON(raw) || dec.Decode(&session) != nil || dec.Decode(new(any)) != io.EOF || session.Name != selection.Name || session.UID != selection.ExpectedUID {
		return usageExportRead{Name: selection.Name, ExpectedUID: selection.ExpectedUID, DirectObservation: "NotAccepted", Capture: usageExportCapture{State: "NotAccepted"}}, nil, nil, errors.New("unconfirmed selected session")
	}
	read := usageExportRead{Name: selection.Name, ExpectedUID: selection.ExpectedUID, DirectObservation: "Unavailable"}
	var direct *protocol.UsageObservation
	if session.UsageObservation != nil {
		candidate := *session.UsageObservation
		switch {
		case candidate.Source.SessionUID != session.UID:
			read.DirectObservation = "SourceUIDMismatch"
			return read, nil, nil, errors.New("unconfirmed direct observation")
		case candidate.Validate() != nil:
			read.DirectObservation = "Invalid"
			return read, nil, nil, errors.New("unconfirmed direct observation")
		default:
			direct = &candidate
			read.DirectObservation = "Included"
		}
	}
	normalized := apiv1.NormalizeCapturedUsage(session.UID, session.CapturedUsage)
	read.Capture = captureMetadata(normalized)
	captured := make([]protocol.UsageObservation, 0, len(normalized.Receipts))
	if normalized.Available {
		for _, receipt := range normalized.Receipts {
			captured = append(captured, *receipt.Observation)
		}
	}
	return read, direct, captured, nil
}

func captureMetadata(capture *apiv1.CapturedUsage) usageExportCapture {
	if capture == nil || !capture.Available {
		if capture == nil {
			return usageExportCapture{State: "Unavailable", Reason: "unavailable"}
		}
		return usageExportCapture{State: "Unavailable", Reason: capture.Reason}
	}
	result := usageExportCapture{State: "Available", Scope: capture.Scope, Coverage: capture.Coverage, Retention: capture.Retention, Overflow: capture.Overflow, Receipts: make([]usageExportCaptureSource, 0, len(capture.Receipts))}
	for _, receipt := range capture.Receipts {
		result.Receipts = append(result.Receipts, usageExportCaptureSource{SourceUID: receipt.Observation.Source.SessionUID, ParentUID: receipt.ParentUID, CapturedAt: receipt.CapturedAt.UTC().Format("2006-01-02T15:04:05Z07:00")})
	}
	sort.Slice(result.Receipts, func(i, j int) bool { return result.Receipts[i].SourceUID < result.Receipts[j].SourceUID })
	return result
}

func (a *app) finishUsageExport(outputDir string, allowances []providerallowance.ReadResponse, observations []protocol.UsageObservation, associations []usageconsolidation.UIDAssociation, view usageExportSelectionView, code int) error {
	report, err := usageconsolidation.Project(usageconsolidation.Input{AllowanceSnapshots: allowances, InitialInvocations: observations, Associations: associations}, usageconsolidation.DefaultLimits)
	if err != nil {
		return fail(ExitFailed, "usage-export could not project validated evidence")
	}
	doc := usageExportDocument{Report: report, Selection: view}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fail(ExitFailed, "usage-export could not encode private report")
	}
	raw = append(raw, '\n')
	summary := usageExportSummary(view, report)
	if view.Failure != nil {
		summary += "coverage: stopped before all selected reads completed\n"
	} else {
		summary += "coverage: selected reads completed; omitted work remains a gap\n"
	}
	if err := writePrivateOutput(outputDir, "report.json", raw); err != nil {
		return fail(ExitFailed, "usage-export could not write private report")
	}
	if err := writePrivateOutput(outputDir, "summary.txt", []byte(summary)); err != nil {
		return fail(ExitFailed, "usage-export could not write private summary")
	}
	a.outf("private usage export written: %s\n", outputDir)
	if code != ExitOK {
		return fail(code, "usage-export stopped; private coverage report was written")
	}
	return nil
}

// usageExportSummary is intentionally a small readable rendering of the
// validated C1 report. It neither exposes the selected Session nor makes an
// account, currentness, allowance, provider, or charge claim.
func usageExportSummary(view usageExportSelectionView, report usageconsolidation.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "private usage export\nselected reads: %d\nattribution rows: %d\nwithheld conflicts: %d\n", len(view.Reads), len(report.Attribution.Rows), len(report.Attribution.WithheldConflicts))
	fmt.Fprintf(&b, "allowance snapshots: %s\n", view.AllowanceInput)
	fmt.Fprintf(&b, "allowance currentness: %s\n", report.Allowance.CurrentnessAtReportGeneration)
	for i, row := range report.Attribution.Rows {
		u := row.Observation
		fmt.Fprintf(&b, "row %d:\n", i+1)
		fmt.Fprintf(&b, "  session: %s\n  attempt: %s\n  observedAt: %s\n  scope: initial-only\n", quoteUsageValue(u.Source.SessionUID), quoteUsageValue(u.Source.AttemptID), quoteUsageValue(u.ObservedAt.UTC().Format("2006-01-02T15:04:05Z07:00")))
		fmt.Fprintf(&b, "  requested model: %s\n  actual model state: %s\n  provenance: %s\n", quoteUsageValue(u.Source.RequestedModel), quoteUsageValue(u.ActualModelState), quoteUsageValue(u.Provenance))
		if len(u.ServedModels) == 0 {
			fmt.Fprintln(&b, "  served models: Unknown")
		} else {
			for _, model := range u.ServedModels {
				fmt.Fprintf(&b, "  served model: %s; input=%s output=%s cache-read=%s cache-creation=%s; %s\n", quoteUsageValue(model.Model), tokenText(model.InputTokens), tokenText(model.OutputTokens), tokenText(model.CacheReadInputTokens), tokenText(model.CacheCreationInputTokens), costText(model.Cost))
			}
		}
		fmt.Fprintf(&b, "  tokens: input=%s output=%s cache-read=%s cache-creation=%s\n  %s\n", tokenText(u.InputTokens), tokenText(u.OutputTokens), tokenText(u.CacheReadInputTokens), tokenText(u.CacheCreationInputTokens), costText(u.Cost))
	}
	if view.Failure != nil {
		fmt.Fprintln(&b, "coverage: stopped before all selected reads completed")
	} else {
		fmt.Fprintln(&b, "coverage: selected reads completed; omitted work remains a gap")
	}
	return b.String()
}

func quoteUsageValue(value string) string { return strconv.Quote(value) }

func tokenText(token protocol.TokenObservation) string {
	if token.State != protocol.UsageReported || token.Amount == nil {
		return "Unknown"
	}
	return strconv.FormatInt(*token.Amount, 10)
}

func costText(cost protocol.CostObservation) string {
	if cost.State != protocol.UsageReported {
		return "CLI-equivalent estimate: Unknown (NOT charge)"
	}
	return "CLI-equivalent estimate: " + quoteUsageValue(cost.Amount) + " " + quoteUsageValue(cost.Unit) + " (NOT charge)"
}

func readUsageExportManifest(path string) (usageExportManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || len(raw) > usageExportManifestMaxBytes {
		return usageExportManifest{}, errors.New("manifest read")
	}
	if !strictWireJSON(raw, reflect.TypeFor[usageExportManifest]()) {
		return usageExportManifest{}, errors.New("manifest shape")
	}
	var manifest usageExportManifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&manifest) != nil || dec.Decode(new(any)) != io.EOF || len(manifest.Selections) == 0 || len(manifest.Selections) > usageconsolidation.DefaultLimits.MaxInitialInvocations {
		return usageExportManifest{}, errors.New("manifest decode")
	}
	names, uids := map[string]struct{}{}, map[string]struct{}{}
	for _, selection := range manifest.Selections {
		if !canaryName.MatchString(selection.Name) || !apiv1.ValidContextUID(selection.ExpectedUID) || strings.TrimSpace(selection.Project) != selection.Project || strings.TrimSpace(selection.Repository) != selection.Repository || strings.TrimSpace(selection.Task) != selection.Task {
			return usageExportManifest{}, errors.New("manifest selection")
		}
		if _, duplicate := names[selection.Name]; duplicate {
			return usageExportManifest{}, errors.New("duplicate name")
		}
		if _, duplicate := uids[selection.ExpectedUID]; duplicate {
			return usageExportManifest{}, errors.New("duplicate uid")
		}
		names[selection.Name], uids[selection.ExpectedUID] = struct{}{}, struct{}{}
	}
	return manifest, nil
}

func readAllowanceSnapshots(paths []string) ([]providerallowance.ReadResponse, error) {
	if len(paths) > usageconsolidation.DefaultLimits.MaxAllowanceSnapshots {
		return nil, errors.New("too many allowance inputs")
	}
	result := make([]providerallowance.ReadResponse, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		read, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
		if err != nil {
			return nil, err
		}
		result = append(result, read)
	}
	return result, nil
}

func createPrivateOutputDir(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil || abs == string(filepath.Separator) || hasSymlinkComponent(filepath.Dir(abs)) {
		return errors.New("unsafe output path")
	}
	if err := os.Mkdir(abs, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("private output mode")
	}
	return nil
}

func hasSymlinkComponent(path string) bool {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&fs.ModeSymlink != 0 {
			return true
		}
		if current == string(filepath.Separator) {
			return false
		}
	}
}

func writePrivateOutput(dir, name string, data []byte) error {
	if name != "report.json" && name != "summary.txt" || hasSymlinkComponent(dir) {
		return errors.New("unsafe output")
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil || info.Mode().Perm() != 0o600 {
		return errors.New("private output mode")
	}
	return nil
}

func flagCounts(args []string, names ...string) map[string]int {
	counts := make(map[string]int, len(names))
	for _, arg := range args {
		for _, name := range names {
			if arg == "--"+name || arg == "-"+name || strings.HasPrefix(arg, "--"+name+"=") || strings.HasPrefix(arg, "-"+name+"=") {
				counts[name]++
			}
		}
	}
	return counts
}
