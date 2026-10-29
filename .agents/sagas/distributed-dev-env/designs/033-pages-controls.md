# DESIGN-033: Pages current-alert grouping and desired controls

Date: 2026-10-29. OpenProject Story #242, parent Epic #120, Coordinator #239.
The narrow decision is Coordinator journal 2361
(2026-10-29T14:05:30.409Z). This draft records that decision for source.
It does not change [DESIGN-031](031-pages-lookup.md).

Phase A is source and tests only. Phase B (workspace templates, session view
model, preview fixtures, and the later body swap) starts only after #241
merges and the Coordinator posts an explicit release comment on #242. This
design does not authorize that edit.

## State

| Layer | State |
|---|---|
| Grouping and desired-preference codec | source in `internal/pages`, unit-tested |
| Production preference store | absent: `UnconfiguredStore` only |
| In-memory store | test double only, in `controls_test.go` |
| Applied mute, escalate, or auto-responder | not stored and not performed |
| Pages workspace UI | not started |
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

An empty `startsAt` or `updatedAt` is unknown. A parsed instant before
`2000-01-01T00:00:00Z` is also unknown. That includes year 0, the zero time
`0001-01-01T00:00:00Z`, and the Unix epoch. The instant
`2000-01-01T00:00:00Z` is known, including when an offset spelling lands on
that same instant. The group bound is the UTC minimum or maximum only when
every alert in that group has a known instant for that field. One unknown
instant makes the bound `Unknown`. A minimum of the known subset is not
reported, and a pre-2000 instant is not rendered as a successful time. A
non-empty value that is not RFC3339Nano fails the whole aggregate, including
the literal word `Unknown` in an alert time. The snapshot's own `observedAt`
sentinel `Unknown` is separate and remains valid. An `observedAt` before the
same 2000 floor becomes `Unknown` and does not fail an otherwise acceptable
snapshot. The `MuteUntil` ceiling of 2100 is not applied to snapshot times.

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
`Not connected`. Preferences have no applied-state field. A later view, not
this phase, uses the sentence `Not connected: saving and live effect unavailable`
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
creates a row only when that principal has none, and the created revision is
`1`. The same empty revision when a row exists is `ErrConflict`. A non-empty
expected revision when no row exists is also `ErrConflict` and does not
create a row.

`UnconfiguredStore` is the production value in this phase. It is not the
absent-row result. `Load` returns the zero `Preferences`, the zero
`Revision`, and `ErrNotConfigured`. `Save` returns `ErrNotConfigured`. It
has no fields, performs no I/O, ignores context and principal, and does not
validate a document into a stored row. There is no filesystem, database, or
network adapter.

The in-memory fake used by tests is not production persistence. Two handles
can share one process-local map. `Load` on the second handle sees `Save` on
the first. It follows the absent-row and create-only-empty-revision rules
above. A matching save increments the revision. Concurrent saves of the same
expected revision produce one success and one `ErrConflict`; the stored
revision advances once and the stored document is the winner only. `Load`
returns a copy. A cancelled context refuses the fake without writing. None
of this survives a process restart, and none of it is evidence of a durable
store.

## Tests

`aggregate_test.go` covers available, observed empty, and unavailable input;
counts across names and severities; exact UTC minima and maxima, including
equal instants written with `Z` and a numeric offset; the 2000 floor, with
earlier instants unknown and `2000-01-01T00:00:00Z` retained; missing fields
as unknown bounds rather than partial minima; malformed times, duplicate and
conflicting fingerprints, and over-limit input as `ErrInvalid` with no
groups; input order; case-sensitive labels; parser agreement for a
whitespace-padded source and an empty label key; and no invented history.

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

## What this does not do

- It does not edit pages templates, `web/portal/pages.go`, session pages,
  preview, shared views, render, product files, DESIGN-029, or the saga README.
- It does not add POST or CSRF handlers, assembly or engine wiring, a
  database migration, a grant, a dispatcher, or a notification.
- It does not write to Alertmanager and does not create a silence, mute,
  escalation, or responder action.
- It does not change authentication, sessions, the operator, or haynes-ops.
- It does not add a module dependency or a Layout entry.
- It does not treat #241, PR 643, or the stopped Root 240 and 238 work as
  input.

The write path that would apply a preference needs a later story with its
own custody of assembly and engine. Phase A does not open that path.
