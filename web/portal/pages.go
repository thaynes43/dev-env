package portal

import (
	"bytes"
	_ "embed"
	"errors"
	"html/template"
	"strings"

	"github.com/thaynes43/dev-env/internal/pages"
)

//go:embed pages.html
var pagesSource string

//go:embed pages_workspace.html
var pagesWorkspaceSource string

const pagesStyle = `
.pages-content{flex:1;min-height:0;overflow-x:clip;overflow-y:auto;padding:24px;overflow-wrap:anywhere;min-width:0}
.pages-content .panel{max-width:72rem;margin:0 auto 16px}
.pages-content .turn-text{margin:10px 0}
.pages-workspace{display:grid;grid-template-columns:minmax(0,22rem) minmax(0,1fr);grid-template-areas:"intro intro" "list stage" "config config";gap:16px;min-width:0}
.pages-intro{grid-area:intro;min-width:0}
.pages-list{grid-area:list;min-width:0}
.pages-workspace.is-list-only{display:block}
.pages-stage{grid-area:stage;min-width:0}
.pages-config{grid-area:config;min-width:0;padding:16px;border:1px solid var(--line);border-radius:8px;background:var(--surface)}
.pages-intro h1{font-size:22px;line-height:1.2;font-weight:650;letter-spacing:-.4px;overflow-wrap:anywhere}
.pages-content .pages-group,.pages-content .item-line,.pages-content .badge,.pages-content .btn,.pages-content .mono{white-space:normal;overflow-wrap:anywhere}
.pages-earliest,.pages-latest,.pages-count{font-variant-numeric:tabular-nums}
.pages-coverage{display:block;margin-top:6px;color:var(--muted);font-size:10.5px}
.pages-detail{display:none;min-width:0}
.pages-stage:not(:has(.pages-detail:target)) .pages-detail.is-default{display:block}
.pages-detail:target{display:block}
.pages-back{display:none}
.pages-content .pages-labels{width:100%;table-layout:fixed;border-collapse:collapse}
.pages-content .pages-labels caption{text-align:left;font-weight:650;margin:0 0 6px}
.pages-content .pages-labels th,.pages-content .pages-labels td{padding:6px 8px 6px 0;border-bottom:1px solid var(--line);white-space:normal;overflow-wrap:anywhere;word-break:break-word;text-align:left;vertical-align:top}
.pages-content a:focus-visible,.pages-content summary:focus-visible{outline:3px solid var(--focus);outline-offset:3px;border-radius:6px}
.pages-content fieldset{min-width:0;margin:0 0 16px;padding:12px;border:1px solid var(--line);border-radius:8px}
.pages-content fieldset:disabled{opacity:1;color:var(--ink)}
.pages-content select{max-width:100%;min-width:0;min-height:34px}
.pages-content select:disabled{opacity:.72;cursor:not-allowed}
.pages-notice{margin:8px 0 12px;font-weight:600}
.pages-unloaded,.pages-empty-value,.pages-content [data-known="false"]{color:var(--muted)}
.pages-content [data-known="false"]{font-style:italic}
.pages-content .pages-group{padding:13px 12px;border:1px solid var(--line);border-radius:7px;background:var(--surface);margin-bottom:8px}
.pages-content .pages-group:hover{background:var(--soft);border-color:var(--control-line)}
.pages-content .pages-group h3{margin:7px 0 3px;font-size:12px;font-weight:600;line-height:1.45;letter-spacing:-.1px}
.pages-content .pages-group .item-kicker{font:9.5px var(--mono);color:var(--muted)}
.pages-current-group{color:inherit;text-decoration:none}
.pages-detail:target,.pages-config:target{scroll-margin-top:16px}
@media (max-width:700px){
.pages-content{overflow:visible;padding:18px 16px}
.pages-workspace{grid-template-columns:minmax(0,1fr);grid-template-areas:"intro" "list" "stage" "config"}
.pages-stage{display:none}
.pages-workspace:has(.pages-detail:target) .pages-list{display:none}
.pages-workspace:has(.pages-config:target) .pages-list{display:none}
.pages-workspace:has(.pages-detail:target) .pages-stage{display:block}
.pages-back{display:flex;gap:16px;flex-wrap:wrap}
.pages-back a{display:inline-flex;min-height:44px;align-items:center}
.pages-content .pages-group{padding:15px 13px;border-radius:10px}
.pages-content .pages-group h3{font-size:14px}
.pages-content select{min-height:44px;font-size:16px}
}
@media (max-width:390px){
.pages-content{padding:16px 12px}
.pages-workspace{gap:12px}
}
@media (max-width:320px){
.pages-content{padding:12px 8px}
.pages-content .kv{grid-template-columns:minmax(0,1fr)}
.pages-intro h1{font-size:20px}
}
`

