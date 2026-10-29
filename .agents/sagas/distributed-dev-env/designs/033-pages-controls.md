# DESIGN-033: Pages current-alert grouping and desired controls

Date: 2026-10-29. OpenProject Story #242, parent Epic #120, Coordinator #239.
The narrow decision is Coordinator journal 2361
(2026-10-29T14:05:30.409Z). This draft records that decision for source.
It does not change [DESIGN-031](031-pages-lookup.md).

Phase A is the grouping and desired-preference codec. Phase B is the
read-only Pages workspace. Coordinator journal 2409 released Phase B at
2026-10-29T15:15:05.382Z. This design records that contract. It does not
change [DESIGN-031](031-pages-lookup.md) or DESIGN-029.

## State

| Layer | State |
|---|---|
| Grouping and desired-preference codec | source in `internal/pages`, unit-tested |
| Production preference store | absent: `UnconfiguredStore` only |
| In-memory store | test double only, in `controls_test.go` |
| Applied mute, escalate, or auto-responder | not stored and not performed |
| Pages workspace UI | source only: list, detail, and disabled configuration. Not installed. |
| Installed or accepted | no |

A passing test is not a deployed Pages workspace and not a live control.

## Boundary

Phase A adds only:

- `internal/pages/aggregate.go`
- `internal/pages/aggregate_test.go`
- `internal/pages/controls.go`
- `internal/pages/controls_test.go`
- this design

Existing `internal/pages` files stay as they are. The keeper and `agent-run`
already consume them. `internal/pages` remains a standard-library package
with no I/O. No new Layout line is required.

The aggregate reads an `AlertmanagerSnapshot`. It does not parse Alertmanager
JSON itself and does not change `ParseAlertmanager` or its consumers.
`cleanAlertText`, `validAlertTime`, and `ErrInvalid` are reused.

`providerallowance` already rejects duplicate keys, case-folded aliases, and
nulls, but its key policy is the allowance schema. Importing it here would
add a dependency to this package. The preference codec uses the same token
rules in this package instead. `decodeAlertValue` stays the Alertmanager
parser's helper and is not retargeted at preferences.

`Principal` is an issuer and subject pair, the same identity fields as
`portal.Principal`. This package does not import `web/portal`:
`web/portal/session` already imports `internal/pages`, and pages stays on
the standard library. The pair is opaque. An address-shaped subject is not
read as mail, and no username is derived.

## Aggregate

`AggregateAlerts` groups one current `AlertmanagerSnapshot` by the exact
label keys `alertname` and `severity`. Both keys are case-sensitive. A label
`Alertname` or `Severity` is a different label and is not the group key.

Each group reports:

- the alert name, and whether the `alertname` label was present
- the severity, and whether the `severity` label was present
- the count of current alerts in the group
- the earliest `startsAt`
- the latest `updatedAt`

A missing label is not the same as a label whose value is the word
`Unknown`. The name and severity fields stay empty when the label is absent,
and `AlertNameKnown` or `SeverityKnown` is false. An observed empty label is
known and empty.

Count is the number of alerts in this snapshot. It is not a historical
occurrence count. `endsAt`, disposition, recurrence, and selection are not
group fields. Putting a recurrence sentence on an input alert does not
change the count.

Coverage is fixed:

`Current snapshot only. Each group is alertname and severity, with the count of current alerts, the earliest start and the latest update. Notification delivery, history, triage and recurrence are not included.`

Availability is exactly `Available` or `Unavailable`.

| Input | Result |
|---|---|
| `Available` and no alerts, including a nil alert slice | `Available`, empty group slice, no error. This is an observed empty snapshot. |
| `Unavailable` and no alerts | `Unavailable`, empty group slice, no error. Source is copied. A known `observedAt` is rendered in UTC; the exact sentinel `Unknown` stays `Unknown`. |
| Any other availability, `Unavailable` with alerts, a duplicate fingerprint, a fingerprint that appears twice with different bodies, a malformed time, a missing label map, a name that disagrees with `alertname`, a status other than `active`, `suppressed`, or `unprocessed`, more than 1000 alerts, or a source or `observedAt` the current snapshot parser would not have produced | `Unavailable`, empty group slice, `observedAt` and source `Unknown`, and `ErrInvalid`. No partial counts. |

A source the parser keeps is kept, including leading or trailing spaces.
An empty label key the parser keeps is kept. It is not the `alertname` or
`severity` group key. `AggregateAlerts` does not add a tighter source or
label rule than `ParseAlertmanager`.

