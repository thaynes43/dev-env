package authority

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

const validTemplates = `
version: 1
templates:
  ops-audit:
    agent: claude
    model: claude-opus-5-5
    effort: xhigh
    size: M
    profile: dev
    repos: [haynes-ops, dev-env]
    modes: [headless]
    maxDuration: 2h
    admits: [human, approved]
  ops-desk:
    agent: claude
    model: claude-sonnet-5-5
    effort: high
    size: S
    profile: ops
    repos: [haynes-ops]
    modes: [interactive, headless]
    maxDuration: 40m
    idleSuspendAfter: 4h
    admits: [session, human, approved]
  phone:
    agent: claude
    model: claude-haiku-4-5
    size: S
    profile: ops
    repos: [haynes-ops]
    modes: [interactive]
    admits: [approved]
`

func wantValid() map[string]Template {
	return map[string]Template{
		"ops-audit": {Agent: "claude", Model: "claude-opus-5-5", Effort: "xhigh", Size: "M", Profile: "dev",
			Repos: []string{"haynes-ops", "dev-env"}, Modes: []string{"headless"}, MaxDuration: 2 * time.Hour,
			Admits: []Basis{BasisHuman, BasisApproved}},
		"ops-desk": {Agent: "claude", Model: "claude-sonnet-5-5", Effort: "high", Size: "S", Profile: "ops",
			Repos: []string{"haynes-ops"}, Modes: []string{"interactive", "headless"}, MaxDuration: 40 * time.Minute,
			IdleSuspendAfter: 4 * time.Hour, Admits: []Basis{BasisSession, BasisHuman, BasisApproved}},
		"phone": {Agent: "claude", Model: "claude-haiku-4-5", Size: "S", Profile: "ops",
			Repos: []string{"haynes-ops"}, Modes: []string{"interactive"}, Admits: []Basis{BasisApproved}},
	}
}

func data(doc string) map[string]string { return map[string]string{TemplatesKey: doc} }

func TestParseTemplatesAccepts(t *testing.T) {
	got, err := ParseTemplates(data(validTemplates))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantValid()) {
		t.Errorf("ParseTemplates =\n%+v\nwant\n%+v", got, wantValid())
	}
	asJSON, err := yaml.YAMLToJSON([]byte(validTemplates))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParseTemplates(data(string(asJSON))); err != nil || !reflect.DeepEqual(got, wantValid()) {
		t.Errorf("the same document as JSON: %v, %v", got, err)
	}
	at100 := strings.Repeat("a", 100)
	if got, err := ParseTemplates(data(edit(t, "repos: [haynes-ops, dev-env]", "repos: ["+at100+"]"))); err != nil || got["ops-audit"].Repos[0] != at100 {
		t.Errorf("a 100-character repository name: %v, %v", got, err)
	}
	for name, d := range map[string]map[string]string{
		"absent ConfigMap":   nil,
		"no data":            {},
		"no templates":       data("version: 1\n"),
		"empty templates":    data("version: 1\ntemplates: {}\n"),
		"templates are null": data("version: 1\ntemplates: null\n"),
	} {
		got, err := ParseTemplates(d)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("%s: ParseTemplates = %v, %v; want no templates and no error", name, got, err)
		}
		if _, err := Resolve(got, "ops-audit", "haynes-ops"); !errors.Is(err, ErrUnknownTemplate) {
			t.Errorf("%s: Resolve = %v, want ErrUnknownTemplate: nothing dispatches", name, err)
		}
	}
}

// edit replaces old, which must occur exactly once in validTemplates, with
// new.
func edit(t *testing.T, old, new string) string {
	t.Helper()
	if n := strings.Count(validTemplates, old); n != 1 {
		t.Fatalf("%q occurs %d times in the valid document", old, n)
	}
	return strings.Replace(validTemplates, old, new, 1)
}

const auditModel = "    model: claude-opus-5-5\n"