const (
	// PagesControlNotice is the disabled-control sentence. It is not a claim that a save ran.
	PagesControlNotice = "Not connected: saving and live effect unavailable"
	// PagesAppliedState is the only applied fact the workspace shows.
	PagesAppliedState = "Unknown / Not connected"
	// PagesDesiredUnset, PagesDesiredOn, and PagesDesiredOff are the displayed desired words.
	PagesDesiredUnset      = "Unset"
	PagesDesiredOn         = "On"
	PagesDesiredOff        = "Off"
	PagesStoreDisconnected = "Not connected"
)

var PagesPolicy = policy(viewsStyle + pagesStyle)
var pagesTemplate = template.Must(template.New("pages").Funcs(template.FuncMap{"pagesTime": pagesTime, "shortFingerprint": shortFingerprint}).Parse(strings.Replace(pagesSource, "/*PORTAL_STYLE*/", viewsStyle+pagesStyle, 1) + pagesWorkspaceSource))

// PagesWorkspace is the read-only Pages document: one current snapshot and
// disconnected controls. It is not applied policy and not a saved store.
type PagesWorkspace struct {
	Banner, Policy, CallerCoverage, Coverage string
	Condition                                string
	Availability, ObservedAt, Source         string
	ListOnly                                 bool
	Groups                                   []PagesGroup
	Controls                                 PagesControls
}

// PagesGroup is one aggregate row and the alerts that belong to it.
type PagesGroup struct {
	ID                          string
	Default                     bool
	AlertName                   string
	AlertNameKnown              bool
	AlertNameDisplay            string
	AlertNameEmpty              bool
	Severity                    string
	SeverityKnown               bool
	SeverityDisplay             string
	SeverityEmpty               bool
	Count                       int
	EarliestStart, LatestUpdate string
	Alerts                      []PagesAlert
}

// PagesAlert is one current alert in the selected group's detail.
type PagesAlert struct {
	Fingerprint                 string
	Name                        string
	NameKnown                   bool
	NameDisplay                 string
	NameEmpty                   bool
	Status                      string
	StartsAt, EndsAt, UpdatedAt string
	Summary, Description        string
	Disposition, Recurrence     string
	Labels                      []PagesPair
	GeneratorURL, RunbookURL    string
}

// PagesPair is one label key and value, already ordered by the view model.
type PagesPair struct {
	Key, Value string
}

// PagesControls is the disabled configuration section.
type PagesControls struct {
	Notice, Store, Applied string
	DocumentLoaded         bool
	Types                  []PagesTypeControl
}

// PagesTypeControl is one current alert name's auto-responder row.
type PagesTypeControl struct {
	AlertName      string
	AlertNameKnown bool
	Label          string
	Desired        string
	Applied        string
}

// DisconnectedPagesControls is the unconfigured control section.
// DocumentLoaded is false. Types may be the current snapshot's alert names.
func DisconnectedPagesControls(types []PagesTypeControl) PagesControls {
	if types == nil {
		types = []PagesTypeControl{}
	}
	return PagesControls{
		Notice:         PagesControlNotice,
		Store:          PagesStoreDisconnected,
		DocumentLoaded: false,
		Applied:        PagesAppliedState,
		Types:          types,
	}
}

type boundedPagesBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedPagesBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errPagesRenderLimit
	}
	return b.Buffer.Write(p)
}

var errPagesRenderLimit = errors.New("pages render exceeds response limit")

func pagesTime(value string) string {
	stamp, known := pages.SnapshotTime(value)
	if !known {
		return "Unknown"
	}
	return stamp.Format("02 Jan 2006, 15:04 UTC")
}

func shortFingerprint(value string) string {
	runes := []rune(value)
	if len(runes) <= 12 {
		return value
	}
	return string(runes[:8]) + "…" + string(runes[len(runes)-4:])
}

// RenderPages returns an escaped workspace, falling back to a list of current
// groups when details exceed the bound. If the list also exceeds it, no bytes
// are returned. Neither render changes the caller's view or the response limit.
func RenderPages(view PagesWorkspace, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, errPagesRenderLimit
	}
	view.Banner = pages.AlertmanagerBanner
	view.Policy = strings.TrimSuffix(PagesPolicy, "; frame-ancestors 'none'")
	b := &boundedPagesBuffer{limit: limit}
	if err := pagesTemplate.Execute(b, view); err != nil {
		if !errors.Is(err, errPagesRenderLimit) || len(view.Groups) == 0 || view.ListOnly {
			return nil, err
		}
		view.ListOnly = true
		b.Reset()
		if err := pagesTemplate.Execute(b, view); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}
