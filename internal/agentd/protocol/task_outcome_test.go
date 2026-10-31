package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskOutcomeRecordValidation(t *testing.T) {
	valid := TaskOutcomeRecord{PRURLs: []string{"https://github.com/thaynes43/dev-env/pull/123"}, HeadSHAs: []string{strings.Repeat("a", 40)}, Summary: "Ready for lead review"}
	for name, mutate := range map[string]func(*TaskOutcomeRecord){
		"valid":        func(*TaskOutcomeRecord) {},
		"invalid URL":  func(r *TaskOutcomeRecord) { r.PRURLs[0] += "?q=x" },
		"off org":      func(r *TaskOutcomeRecord) { r.PRURLs[0] = "https://github.com/other/dev-env/pull/1" },
		"bad SHA":      func(r *TaskOutcomeRecord) { r.HeadSHAs[0] = strings.Repeat("g", 40) },
		"too many PRs": func(r *TaskOutcomeRecord) { r.PRURLs = append(r.PRURLs, r.PRURLs[0], r.PRURLs[0], r.PRURLs[0]) },
		"too many SHAs": func(r *TaskOutcomeRecord) {
			r.HeadSHAs = append(r.HeadSHAs, r.HeadSHAs[0], r.HeadSHAs[0], r.HeadSHAs[0])
		},
		"oversized summary": func(r *TaskOutcomeRecord) { r.Summary = strings.Repeat("x", 201) },
		"empty summary":     func(r *TaskOutcomeRecord) { r.Summary = "" },
		"newline":           func(r *TaskOutcomeRecord) { r.Summary = "one\ntwo" },
		"escape":            func(r *TaskOutcomeRecord) { r.Summary = "\x1b[31mred" },
		"unicode":           func(r *TaskOutcomeRecord) { r.Summary = "review\u2028now" },
	} {
		t.Run(name, func(t *testing.T) {
			r := valid
			r.PRURLs = append([]string(nil), valid.PRURLs...)
			r.HeadSHAs = append([]string(nil), valid.HeadSHAs...)
			mutate(&r)
			raw, _ := json.Marshal(r)
			got, err := DecodeTaskOutcomeRecord(raw)
			if (err == nil) != (name == "valid") {
				t.Fatalf("validation got=%v err=%v", got, err)
			}
		})
	}
	raw, _ := json.Marshal(valid)
	for name, data := range map[string][]byte{"duplicate": []byte(`{"summary":"one","summary":"two"}`), "case alias": []byte(`{"Summary":"ok"}`), "unknown": []byte(`{"summary":"ok","prompt":"private"}`), "trailing": append(append([]byte(nil), raw...), raw...), "oversized": append(append([]byte(nil), raw...), []byte(strings.Repeat(" ", MaxTaskOutcomeBytes))...), "invalid UTF8": append([]byte(`{"summary":"`), append([]byte{0xff}, []byte(`"}`)...)...), "null": []byte(`null`), "malformed": []byte(`{"summary":`)} {
		t.Run(name, func(t *testing.T) {
			if got, err := DecodeTaskOutcomeRecord(data); err == nil || got != nil {
				t.Fatal("invalid document accepted", got, err)
			}
		})
	}
}

func TestTaskOutcomePrintableUnicode(t *testing.T) {
	r := TaskOutcomeRecord{Summary: strings.Repeat("界", 200)}
	raw, _ := json.Marshal(r)
	if got, err := DecodeTaskOutcomeRecord(raw); err != nil || got.Summary != r.Summary {
		t.Fatal("printable 200-character summary refused", err)
	}
	r.Summary += "界"
	if r.Validate() == nil {
		t.Fatal("201 Unicode characters accepted")
	}
}
