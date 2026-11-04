package authority

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	kjson "sigs.k8s.io/json"
	"sigs.k8s.io/yaml"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Where the dispatch templates live (DESIGN-054 "Dispatch templates"): their
// own ConfigMap, not dev-env-templates, so a template edit drains no session.
const (
	TemplatesNamespace = "dev-env-system"
	TemplatesConfigMap = "dev-env-scheduled-work"
	TemplatesKey       = "templates.yaml"
)

// TemplatesVersion is the one document version this package reads. A newer
// document is refused, so an older operator dispatches nothing rather than
// misread it.
const TemplatesVersion = 1

// Bounds of the templates document.
const (
	MaxTemplatesBytes      = 64 << 10
	MaxTemplates           = 64
	MaxTemplateRepos       = 32
	MaxTemplateDuration    = 24 * time.Hour
	MaxTemplateIdleSuspend = 72 * time.Hour
)

// The modes a template serves: ScheduledWork's spec.mode values.
const (
	ModeHeadless    = v1alpha1.ScheduledWorkHeadless
	ModeInteractive = v1alpha1.ScheduledWorkInteractive
)

// The efforts and sizes a template may fix.
var (
	templateEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	templateSizes   = []string{string(v1alpha1.SizeS), string(v1alpha1.SizeM), string(v1alpha1.SizeL)}
	templateBases   = []Basis{BasisHuman, BasisSession, BasisApproved}
	templateModes   = []string{ModeHeadless, ModeInteractive}
)

// repoName is ScheduledWorkOrder.Repo's schema pattern.
var repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Template is one dispatch template: what a session of it runs, fixed by the
// template and never raised by the request. Its fields are the executor's
// Template's, plus Modes.
type Template struct {
	Agent, Model, Effort, Size, Profile string
	// Repos are the repositories the template allows.
	Repos []string
	// Modes are the ScheduledWork modes the template serves.
	Modes []string
	// MaxDuration bounds a headless task: set exactly when Modes has
	// headless.
	MaxDuration time.Duration
	// IdleSuspendAfter suspends an interactive session once idle this long;
	// zero takes the executor's default. Set only when Modes has
	// interactive.
	IdleSuspendAfter time.Duration
	// Admits are the bases the template admits.
	Admits []Basis
}

// AllowsMode reports whether the template serves ScheduledWork mode mode.
func (t Template) AllowsMode(mode string) bool { return slices.Contains(t.Modes, mode) }

// AdmitsBasis reports whether the template admits basis b. BasisNone is
// never admitted.
func (t Template) AdmitsBasis(b Basis) bool { return b != BasisNone && slices.Contains(t.Admits, b) }

func (t Template) clone() Template {
	t.Repos, t.Modes, t.Admits = slices.Clone(t.Repos), slices.Clone(t.Modes), slices.Clone(t.Admits)
	return t
}

// templatesDoc is the templates.yaml document.
type templatesDoc struct {
	Version   int                    `json:"version"`
	Templates map[string]templateDoc `json:"templates"`
}

type templateDoc struct {
	Agent            string   `json:"agent"`
	Model            string   `json:"model"`
	Effort           string   `json:"effort"`
	Size             string   `json:"size"`
	Profile          string   `json:"profile"`
	Repos            []string `json:"repos"`
	Modes            []string `json:"modes"`
	MaxDuration      string   `json:"maxDuration"`
	IdleSuspendAfter string   `json:"idleSuspendAfter"`
	Admits           []string `json:"admits"`
}

