// Package authority holds DESIGN-054's due-time authority rules and dispatch
// templates (step S3b2) as pure functions over the API types. It reads only
// what it is given and does no I/O: the caller reads the ScheduledWork, the
// owner session and the dev-env-system/dev-env-scheduled-work ConfigMap
// uncached, then asks this package.
//
//   - SpecHash is the hash a human approval binds to: every spec field except
//     editor and cancelled.
//   - Decide is the authority basis of an item at one time: a human owner who
//     wrote the current spec, a human approval of the current spec's hash, or
//     a live session owner who wrote it. An owner session whose liveness is not
//     proven gives no basis and asks the caller to wait.
//   - ParseTemplates and Resolve read the dispatch templates, fail closed.
//
// The package does not import the executor, so the executor's Authorizer and
// TemplateResolver can adapt it without an import cycle.
//
// # The templates document
//
// The ConfigMap's one data key is templates.yaml. Its value is YAML (JSON is
// YAML too), decoded strictly: field names are case-sensitive, and an unknown
// or repeated field is an error. Durations are Go durations such as 40m.
//
//	version: 1
//	templates:
//	  ops-audit:                    # a DNS label, as spec.work.template
//	    agent: claude               # required; claude only (see below)
//	    model: claude-opus-5-5      # required; a full model id, never an alias
//	    effort: xhigh               # low|medium|high|xhigh|max, one the model takes;
//	                                # required, except empty for a model with no effort control
//	    size: M                     # required; S|M|L
//	    profile: ops                # required; a DNS label, checked against dev-env-templates at create
//	    repos: [haynes-ops]         # required; 1 to 32 repository names
//	    modes: [headless]           # required; headless and/or interactive
//	    maxDuration: 2h             # required when modes has headless, refused otherwise; at most 24h
//	    idleSuspendAfter: 4h        # optional, only when modes has interactive; at most 72h
//	    admits: [human, approved]   # required; a subset of human, session, approved
//
// The mode a template serves is explicit (modes): the template, not the item,
// says whether it runs headless tasks, interactive sessions or both. So a
// template written for a bounded headless audit cannot be used to open an
// interactive session, and maxDuration is required exactly when the template
// admits headless work.
//
// Only agent claude is accepted. A Codex remote session keeps no first launch
// record, so the controller refuses it the scheduled-work start deadline, and
// a Codex task needs an accepted project snapshot, which a scheduled session
// never carries. A template naming codex would park every occurrence at the
// create; it is refused here instead.
//
// One invalid template refuses the whole document, so a typo never leaves a
// partial set in force. An absent ConfigMap, or one with no data, is no
// templates: nothing dispatches. A data key other than templates.yaml is an
// error, so a misspelt key does not read as no templates.
package authority