// Each fault refuses the whole document: no template is returned, the valid
// ones included, and the error names the fault.
func TestParseTemplatesRefuses(t *testing.T) {
	manyRepos := "[" + strings.TrimSuffix(strings.Repeat("r,", MaxTemplateRepos+1), ",") + "]"
	var many strings.Builder
	many.WriteString("version: 1\ntemplates:\n")
	for i := range MaxTemplates + 1 {
		fmt.Fprintf(&many, "  t%d: {agent: claude, model: claude-opus-5-5, effort: high, size: S, profile: dev, repos: [r], modes: [interactive], admits: [approved]}\n", i)
	}
	for name, c := range map[string]struct {
		data map[string]string
		want string
	}{
		// The document.
		"unknown data key":       {map[string]string{"template.yaml": validTemplates}, `data key "template.yaml"`},
		"extra data key":         {map[string]string{TemplatesKey: validTemplates, "notes": "x"}, `data key "notes"`},
		"empty document":         {data(""), "not a mapping"},
		"a list":                 {data("- a\n"), "not a mapping"},
		"not YAML":               {data("version: [1\n"), "templates.yaml: yaml: line 1: did not find expected ',' or ']'"},
		"no version":             {data("templates: {}\n"), "version 0"},
		"newer version":          {data(edit(t, "version: 1", "version: 2")), "version 2"},
		"version as a string":    {data(edit(t, "version: 1", `version: "1"`)), "cannot unmarshal string into Go struct field templatesDoc.version of type int"},
		"unknown top field":      {data(edit(t, "version: 1", "version: 1\ndefaults: {}")), `unknown field "defaults"`},
		"too large":              {data(validTemplates + "#" + strings.Repeat("x", MaxTemplatesBytes)), "more than"},
		"too many templates":     {data(many.String()), "65 templates"},
		"duplicate template":     {data(validTemplates + "  phone:\n    agent: claude\n"), `key "phone" already set in map`},
		"unknown template field": {data(edit(t, auditModel, auditModel+"    priority: urgent\n")), `unknown field "templates.ops-audit.priority"`},
		"misspelt field":         {data(edit(t, auditModel, "    modelId: claude-opus-5-5\n")), `unknown field "templates.ops-audit.modelId"`},
		"field in another case":  {data(edit(t, auditModel, "    Model: claude-opus-5-5\n")), `unknown field "templates.ops-audit.Model"`},
		"repeated field":         {data(edit(t, auditModel, auditModel+auditModel)), `"model"`},
		"null template":          {data(validTemplates + "  empty: null\n"), `template "empty": agent: required`},
		"name not a DNS label":   {data(edit(t, "  ops-audit:", "  Ops_Audit:")), `template "Ops_Audit": name`},
		"name too long":          {data(edit(t, "  ops-audit:", "  "+strings.Repeat("a", 64)+":")), "name: not a DNS label"},

		// Agent and model.
		"no agent":             {data(edit(t, "    agent: claude\n    model: claude-opus-5-5", "    model: claude-opus-5-5")), "agent: required"},
		"codex":                {data(edit(t, "    agent: claude\n    model: claude-opus-5-5", "    agent: codex\n    model: claude-opus-5-5")), `agent: "codex"`},
		"no model":             {data(edit(t, auditModel, "")), "model: required"},
		"alias opus":           {data(edit(t, auditModel, "    model: opus\n")), `model: "opus" is an alias`},
		"alias sonnet":         {data(edit(t, "model: claude-sonnet-5-5", "model: sonnet")), `model: "sonnet" is an alias`},
		"alias in upper case":  {data(edit(t, auditModel, "    model: Opus\n")), `model: "Opus" is an alias`},
		"latest alias":         {data(edit(t, auditModel, "    model: claude-opus-latest\n")), "moves with each release"},
		"not a full Claude id": {data(edit(t, auditModel, "    model: gpt-6-astra\n")), `"gpt-6-astra" is not a full Claude model id`},

		// Effort and size.
		"effort not in the enum":   {data(edit(t, "effort: xhigh", "effort: extreme")), `effort: "extreme" is not one of`},
		"ultracode":                {data(edit(t, "effort: xhigh", "effort: ultracode")), `effort: "ultracode" is not one of`},
		"no effort":                {data(edit(t, "    effort: xhigh\n", "")), "effort: required"},
		"effort the model lacks":   {data(edit(t, auditModel+"    effort: xhigh", "    model: claude-opus-4-6\n    effort: xhigh")), "model claude-opus-4-6 takes low, medium, high, max"},
		"effort on a no-effort id": {data(edit(t, "    model: claude-haiku-4-5\n", "    model: claude-haiku-4-5\n    effort: high\n")), "has no effort control"},
		"no size":                  {data(edit(t, "    size: M\n", "")), `size: "" is not one of S, M, L`},
		"size not in the enum":     {data(edit(t, "size: M", "size: XL")), `size: "XL"`},
		"size in lower case":       {data(edit(t, "size: M", "size: m")), `size: "m"`},

		// Profile and repos.
		"no profile":         {data(edit(t, "    profile: dev\n", "")), "profile: required"},
		"profile not a name": {data(edit(t, "profile: dev", "profile: Dev!")), `profile: "Dev!" is not a DNS label`},
		"no repos":           {data(edit(t, "    repos: [haynes-ops, dev-env]\n", "")), "repos: required"},
		"empty repos":        {data(edit(t, "repos: [haynes-ops, dev-env]", "repos: []")), "repos: required"},
		"repo a path":        {data(edit(t, "repos: [haynes-ops, dev-env]", "repos: [haynes-ops, thaynes43/dev-env]")), `repos[1]: "thaynes43/dev-env" is not a repository name`},
		"repo dot-dot":       {data(edit(t, "repos: [haynes-ops, dev-env]", "repos: [..]")), `repos[0]: ".." is not a repository name`},
		"repo dot":           {data(edit(t, "repos: [haynes-ops, dev-env]", "repos: [.]")), `repos[0]: "." is not a repository name`},
		"repo over 100":      {data(edit(t, "repos: [haynes-ops, dev-env]", "repos: ["+strings.Repeat("a", 101)+"]")), `repos[0]: "` + strings.Repeat("a", 101) + `" is not a repository name`},
		"repo empty":         {data(edit(t, "repos: [haynes-ops, dev-env]", `repos: [""]`)), `repos[0]: "" is not a repository name`},
		"repo repeated":      {data(edit(t, "repos: [haynes-ops, dev-env]", "repos: [dev-env, dev-env]")), `repos[1]: "dev-env" is repeated`},
		"too many repos":     {data(edit(t, "repos: [haynes-ops, dev-env]", "repos: "+manyRepos)), "33 repositories"},

		// Modes and durations.
		"no modes":                 {data(edit(t, "    modes: [headless]\n", "")), "modes: required"},
		"unknown mode":             {data(edit(t, "modes: [headless]", "modes: [batch]")), `modes[0]: "batch"`},
		"mode repeated":            {data(edit(t, "modes: [headless]", "modes: [headless, headless]")), `modes[1]: "headless" is repeated`},
		"headless, no maxDuration": {data(edit(t, "    maxDuration: 2h\n", "")), "maxDuration: required: headless work needs a maximum duration"},
		"maxDuration, interactive": {data(edit(t, "    modes: [interactive]\n", "    modes: [interactive]\n    maxDuration: 1h\n")), "maxDuration: set, but the template serves no headless work"},
		"maxDuration zero":         {data(edit(t, "maxDuration: 2h", "maxDuration: 0s")), `maxDuration: "0s" is not positive`},
		"maxDuration negative":     {data(edit(t, "maxDuration: 2h", "maxDuration: -1h")), `maxDuration: "-1h" is not positive`},
		"maxDuration too long":     {data(edit(t, "maxDuration: 2h", "maxDuration: 25h")), `maxDuration: "25h" is more than 24h0m0s`},
		"maxDuration not Go":       {data(edit(t, "maxDuration: 2h", "maxDuration: 2 hours")), `maxDuration: "2 hours" is not a Go duration`},
		"maxDuration a number":     {data(edit(t, "maxDuration: 2h", "maxDuration: 7200")), "cannot unmarshal number into Go struct field templateDoc.templates.maxDuration of type string"},
		"idle on headless only":    {data(edit(t, "    maxDuration: 2h\n", "    maxDuration: 2h\n    idleSuspendAfter: 1h\n")), "idleSuspendAfter: set, but the template serves no interactive work"},
		"idle too long":            {data(edit(t, "idleSuspendAfter: 4h", "idleSuspendAfter: 73h")), `idleSuspendAfter: "73h" is more than 72h0m0s`},
		"idle zero":                {data(edit(t, "idleSuspendAfter: 4h", "idleSuspendAfter: 0s")), `idleSuspendAfter: "0s" is not positive`},
		"idle not Go":              {data(edit(t, "idleSuspendAfter: 4h", "idleSuspendAfter: 4 hours")), `idleSuspendAfter: "4 hours" is not a Go duration`},

		// Admits.
		"no admits":       {data(edit(t, "    admits: [human, approved]\n", "")), "admits: required"},
		"admits a client": {data(edit(t, "admits: [human, approved]", "admits: [human, client]")), `admits[1]: "client" is not human, session or approved`},
		"admits empty":    {data(edit(t, "admits: [human, approved]", `admits: [""]`)), `admits[0]: "" is not`},
		"admits repeated": {data(edit(t, "admits: [human, approved]", "admits: [human, human]")), `admits[1]: "human" is repeated`},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseTemplates(c.data)
			if err == nil {
				t.Fatalf("ParseTemplates accepted it: %v", got)
			}
			if got != nil {
				t.Errorf("a refused document returned templates: %v", got)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}

// One fault in one template refuses the whole document, and every fault is
// reported, not only the first.
func TestParseTemplatesFailsClosed(t *testing.T) {
	doc := edit(t, auditModel, "    model: opus\n")
	doc = strings.Replace(doc, "size: S\n    profile: ops\n    repos: [haynes-ops]\n    modes: [interactive]\n",
		"size: S\n    profile: ops\n    repos: [haynes-ops]\n    modes: [interactive]\n    maxDuration: 1h\n", 1)
	got, err := ParseTemplates(data(doc))
	if err == nil || got != nil {
		t.Fatalf("ParseTemplates = %v, %v", got, err)
	}
	for _, want := range []string{`template "ops-audit": model`, `template "phone": maxDuration`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not report %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "ops-desk") {
		t.Errorf("error %q blames the valid template", err)
	}
}

func TestResolve(t *testing.T) {
	templates, err := ParseTemplates(data(validTemplates))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(templates, "ops-audit", "dev-env")
	if err != nil || !reflect.DeepEqual(got, wantValid()["ops-audit"]) {
		t.Fatalf("Resolve = %+v, %v", got, err)
	}
	got.Repos[0], got.Admits[0], got.Modes[0] = "other", BasisSession, "interactive"
	if again, _ := Resolve(templates, "ops-audit", "dev-env"); !reflect.DeepEqual(again, wantValid()["ops-audit"]) {
		t.Error("changing a resolved template changed the parsed one")
	}
	for name, c := range map[string]struct {
		name, repo string
		want       error
	}{
		"unknown name":       {"ops-auditor", "haynes-ops", ErrUnknownTemplate},
		"empty name":         {"", "haynes-ops", ErrUnknownTemplate},
		"repo not allowed":   {"ops-desk", "dev-env", ErrRepoNotAllowed},
		"repo in other case": {"ops-audit", "Dev-Env", ErrRepoNotAllowed},
		"empty repo":         {"ops-audit", "", ErrRepoNotAllowed},
	} {
		if _, err := Resolve(templates, c.name, c.repo); !errors.Is(err, c.want) {
			t.Errorf("%s: Resolve = %v, want %v", name, err, c.want)
		}
	}
	if _, err := Resolve(nil, "ops-audit", "haynes-ops"); !errors.Is(err, ErrUnknownTemplate) {
		t.Errorf("no templates: Resolve = %v", err)
	}
}

func TestTemplateModesAndBases(t *testing.T) {
	ts := wantValid()
	audit, desk, phone := ts["ops-audit"], ts["ops-desk"], ts["phone"]
	for name, c := range map[string]struct{ got, want bool }{
		"audit headless":     {audit.AllowsMode(ModeHeadless), true},
		"audit interactive":  {audit.AllowsMode(ModeInteractive), false},
		"desk both":          {desk.AllowsMode(ModeHeadless) && desk.AllowsMode(ModeInteractive), true},
		"phone headless":     {phone.AllowsMode(ModeHeadless), false},
		"unknown mode":       {desk.AllowsMode("batch"), false},
		"audit human":        {audit.AdmitsBasis(BasisHuman), true},
		"audit session":      {audit.AdmitsBasis(BasisSession), false},
		"phone approved":     {phone.AdmitsBasis(BasisApproved), true},
		"phone human":        {phone.AdmitsBasis(BasisHuman), false},
		"no basis, admitted": {Template{Admits: []Basis{BasisNone}}.AdmitsBasis(BasisNone), false},
	} {
		if c.got != c.want {
			t.Errorf("%s: %v, want %v", name, c.got, c.want)
		}
	}
}

// The canonical bytes and the digest are pinned: a change voids every
// approval that records a template digest, so it must be a deliberate edit
// of this test.
func TestTemplateDigestGolden(t *testing.T) {
	desk := wantValid()["ops-desk"]
	const wantInput = `{"agent":"claude","model":"claude-sonnet-5-5","effort":"high","size":"S","profile":"ops",` +
		`"repos":["haynes-ops"],"modes":["headless","interactive"],"maxDuration":2400000000000,` +
		`"idleSuspendAfter":14400000000000,"admits":["approved","human","session"]}`
	if got := string(templateDigestInput(desk)); got != wantInput {
		t.Errorf("canonical input changed:\n got %s\nwant %s", got, wantInput)
	}
	const want = "cf33877992136d02b7c16eb6b5b723f748ec449614be8d69c52e15edfcb9f336"
	if got := TemplateDigest(desk); got != want {
		t.Errorf("TemplateDigest = %s, want %s", got, want)
	}
	if desk.Modes[0] != "interactive" || desk.Admits[0] != BasisSession {
		t.Error("TemplateDigest sorted its argument's sets")
	}
}

// templateDigestFields and templateSetFields name every Template field by its
// JSON name. A new field fails here until it is listed; every listed field
// changes the digest when it alone changes.
var (
	templateDigestFields = []string{"agent", "model", "effort", "size", "profile", "repos", "modes", "maxDuration", "idleSuspendAfter", "admits"}
	templateSetFields    = []string{"repos", "modes", "admits"}
)

func TestTemplateDigestCoversEveryField(t *testing.T) {
	typ := reflect.TypeFor[Template]()
	var names, lists []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			t.Fatalf("Template field %s has no JSON name, so TemplateDigest does not hash it", f.Name)
		}
		names = append(names, name)
		if f.Type.Kind() == reflect.Slice {
			lists = append(lists, name)
		}
	}
	if !slices.Equal(names, templateDigestFields) {
		t.Fatalf("Template's fields are %v; templateDigestFields lists %v", names, templateDigestFields)
	}
	if !slices.Equal(lists, templateSetFields) {
		t.Fatalf("Template's list fields are %v; templateSetFields lists %v: declare each a set or give it an ordered rule", lists, templateSetFields)
	}
	base := wantValid()["ops-desk"]
	h := TemplateDigest(base)
	for i, name := range names {
		c := base.clone()
		v := reflect.ValueOf(&c).Elem().Field(i)
		switch {
		case v.Kind() == reflect.String:
			v.SetString(v.String() + "x")
		case v.Kind() == reflect.Int64:
			v.SetInt(v.Int() + int64(time.Second))
		case v.Kind() == reflect.Slice:
			v.Set(reflect.Append(v, reflect.ValueOf("zz-added").Convert(v.Type().Elem())))
		default:
			t.Fatalf("%s: no mutation for %s", name, v.Type())
		}
		if TemplateDigest(c) == h {
			t.Errorf("an edit of %s kept the digest", name)
		}
	}
	for i, name := range names {
		if !slices.Contains(templateSetFields, name) {
			continue
		}
		c := base.clone()
		v := reflect.ValueOf(&c).Elem().Field(i)
		if v.Len() < 2 {
			v.Set(reflect.Append(v, reflect.ValueOf("aa-first").Convert(v.Type().Elem())))
		}
		ordered := TemplateDigest(c)
		for a, b := 0, v.Len()-1; a < b; a, b = a+1, b-1 {
			x, y := v.Index(a).Interface(), v.Index(b).Interface()
			v.Index(a).Set(reflect.ValueOf(y))
			v.Index(b).Set(reflect.ValueOf(x))
		}
		if TemplateDigest(c) != ordered {
			t.Errorf("reversing %s changed the digest", name)
		}
	}
}