// ParseTemplates reads the dispatch templates from the dev-env-scheduled-work
// ConfigMap's data (the package documentation gives the document). Nil or
// empty data, an absent ConfigMap, is no templates: an empty map and no
// error, so nothing dispatches. Any other fault refuses the whole document,
// one invalid template included: the result is nil and the error names every
// fault found.
func ParseTemplates(data map[string]string) (map[string]Template, error) {
	for k := range data {
		if k != TemplatesKey {
			return nil, fmt.Errorf("ConfigMap %s/%s has data key %q; its one key is %s", TemplatesNamespace, TemplatesConfigMap, k, TemplatesKey)
		}
	}
	raw, ok := data[TemplatesKey]
	if !ok {
		return map[string]Template{}, nil
	}
	if len(raw) > MaxTemplatesBytes {
		return nil, fmt.Errorf("%s: %d bytes, more than %d", TemplatesKey, len(raw), MaxTemplatesBytes)
	}
	// YAML to JSON, then the API server's own strict decoder: field names are
	// case-sensitive, and an unknown or repeated field is an error.
	j, err := yaml.YAMLToJSONStrict([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", TemplatesKey, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(j)), "{") {
		return nil, fmt.Errorf("%s: the document is not a mapping", TemplatesKey)
	}
	var doc templatesDoc
	strict, err := kjson.UnmarshalStrict(j, &doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", TemplatesKey, err)
	}
	if len(strict) > 0 {
		return nil, fmt.Errorf("%s: %w", TemplatesKey, errors.Join(strict...))
	}
	if doc.Version != TemplatesVersion {
		return nil, fmt.Errorf("%s: version %d; this operator reads version %d", TemplatesKey, doc.Version, TemplatesVersion)
	}
	if len(doc.Templates) > MaxTemplates {
		return nil, fmt.Errorf("%s: %d templates, more than %d", TemplatesKey, len(doc.Templates), MaxTemplates)
	}
	out := make(map[string]Template, len(doc.Templates))
	var faults []error
	for _, name := range slices.Sorted(maps.Keys(doc.Templates)) {
		t, errs := parseTemplate(name, doc.Templates[name])
		if len(errs) > 0 {
			faults = append(faults, errs...)
			continue
		}
		out[name] = t
	}
	if len(faults) > 0 {
		return nil, fmt.Errorf("%s: %w", TemplatesKey, errors.Join(faults...))
	}
	return out, nil
}

// parseTemplate validates one template and converts it.
func parseTemplate(name string, d templateDoc) (Template, []error) {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, fmt.Errorf("template %q: %s: %s", name, field, fmt.Sprintf(format, args...)))
	}
	if msgs := validation.IsDNS1123Label(name); len(msgs) > 0 {
		add("name", "not a DNS label: %s", strings.Join(msgs, "; "))
	}

	switch d.Agent {
	case string(v1alpha1.AgentClaude):
	case "":
		add("agent", "required: claude")
	default:
		add("agent", "%q: scheduled work runs claude only", d.Agent)
	}

	modelOK := false
	switch apiv1.CheckClaudeModel(d.Model) {
	case apiv1.ClaudeModelOK:
		modelOK = true
	case apiv1.ClaudeModelAlias:
		add("model", "%q is an alias, which the CLI resolves to whatever its own version knows; give the full model id, such as claude-opus-5-5", d.Model)
	case apiv1.ClaudeModelLatest:
		add("model", "%q is an alias that moves with each release; give the full model id", d.Model)
	default:
		if d.Model == "" {
			add("model", "required: a full model id such as claude-opus-5-5")
		} else {
			add("model", "%q is not a full Claude model id", d.Model)
		}
	}

	switch {
	case d.Effort != "" && !slices.Contains(templateEfforts, d.Effort):
		add("effort", "%q is not one of %s", d.Effort, strings.Join(templateEfforts, ", "))
	case !modelOK:
		// The model's fault is reported; its levels are unknown.
	case apiv1.ClaudeEffortLevels(d.Model) == nil:
		if d.Effort != "" {
			add("effort", "model %s has no effort control; leave effort empty", d.Model)
		}
	case d.Effort == "":
		add("effort", "required: one of %s that model %s takes", strings.Join(apiv1.ClaudeEffortLevels(d.Model), ", "), d.Model)
	case !apiv1.ClaudeEffortAccepted(d.Model, d.Effort):
		add("effort", "model %s takes %s, not %q", d.Model, strings.Join(apiv1.ClaudeEffortLevels(d.Model), ", "), d.Effort)
	}

	if !slices.Contains(templateSizes, d.Size) {
		add("size", "%q is not one of %s", d.Size, strings.Join(templateSizes, ", "))
	}

	if d.Profile == "" {
		add("profile", "required")
	} else if msgs := validation.IsDNS1123Label(d.Profile); len(msgs) > 0 {
		add("profile", "%q is not a DNS label: %s", d.Profile, strings.Join(msgs, "; "))
	}

	switch {
	case len(d.Repos) == 0:
		add("repos", "required: at least one repository")
	case len(d.Repos) > MaxTemplateRepos:
		add("repos", "%d repositories, more than %d", len(d.Repos), MaxTemplateRepos)
	}
	for i, r := range d.Repos {
		switch {
		case len(r) == 0 || len(r) > 100 || !repoName.MatchString(r) || r == "." || r == "..":
			add(fmt.Sprintf("repos[%d]", i), "%q is not a repository name", r)
		case slices.Index(d.Repos, r) != i:
			add(fmt.Sprintf("repos[%d]", i), "%q is repeated", r)
		}
	}

	if len(d.Modes) == 0 {
		add("modes", "required: headless, interactive or both")
	}
	for i, m := range d.Modes {
		switch {
		case !slices.Contains(templateModes, m):
			add(fmt.Sprintf("modes[%d]", i), "%q is not headless or interactive", m)
		case slices.Index(d.Modes, m) != i:
			add(fmt.Sprintf("modes[%d]", i), "%q is repeated", m)
		}
	}
	headless, interactive := slices.Contains(d.Modes, ModeHeadless), slices.Contains(d.Modes, ModeInteractive)

	var maxDuration, idle time.Duration
	switch {
	case !headless && d.MaxDuration != "":
		add("maxDuration", "set, but the template serves no headless work")
	case headless && d.MaxDuration == "":
		add("maxDuration", "required: headless work needs a maximum duration")
	case headless:
		maxDuration = duration(d.MaxDuration, MaxTemplateDuration, "maxDuration", add)
	}
	switch {
	case !interactive && d.IdleSuspendAfter != "":
		add("idleSuspendAfter", "set, but the template serves no interactive work")
	case d.IdleSuspendAfter != "":
		idle = duration(d.IdleSuspendAfter, MaxTemplateIdleSuspend, "idleSuspendAfter", add)
	}

	if len(d.Admits) == 0 {
		add("admits", "required: a subset of human, session, approved")
	}
	admits := make([]Basis, 0, len(d.Admits))
	for i, a := range d.Admits {
		b := Basis(a)
		switch {
		case !slices.Contains(templateBases, b):
			add(fmt.Sprintf("admits[%d]", i), "%q is not human, session or approved", a)
		case slices.Contains(admits, b):
			add(fmt.Sprintf("admits[%d]", i), "%q is repeated", a)
		default:
			admits = append(admits, b)
		}
	}

	if len(errs) > 0 {
		return Template{}, errs
	}
	return Template{
		Agent: d.Agent, Model: d.Model, Effort: d.Effort, Size: d.Size, Profile: d.Profile,
		Repos: slices.Clone(d.Repos), Modes: slices.Clone(d.Modes),
		MaxDuration: maxDuration, IdleSuspendAfter: idle, Admits: admits,
	}, nil
}

