package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func checkinsFixture(t *testing.T, name string) (map[string]any, map[string]any) {
	t.Helper()
	doc := loadDoc(t, "checkins-"+name+".json")
	return doc["export"].(map[string]any), doc["contexts"].(map[string]any)
}

func checkinsCase(t *testing.T, name string, code int) checkinsPlan {
	t.Helper()
	export, contexts := checkinsFixture(t, name)
	plan, got := planCheckins(mustMarshal(t, export), mustMarshal(t, contexts))
	if got != code {
		t.Fatalf("%s exit %d want %d: %#v", name, got, code, plan)
	}
	if !plan.DryRun || plan.Executed || plan.CheckerAuthority != "none" {
		t.Fatalf("authority %#v", plan)
	}
	for _, item := range plan.Items {
		if !item.DryRun || item.Executed || item.CheckerAuthority != "none" {
			t.Fatalf("item authority %#v", item)
		}
		if item.Plan != nil && (!item.Plan.DryRun || item.Plan.Executed || item.Plan.CheckerAuthority != "none" || item.Plan.OverallDone) {
			t.Fatalf("planner authority %#v", item.Plan)
		}
	}
	return plan
}

func TestCheckinsTriageFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		intents      int
	}{
		{"known-due", "planned", 1}, {"not-due", "current", 0}, {"blocked", "blocked", 0}, {"missing-ack", "planned", 1}, {"unknown", "unknown", 0},
		{"incomplete-history", "incomplete", 0}, {"partial-page", "incomplete", 0}, {"selected-page", "incomplete", 0}, {"last-page", "incomplete", 0},
		{"unknown-owner", "unbound", 0}, {"missing-owner", "unbound", 0}, {"no-context", "unbound", 0}, {"pre-pr", "unbound", 0}, {"unknown-export-fields", "planned", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := checkinsCase(t, tc.name, 0)
			if len(p.Items) != 1 {
				t.Fatalf("items %#v", p.Items)
			}
			item := p.Items[0]
			if item.Status != tc.status {
				t.Fatalf("status %s reasons %#v plan %#v", item.Status, item.Reasons, item.Plan)
			}
			if item.Plan != nil && len(item.Plan.Intents) != tc.intents {
				t.Fatalf("intents %#v", item.Plan.Intents)
			}
			if tc.intents > 0 && item.Plan == nil {
				t.Fatal("missing plan")
			}
			export, _ := checkinsFixture(t, tc.name)
			var source map[string]any
			if err := json.Unmarshal(item.Sources[0], &source); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(source, export["items"].([]any)[0]) {
				t.Fatal("canonical source evidence was changed")
			}
			if tc.status == "incomplete" && len(item.Reasons) == 0 {
				t.Fatal("coverage was hidden")
			}
			if tc.name == "partial-page" && (!bytes.Contains(p.Visibility, []byte(`"has_more":true`)) || !bytes.Contains(p.Visibility, []byte(`"owner_filter_scope":"selected page"`))) {
				t.Fatalf("visibility %s", p.Visibility)
			}
		})
	}
}

func TestCheckinsExactBinding(t *testing.T) {
	for _, field := range []string{"owner", "story", "task", "uid", "repository", "pullRequest", "head", "coordinator"} {
		t.Run(field, func(t *testing.T) {
			p := checkinsCase(t, "mismatch-"+field, 0)
			if p.Items[0].Bound || p.Items[0].Plan != nil || p.Items[0].Status != "unbound" || len(p.Items[0].Reasons) == 0 {
				t.Fatalf("foreign binding %#v", p.Items[0])
			}
		})
	}
	due := checkinsCase(t, "known-due", 0).Items[0]
	if !due.Bound || due.Plan.Task != "synthetic-task-9001" || due.Plan.UID != "uid-synthetic-task-9001" || due.Plan.Story != "9001" || due.Plan.PullRequest != 9002 {
		t.Fatalf("binding %#v", due)
	}
}

