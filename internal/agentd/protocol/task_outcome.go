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

var taskOutcomePR = regexp.MustCompile(`^https://github\.com/thaynes43/[A-Za-z0-9._-]+/pull/[0-9]{1,6}$`)
var taskOutcomeSHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

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
		if !unicode.IsPrint(c) {
			return errors.New("task outcome summary must be printable")
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
	if !ValidTaskBudgetIdentifier(o.SessionUID) || !ValidTaskBudgetIdentifier(o.PodUID) {
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