// duration parses a positive Go duration of at most limit.
func duration(s string, limit time.Duration, field string, add func(field, format string, args ...any)) time.Duration {
	d, err := time.ParseDuration(s)
	switch {
	case err != nil:
		add(field, "%q is not a Go duration such as 40m", s)
	case d <= 0:
		add(field, "%q is not positive", s)
	case d > limit:
		add(field, "%q is more than %s", s, limit)
	default:
		return d
	}
	return 0
}

// ErrUnknownTemplate and ErrRepoNotAllowed are Resolve's refusals. Both are
// definite: the executor parks the occurrence on either.
var (
	ErrUnknownTemplate = errors.New("no such dispatch template")
	ErrRepoNotAllowed  = errors.New("the dispatch template does not allow the repository")
)

// Resolve returns the template name from templates, ParseTemplates' result,
// for work in repo: an unknown name is ErrUnknownTemplate, a repo the
// template does not allow is ErrRepoNotAllowed. The template returned is a
// copy.
func Resolve(templates map[string]Template, name, repo string) (Template, error) {
	t, ok := templates[name]
	if !ok {
		return Template{}, fmt.Errorf("%w: %q", ErrUnknownTemplate, name)
	}
	if !slices.Contains(t.Repos, repo) {
		return Template{}, fmt.Errorf("%w: template %s, repo %q", ErrRepoNotAllowed, name, repo)
	}
	return t.clone(), nil
}
