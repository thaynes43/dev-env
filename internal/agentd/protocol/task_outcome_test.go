package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// The kube-apiserver checks the CRD pattern with its own Go's Unicode tables
// (Kubernetes 1.35: Go 1.25, Unicode 15.0). A rune this binary's newer tables
// call printable must not pass here when that schema would refuse it.
func TestTaskOutcomeSummaryRefusesRunesTheSchemaCannot(t *testing.T) {
	for name, summary := range map[string]string{
		"supplementary plane (U+1FAE9, Unicode 16)": "tired \U0001FAE9",
		"supplementary emoji (U+1F600)":             "done \U0001F600",
		"BMP Unicode 17 (U+20C1)":                   "price ⃁",
		"BMP Unicode 16 (U+A7CB)":                   "Ɤ",
		"BMP Unicode 15.1 (U+2FFC)":                 "⿼",
	} {
		t.Run(name, func(t *testing.T) {
			if (TaskOutcomeRecord{Summary: summary}).Validate() == nil {
				t.Fatalf("summary %q accepted", summary)
			}
		})
	}
	for _, summary := range []string{"Ready for lead review", "界 café — ½ done", "Größe: ≤ 3 PRs, §2 ✓"} {
		if err := (TaskOutcomeRecord{Summary: summary}).Validate(); err != nil {
			t.Fatalf("summary %q refused: %v", summary, err)
		}
	}
}

// A browser normalizes "." and ".." path segments, so either would move a
// thaynes43 link off the organization.
func TestTaskOutcomePRURLRefusesDotSegments(t *testing.T) {
	for _, u := range []string{
		"https://github.com/thaynes43/../pull/1",
		"https://github.com/thaynes43/./pull/1",
	} {
		if (TaskOutcomeRecord{Summary: "ok", PRURLs: []string{u}}).Validate() == nil {
			t.Fatalf("%s accepted", u)
		}
	}
	for _, u := range []string{
		"https://github.com/thaynes43/dev-env/pull/1",
		"https://github.com/thaynes43/.github/pull/2",
		"https://github.com/thaynes43/a.b_c-d/pull/3",
	} {
		if err := (TaskOutcomeRecord{Summary: "ok", PRURLs: []string{u}}).Validate(); err != nil {
			t.Fatalf("%s refused: %v", u, err)
		}
	}
}

// The identity check matches the CRD pattern exactly; the task budget's wider
// identifier (which allows ':') is not used here.
func TestTaskOutcomeIdentityMatchesSchema(t *testing.T) {
	record := TaskOutcomeRecord{Summary: "ok"}
	for _, o := range []TaskOutcome{
		{SessionUID: "session:uid", PodUID: "pod-uid", Record: record},
		{SessionUID: "session-uid", PodUID: "pod:uid", Record: record},
		{SessionUID: "-session", PodUID: "pod-uid", Record: record},
		{SessionUID: strings.Repeat("a", 129), PodUID: "pod-uid", Record: record},
	} {
		if o.Validate() == nil {
			t.Fatalf("identity %q/%q accepted", o.SessionUID, o.PodUID)
		}
	}
	if err := (TaskOutcome{SessionUID: "0b3c6a54-2f8e-4c51-9d55-6a3c1f1e2b7d", PodUID: "pod.uid_1", Record: record}).Validate(); err != nil {
		t.Fatal(err)
	}
}

// The portal links a pull request URL only when it matches this pattern, so it
// must stay the one the generated CRD enforces.
func TestTaskOutcomePRURLPatternMatchesTheCRD(t *testing.T) {
	crd, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "crd", "dev-env.haynesops.com_agentsessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(crd), "pattern: "+TaskOutcomePRURLPattern+"\n") {
		t.Fatal("the CRD's prURLs pattern differs from TaskOutcomePRURLPattern")
	}
}