An empty `startsAt` or `updatedAt` is unknown. `snapshotTimeFloor` is
`2000-01-01T00:00:00Z`. A parsed instant before that floor is unknown. That
includes year 0, the zero time `0001-01-01T00:00:00Z`, and the Unix epoch.
The instant `2000-01-01T00:00:00Z` is known, including when an offset
spelling lands on that same instant. The floor is the snapshot floor only.
The MuteUntil range stays separate, including its 2100 ceiling.

A UTC year after 9999 is Unknown in the same way. The offset is normalized
to UTC before the year is read. `9999-12-31T23:59:59-01:00` is
`10000-01-01T00:59:59Z`, so it is Unknown and is not rendered.
`9999-12-31T23:59:59Z` stays known. `9999-12-31T22:59:59-01:00` normalizes to
`9999-12-31T23:59:59Z` and stays known. A year after 9999 is not a parse
error.

The group bound is the UTC minimum or maximum only when every alert in that
group has a known instant for that field. One unknown instant makes the
bound `Unknown`. A minimum of the known subset is not reported, and a
pre-2000 instant or a UTC year after 9999 is not rendered as a successful
time. A non-empty value that is not RFC3339Nano fails the whole aggregate,
including the literal word `Unknown` in an alert time. The snapshot's own
`observedAt` sentinel `Unknown` is separate and remains valid. An
`observedAt` before `snapshotTimeFloor`, or one whose UTC year is after
9999, becomes `Unknown` and does not fail an otherwise acceptable snapshot.

Known instants are compared with `time.Time` and rendered with
`time.RFC3339Nano` in UTC. `2026-10-28T00:00:00Z` and
`2026-10-27T20:00:00-04:00` are the same instant. The aggregate does not
read the wall clock, so it does not invent a missing time.

Groups are ordered by known alert name first, then alert name ascending in
byte order, then known severity first, then severity ascending in byte
order. Input order does not change the result. The input snapshot is not
modified.

`ListPreference` is not an argument. A desired filter is not applied, because
applying it would drop alerts and change counts.

## Desired preferences

Saved preferences are what a principal asked for. They are not applied
policy. The only applied-state facts are the literals `Unknown` and
`Not connected`. Preferences have no applied-state field. The Phase B
workspace uses the sentence `Not connected: saving and live effect unavailable`
for disabled controls.

`Desired` is exactly `unset`, `on`, or `off`.

```text
AlertPreference{Fingerprint, Escalate Desired, Mute Desired, MuteUntil *time.Time}  at most 200
TypePreference{AlertName, AutoResponder Desired}                                    at most 100
ListPreference{Sort, Filter}
Preferences{Version, Alerts, Types, List}
```

Schema version is the integer 1 only.

`MuteUntil` may be omitted. JSON null is rejected. A present instant must
parse as RFC3339Nano and fall in `[2000-01-01T00:00:00Z, 2100-01-01T00:00:00Z)`.
The codec stores and renders it in UTC. An offset spelling is accepted and
canonicalized; it is not preserved. No combination of desired and
`MuteUntil` is rewritten: mute `off` with a time, and mute `on` without a
time, both round-trip. The package does not turn either into a silence.

Sort allowlist, exact match. `PreferenceSorts` returns these spellings in byte order:

| Sort | Desired list order, not applied here |
|---|---|
| `alertname` | alert name, then severity |
| `count` | count, then alert name, then severity |
| `earliestStart` | earliest start, then alert name |
| `latestUpdate` | latest update, then alert name |
| `severity` | severity, then alert name |

Filter allowlist, exact match. `PreferenceFilters` returns these spellings
in byte order. They name a desired status narrowing of the current snapshot,
or no narrowing. They are not applied in Phase A.

| Filter | Meaning when a later phase applies it |
|---|---|
| `active` | Alertmanager status `active` |
| `all` | no status narrowing |
| `suppressed` | Alertmanager status `suppressed` |
| `unprocessed` | Alertmanager status `unprocessed` |

`MarshalPreferences` and `ParsePreferences` are a strict schema with canonical output.

Canonical JSON field order is `version`, `alerts`, `types`, `list`. Alert
objects are `fingerprint`, `escalate`, `mute`, then `muteUntil` only when
set. Type objects are `alertName`, `autoResponder`. List objects are `sort`,
`filter`. Alerts are ordered by fingerprint, types by alert name, both in
case-sensitive byte order. Empty lists are `[]`. Encoding does not escape
`<`, `>`, or `&` as `\u003c` / `\u003e` / `\u0026`. There is no trailing
newline. `Marshal(Parse(x))` is stable. The encoded bytes are refused with
`ErrInvalid` when longer than 128 KiB. That size check counts the encoded
document itself. It does not depend on `"` or `\` expanding.

The decoder reads tokens. It does not unmarshal into structs, because
`encoding/json` folds case and keeps one of a duplicate key. It rejects:

- unknown keys
- a key that case-folds to a canonical field and is not that field's exact
  spelling, including Unicode folds such as long s
- duplicate keys, including a second key that escapes to the same name
- JSON null
- trailing values
- malformed JSON
- invalid UTF-8
- a version token other than `1` (`1.0`, `1e0`, a string, a boolean)
- more than 200 alerts, 100 types, or 128 KiB on input or on canonical output
- fingerprint or alert name empty, longer than 256 bytes, not printable
  ASCII, a `"` or `\`, or with leading or trailing space. Printable ASCII
  also refuses invisible characters such as bidi controls and zero-width
  spaces. An internal space remains valid. Case is preserved.
