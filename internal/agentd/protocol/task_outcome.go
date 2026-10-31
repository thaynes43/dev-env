package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode"
	"unicode/utf8"
)

const MaxTaskOutcomeBytes = 2048

// TaskOutcomePRURLPattern's repository segment is never "." or "..": a browser
// would normalize either off the organization. ".github" and other names stay
// valid. The CRD carries the same pattern, and the portal links only a URL
// that matches it.
const TaskOutcomePRURLPattern = `^https://github\.com/thaynes43/(?:\.?[A-Za-z0-9_-][A-Za-z0-9._-]*|\.\.[A-Za-z0-9._-]+)/pull/[0-9]{1,6}$`

var taskOutcomePR = regexp.MustCompile(TaskOutcomePRURLPattern)
var taskOutcomeSHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

// taskOutcomeIdentifier is the CRD's sessionUID and podUID pattern. It is
// narrower than ValidTaskBudgetIdentifier, which also allows ':'.
var taskOutcomeIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// unassignedInUnicode15BMP lists the Basic Multilingual Plane code points that
// Go 1.27's tables (Unicode 17.0) call printable but Unicode 15.0 left
// unassigned. The kube-apiserver checks the CRD's summary pattern with its own
// Go's tables (Kubernetes 1.35: Go 1.25, Unicode 15.0) and refuses these.
// Derived 2026-10-31 by matching every rune against the CRD pattern under
// go1.25.5 and against unicode.IsPrint under go1.27.2.
// TestTaskOutcomeSchemaAcceptsEveryOperatorRune in api/v1alpha1 checks the
// result against envtest's API server, so a newer Go fails there first.
var unassignedInUnicode15BMP = &unicode.RangeTable{R16: []unicode.Range16{
	{Lo: 0x088f, Hi: 0x088f, Stride: 1}, {Lo: 0x0897, Hi: 0x0897, Stride: 1},
	{Lo: 0x0c5c, Hi: 0x0c5c, Stride: 1}, {Lo: 0x0cdc, Hi: 0x0cdc, Stride: 1},
	{Lo: 0x1acf, Hi: 0x1add, Stride: 1}, {Lo: 0x1ae0, Hi: 0x1aeb, Stride: 1},
	{Lo: 0x1b4e, Hi: 0x1b4f, Stride: 1}, {Lo: 0x1b7f, Hi: 0x1b7f, Stride: 1},
	{Lo: 0x1c89, Hi: 0x1c8a, Stride: 1}, {Lo: 0x20c1, Hi: 0x20c1, Stride: 1},
	{Lo: 0x2427, Hi: 0x2429, Stride: 1}, {Lo: 0x2b96, Hi: 0x2b96, Stride: 1},
	{Lo: 0x2ffc, Hi: 0x2fff, Stride: 1}, {Lo: 0x31e4, Hi: 0x31e5, Stride: 1},
	{Lo: 0x31ef, Hi: 0x31ef, Stride: 1}, {Lo: 0xa7cb, Hi: 0xa7cf, Stride: 1},
	{Lo: 0xa7d2, Hi: 0xa7d2, Stride: 1}, {Lo: 0xa7d4, Hi: 0xa7d4, Stride: 1},
	{Lo: 0xa7da, Hi: 0xa7dc, Stride: 1}, {Lo: 0xa7f1, Hi: 0xa7f1, Stride: 1},
	{Lo: 0xfbc3, Hi: 0xfbd2, Stride: 1}, {Lo: 0xfd90, Hi: 0xfd91, Stride: 1},
	{Lo: 0xfdc8, Hi: 0xfdce, Stride: 1},
}}

// taskOutcomeSummaryRune is a conservative class that both this binary and
// the API server's schema accept: a printable rune in the Basic Multilingual
// Plane that Unicode 15.0 already assigned. Supplementary-plane runes, such as
// emoji, are refused whatever either side's tables say.
func taskOutcomeSummaryRune(r rune) bool {
	return r <= 0xffff && unicode.IsPrint(r) && !unicode.Is(unassignedInUnicode15BMP, r)
}

// TaskOutcomeRecord is the task-authored allowlist, never harvested output.
type TaskOutcomeRecord struct {
	PRURLs   []string `json:"prURLs,omitempty"`
	HeadSHAs []string `json:"headSHAs,omitempty"`
	Summary  string   `json:"summary"`
}

// TaskOutcome binds the exit-time snapshot to its original executor.
type TaskOutcome struct {
	SessionUID string            `json:"sessionUID"`
	PodUID     string            `json:"podUID"`
	Record     TaskOutcomeRecord `json:"record"`
}

func (r TaskOutcomeRecord) Validate() error {
	if len(r.PRURLs) > 3 || len(r.HeadSHAs) > 3 || len(r.Summary) == 0 || utf8.RuneCountInString(r.Summary) > 200 || !utf8.ValidString(r.Summary) {
		return errors.New("invalid task outcome bounds")
	}
	for _, c := range r.Summary {
		if !taskOutcomeSummaryRune(c) {
			return errors.New("task outcome summary must be printable Basic Multilingual Plane text")
		}
	}
	for _, p := range r.PRURLs {
		if len(p) > 256 || !taskOutcomePR.MatchString(p) {
			return errors.New("invalid task outcome PR URL")
		}
	}
	for _, h := range r.HeadSHAs {
		if !taskOutcomeSHA.MatchString(h) {
			return errors.New("invalid task outcome head SHA")
		}
	}
	return nil
}

func (o TaskOutcome) Validate() error {
	if !taskOutcomeIdentifier.MatchString(o.SessionUID) || !taskOutcomeIdentifier.MatchString(o.PodUID) {
		return errors.New("invalid task outcome identity")
	}
	return o.Record.Validate()
}

// DecodeTaskOutcomeRecord refuses the whole bounded document, including unknown
// fields and trailing JSON. No raw text is retained on failure.
func DecodeTaskOutcomeRecord(data []byte) (*TaskOutcomeRecord, error) {
	if len(data) > MaxTaskOutcomeBytes || !utf8.Valid(data) {
		return nil, errors.New("task outcome exceeds byte bound or contains invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("task outcome must be an object")
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, errors.New("invalid task outcome JSON")
		}
		name, ok := key.(string)
		if !ok || (name != "summary" && name != "prURLs" && name != "headSHAs") {
			return nil, errors.New("unknown task outcome field")
		}
		if _, exists := fields[name]; exists {
			return nil, errors.New("duplicate task outcome field")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, errors.New("invalid task outcome field")
		}
		fields[name] = value
	}
	if token, err := dec.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid task outcome JSON")
	}
	if dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing task outcome JSON")
	}
	var r TaskOutcomeRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, errors.New("invalid task outcome JSON")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}