func TestCheckinsDuplicatesAndOrder(t *testing.T) {
	base := checkinsCase(t, "known-due", 0)
	duplicate := checkinsCase(t, "duplicate", 0)
	if !reflect.DeepEqual(base, duplicate) {
		t.Fatal("exact duplicate changed plan")
	}
	for _, name := range []string{"conflicting-item", "conflicting-context"} {
		t.Run(name, func(t *testing.T) {
			first := checkinsCase(t, name, 3)
			export, contexts := checkinsFixture(t, name)
			items := export["items"].([]any)
			for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
				items[i], items[j] = items[j], items[i]
			}
			ctx := contexts["contexts"].([]any)
			for i, j := 0, len(ctx)-1; i < j; i, j = i+1, j-1 {
				ctx[i], ctx[j] = ctx[j], ctx[i]
			}
			second, code := planCheckins(mustMarshal(t, export), mustMarshal(t, contexts))
			if code != 3 || !reflect.DeepEqual(first, second) || first.Items[0].Plan != nil || first.Items[0].Bound {
				t.Fatalf("conflict picked a winner %#v %#v", first, second)
			}
		})
	}
	export, contexts := checkinsFixture(t, "known-due")
	other := cloneDoc(export["items"].([]any)[0].(map[string]any))
	other["id"] = 9004
	export["items"] = append(export["items"].([]any), other)
	first, _ := planCheckins(mustMarshal(t, export), mustMarshal(t, contexts))
	export["items"] = []any{other, export["items"].([]any)[0]}
	second, _ := planCheckins(mustMarshal(t, export), mustMarshal(t, contexts))
	if !reflect.DeepEqual(first, second) {
		t.Fatal("item order changed output")
	}
}

func TestCheckinsReusesWatchdogSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, status  string
		code, intents int
	}{
		{"accepted", "planned", 0, 1}, {"acknowledged", "planned", 0, 1}, {"ambiguous", "current", 0, 0},
		{"receipt-replay", "current", 0, 0}, {"receipt-conflict", "blocked", 0, 0}, {"refused-receipt", "refused", 2, 0}, {"refused-handoff", "blocked", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := checkinsCase(t, tc.name, tc.code)
			plan := p.Items[0].Plan
			if plan == nil || plan.Status != tc.status || len(plan.Intents) != tc.intents || plan.OverallDone {
				t.Fatalf("semantics %#v", plan)
			}
			export, contexts := checkinsFixture(t, tc.name)
			var ctx checkinContext
			if err := json.Unmarshal(mustMarshal(t, contexts["contexts"].([]any)[0].(map[string]any)), &ctx); err != nil {
				t.Fatal(err)
			}
			due := export["items"].([]any)[0].(map[string]any)["check_after"].(string)
			ob, _ := json.Marshal(obligationIn{ID: "checkin-9001", Kind: ctx.Kind, Owner: ctx.Coordinator, DueAt: &due, OwnerEscalationDueAt: ctx.OwnerEscalationDueAt})
			wrapper, _ := json.Marshal(watchdogDocument{Schema: watchdogSchema, ObservedAt: contexts["observedAt"].(string), Handoff: ctx.Handoff, Obligations: []json.RawMessage{ob}, Receipts: ctx.Receipts})
			direct, _ := planWatchdog(wrapper)
			if !reflect.DeepEqual(*plan, direct) {
				t.Fatal("adapter diverged from existing reducer")
			}
		})
	}
}

func TestCheckinsPlannedReceiptIdempotency(t *testing.T) {
	export, contexts := checkinsFixture(t, "known-due")
	first := checkinsCase(t, "known-due", 0)
	ctx := contexts["contexts"].([]any)[0].(map[string]any)
	ctx["receipts"] = receiptAny(first.Items[0].Plan.NextReceipts)
	p, code := planCheckins(mustMarshal(t, export), mustMarshal(t, contexts))
	if code != 0 || len(p.Items[0].Plan.Intents) != 0 || len(p.Items[0].Plan.Receipts) != 1 || p.Items[0].Plan.OverallDone {
		t.Fatalf("replay %#v", p)
	}
}