- a duplicate fingerprint or a duplicate alert name
- a desired value, sort, or filter outside its allowlist
- a `muteUntil` that is not an in-range RFC3339Nano instant

Alert names and fingerprints stay case-sensitive. `DiskFull` and `diskfull`
are different type identities. Keys are not lowercased to force a match.

## Store

```text
Load(ctx, principal) (Preferences, Revision, error)
Save(ctx, principal, Preferences, expected Revision) (Revision, error)
```

`Revision` is an opaque string. `Save` is compare-and-swap. A stale expected
revision returns `ErrConflict` and does not replace the stored value.

For a configured store, `Load` of a principal with no saved row returns the
zero `Preferences`, the zero `Revision`, and a nil error. Version 0 is not a
saved document. A saved document has version 1. An empty expected revision
creates a row only when that principal has none. The revision created by
that save is a new opaque token, not the zero `Revision`. The same empty
revision when a row exists is `ErrConflict`. A non-empty expected revision
when no row exists is also `ErrConflict` and does not create a row.

`UnconfiguredStore` is the production value in this phase. It is not the
absent-row result. `Load` returns the zero `Preferences`, the zero
`Revision`, and `ErrNotConfigured`. `Save` returns `ErrNotConfigured`. It
has no fields, performs no I/O, ignores context and principal, and does not
validate a document into a stored row. There is no filesystem, database, or
network adapter.

The in-memory fake used by tests is not production persistence. Two handles
can share one process-local map. `Load` on the second handle sees `Save` on
the first. It follows the absent-row and create-only-empty-revision rules
above. In this test fake, the created revision is `1`, and a matching save
increments that decimal spelling. That spelling belongs to the fake only.
Revisions stay opaque: another store may use any non-empty token, and
callers compare the whole token. Concurrent saves of the same expected
revision produce one success and one `ErrConflict`; the stored revision
advances once and the stored document is the winner only. `Load` returns a
copy. A cancelled context refuses the fake without writing. None of this
survives a process restart, and none of it is evidence of a durable store.

The fake's principal check and `maxPrincipalBytes` live in `controls_test.go`.
Production code does not carry those test helpers. The applied facts remain
the literals `Unknown` and `Not connected`.

## Tests

`aggregate_test.go` covers available, observed empty, and unavailable input;
counts across names and severities; exact UTC minima and maxima, including
equal instants written with `Z` and a numeric offset; `snapshotTimeFloor`,
with earlier instants unknown and `2000-01-01T00:00:00Z` retained; a real
offset that normalizes to UTC year 10000 (`9999-12-31T23:59:59-01:00`) as
Unknown while year 9999 stays known; missing fields as unknown bounds rather
than partial minima; malformed times, duplicate and conflicting fingerprints,
and over-limit input as `ErrInvalid` with no groups; input order;
case-sensitive labels; parser agreement for a whitespace-padded source and
an empty label key; and no invented history.

`controls_test.go` covers canonical round-trip and byte stability for
`unset`, `on`, and `off`; every sort and filter; count, byte, version, and
time bounds, including exactly 256-byte tokens; refusal of quotes,
backslashes, invisible characters, unknown keys, nulls, duplicates,
case-variants, trailing data, malformed JSON, and invalid UTF-8; the encoded
128 KiB guard on its own; the largest valid document's stable
`Marshal(Parse(x))`; case-sensitive identities; an absent principal's zero
value and create-only empty revision; principal separation; reload across
handles; stale and concurrent revisions; copy isolation; and
`UnconfiguredStore` refusing both operations with no effect on the test
double. The memory fake is not a durability claim.

## Phase B workspace

Released by Coordinator journal 2409 at 2026-10-29T15:15:05.382Z, from main
`fc57dd40`. Source and tests only. A passing test is not an installed Pages
workspace and not a live control.