func TestCheckinsInputRefusals(t *testing.T) {
	_ = checkinsCase(t, "unknown-context-field", 2)
	export, contexts := checkinsFixture(t, "known-due")
	e, c := mustMarshal(t, export), mustMarshal(t, contexts)
	for _, tc := range []struct {
		name string
		e, c []byte
	}{
		{"oversized-export", bytes.Repeat([]byte(" "), maxDocumentBytes+1), c},
		{"oversized-context", e, bytes.Repeat([]byte(" "), maxDocumentBytes+1)},
		{"duplicate-key", []byte(`{"items":[],"items":[]}`), c},
		{"nested-duplicate-key", []byte(`{"metadata":{"future":{"Task":1,"Task":2}}}`), c},
		{"case-variant", bytes.Replace(e, []byte(`"owner"`), []byte(`"Owner"`), 1), c},
		{"trailing", append(append([]byte{}, e...), []byte(` {}`)...), c},
		{"null-export", []byte(`null`), c},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, code := planCheckins(tc.e, tc.c)
			if code != 2 || p.Status != "refused" || len(p.Items) != 0 {
				t.Fatalf("refusal %d %#v", code, p)
			}
		})
	}
}

type checkinsDenyHTTP struct{ t *testing.T }

func (d checkinsDenyHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	d.t.Error("offline mode attempted HTTP")
	return nil, errors.New("network unavailable")
}

func TestCheckinsCLINoNetworkOrProcess(t *testing.T) {
	// There are no network/process primitives in the entire production package.
	// Fail closed on any new import, including syscall, unsafe, cgo or a helper
	// dependency that could bypass the HTTP guard or start a process.
	allowed := map[string]bool{"bytes": true, "encoding/json": true, "errors": true, "fmt": true, "io": true, "os": true, "slices": true, "strconv": true, "strings": true, "time": true}
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if imp.Name != nil {
				t.Fatalf("production import alias can hide side effects: %s", path)
			}
			if !allowed[name] {
				t.Fatalf("production dependency %s can introduce network/process effects", name)
			}
		}
		osSelectors := map[string]bool{"Open": true, "Exit": true, "Args": true, "Stdin": true, "Stdout": true, "Stderr": true}
		ast.Inspect(f, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if ok && pkg.Name == "os" && !osSelectors[selector.Sel.Name] {
				t.Fatalf("production os.%s can introduce mutation/process effects", selector.Sel.Name)
			}
			return true
		})
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	previous := http.DefaultTransport
	http.DefaultTransport = checkinsDenyHTTP{t}
	t.Cleanup(func() { http.DefaultTransport = previous })
	before, err := os.ReadFile("testdata/checkins-export.json")
	if err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := run([]string{"--checkins", "testdata/checkins-export.json", "--contexts", "testdata/checkins-contexts.json"}, nil, &out, &errw); code != 0 {
		t.Fatalf("offline CLI %d %s", code, errw.String())
	}
	var plan checkinsPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 || len(plan.Items[0].Plan.Intents) != 1 {
		t.Fatalf("offline plan %#v", plan)
	}
	after, err := os.ReadFile("testdata/checkins-export.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
	out.Reset()
	if code := run([]string{"--checkins", "-", "--contexts", "testdata/checkins-contexts.json"}, bytes.NewReader(before), &out, &errw); code != 0 {
		t.Fatalf("stdin %d %s", code, errw.String())
	}
	for _, args := range [][]string{{"--checkins"}, {"--checkins", "missing", "--contexts", "testdata/checkins-contexts.json"}, {"--checkins", "-", "--contexts", "-"}} {
		out.Reset()
		if code := run(args, bytes.NewReader(before), &out, &errw); code != 2 || !strings.Contains(out.String(), `"dryRun": true`) {
			t.Fatalf("CLI refusal %d %s", code, out.String())
		}
	}
}