`session.PagesWorkspace` calls `AggregateAlerts` on the current
`AlertmanagerSnapshot`, then builds explicitly disconnected controls.
`RenderPages` renders that view. No store is loaded with an empty principal.
The workspace does not claim a loaded empty document or a saved preference row. `Save` is not called. There is no POST, CSRF, store
wiring, or network write. Authentication, the fixed source URL, the strict
parser, escaping, CSP, and the whole-page response bound stay enforced. If details
exceed the bound, a second atomic render lists all current groups with a notice
and omits detail and configuration. If that list also exceeds the same bound,
rendering still fails closed without partial output.
No new route or query string is added. The live handler still rejects a query.

The page has one `h1`. The shared navigation lines in `pages.html` stay as
they were at `fc57dd40`. The body is the workspace template.

List: one row per aggregate group. Each row shows the alert name, the
severity, the count, the earliest start, the latest update, and the coverage
line "Current snapshot only". Known instants display as `28 Oct 2026, 11:00 UTC`
in the list and detail; machine-readable time attributes retain RFC3339.
Unknown times stay the word Unknown. Missing labels show the muted text
`Unknown (absent)` and known empty labels show `Empty (value)`, distinct from
literal `Unknown` and `Empty` label values.

Detail: the selected group's alerts. Selection is a same-document fragment.
When `Snapshot.Selection` equals a fingerprint, that group is the selected
detail. Otherwise the first group is selected. Each alert is one key/value
document: fingerprint, status, start, end, update, disposition, and
recurrence, then summary and description. Labels are a compact table, with
keys sorted. Generator and Runbook links render only when the snapshot still
has a safe http or https URL. Alert field times use the same UTC floor and
year ceiling as `snapshotTimeFloor`. The group bounds themselves come from
`AggregateAlerts`.

Each alert detail has its own disabled Escalate and Mute pair, identified by
alert name and a short fingerprint; the full fingerprint remains in the detail.
Configuration has one auto-responder per distinct current alert name accepted
by the preference codec. Absent, empty, overlong, Unicode, quoted, backslashed
or padded names get no configurable row. Desired is shown as Unset, On, or
Off. With the unconfigured store the shown desired value is Unset. Applied
is `Unknown / Not connected`. The controls are disabled. The notice is
`Not connected: saving and live effect unavailable`. Sort and filter are not
rendered, because a filter would suggest the group counts had been narrowed.
`ListPreference` is still not applied. Disabled controls are not in the tab
order. Group and configuration links are keyboard reachable and use the shared focus
ring. The plain notice paragraph is not a tab stop. Each select has an accessible
name. The current-group link lives in the displayed detail, so fragment changes
expose the matching `aria-current="location"` link without stale current claims
on the list. The single h1 remains above the list/detail on mobile. Configuration
links target the section in the document scroll area on mobile.

States stay separate. Disabled says "Pages not enabled." Unavailable says
"Alertmanager is unavailable." An observed empty snapshot says "Observed
zero current alerts." An aggregate refusal says "Current snapshot is
invalid." Unknown is not shown as 0.

The workspace reuses the shared charcoal and lilac tokens. `pagesStyle`
adds the list, detail, and configuration layout for 1280, 390, and 320
widths, with wrapping text and no horizontal scroll. There is no script and
no form.

Preview fixtures are synthetic parses of current Alertmanager JSON: grouped
available, empty, unavailable, disabled, group detail, and
configuration-unconfigured. They are not production screenshots.

Phase B tests cover rendered group count and UTC bounds, selected detail
identity, escaped long labels and an atomic response bound, the four states
above plus invalid, per-alert disabled controls that cannot submit or claim policy
applied, codec-compatible type rows, missing versus literal labels, a 1000-alert
list fallback, atomic failure when even the list exceeds the bound, shared time
validation, semantic markup and unchanged shared navigation bytes. Unit tests
check markup and style contracts; actual widths, keyboard behavior, scrolling
and absence of horizontal overflow require browser evidence.

The continuation authorized in Coordinator journal 2520 updates only two
expectations in `session/pages_test.go`. An oversized detail now expects a
200 list-only response with its notice and aggregate, without the detail
canary or controls. A valid snapshot whose escaped group list also exceeds
the bound still expects the existing 503 response with no snapshot content.
The request-boundary test checks the observation's RFC3339 `datetime`
attribute while its visible text uses the human UTC format.

## What this does not do

- It does not edit `render.go`, `views.html`, `views.css`, attention,
  product files, assembly, the engine, production wiring, authorization,
  migrations, DESIGN-029, or the saga README.
- It does not add a route, a query API, POST, CSRF, a database, a
  dispatcher, or a call to `ControlStore.Save`.
- It does not write to Alertmanager and does not create a silence, mute,
  escalation, or responder action.
- It does not change authentication, sessions, the operator, or haynes-ops.
- It does not add a module dependency or a Layout entry.
- It does not treat a rendered control as applied policy.

The write path that would apply a preference needs a later story with its
own custody of assembly and engine. Phase B does not open that path.
