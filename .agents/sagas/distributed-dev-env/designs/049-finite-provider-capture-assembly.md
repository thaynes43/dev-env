# DESIGN-049: Finite assembly of retained provider captures

Status: source preparation, 2026-11-02, OP192/187. No provider acquisition,
recurring ingestion, publisher configuration or credentials are activated.

The existing aggregate transport accepts a normalized read snapshot, but the
supported upstream inputs differ: a sanitized Cursor native display, a Claude
passive statusline window callback and a sanitized Codex current-source report.
An explicit finite local assembly command bridges these inputs. It neither
contacts a provider nor reads auth/history or starts a schedule/subprocess.

`agent-run usage capture -o json --input FILE|- --trust-file FILE` reads two
explicit bounded files (the input may be stdin). Input is the closed
`providerallowance.captures.v1` bundle: `schema` and `captures`, with at most six
entries and at most two captures of each of Cursor, Anthropic and OpenAI.
Each entry has exactly `provider`, `producerRef`, `producerIncarnation`,
`sequence`, `capturedAt`, `receivedAt` and `payload`. The header is caller-owned
provenance, not provider evidence. Capture/receipt times are original clocks;
future or reversed times are refused. Sequence is stable per producer and
incarnation; rereading bytes cannot manufacture an independent observation.

The separate trust file uses existing ParseAggregateTrust. Provider payloads
cannot select their account, workspace, generation, producer identity or source
registry. A matching configured context may supply those bindings; otherwise
binding remains unknown. Unknowns are retained locally and cannot become
trusted merely because normalization succeeds. The aggregate server independently
checks its own trust before accepting a newly introduced capture.

Payloads are closed sanitized inputs only:

- Cursor: `cursor.native_display.sanitized.v1`, bounded by its existing parser.
  Its original capture time must agree. Preserve fractional used percentage,
  separate additional-credit spend and exact date-only/yearless reset labels.
- Claude: only `rate_limits`, with nullable `five_hour` and `seven_day` windows
  containing `used_percentage` and `resets_at`. Only these documented subscription
  windows are projected; payload account or credentials are not accepted.
- Codex: the existing sanitized CurrentSourceReport, obtained through the
  explicit usage-source sanitizer. Its retrievedAt must equal capturedAt. It
  does not establish stable account identity or entitlement. Used percentage
  and its declared complement stay distinct; a missing secondary window stays
  missing.

All three lack an independently confirmed provider observation timestamp on
these paths. Keep providerObservedAt null and current measures unknown even
when caller-owned account binding is verified. New distinct captures may advance
validated last-reported history; a new local read time is not provider refresh.
No reset action or claim of renewed capacity follows from a passed reset.

The existing collection projection supplies validation, source status, ordering
conflict and explicit missing-provider gaps. The command emits one bounded
providerallowance.read.v1 document; publication remains a separate explicit
`usage --publish-file` action. It never follows a provider URL or publishes
implicitly. Input waits are capped at 30 seconds and observe cancellation, including an open stdin pipe. Invalid input produces a fixed error without payload/path echo.

Acceptance needs meaningful local fixtures for all three inputs, provenance
and clock preservation, fractional/reset precision, unknown binding/freshness,
replay/conflict/order and malformed/credential input. It also needs reviewed
source and required CI. Real supported captures, external same-account/shared
entitlement evidence, two independent observations, exact publisher/reader
configuration and API/native/UI acceptance remain open until actually proved.

The three-provider assembly fixture also qualifies the existing transport and
serving projection with synthetic independent caller-owned account/source rules.
Validated historical-only measures keep their original observation and binding
when projected for last-reported UI values. They cannot become current measures,
even when a submitted report supplies provider time. The store rejects such a
promotion using caller-owned metric semantics, and the read projection withholds
it while retaining valid history and known conflicts. No UI presentation code or
runtime policy changes are included.

Registry additions for these inputs are explicit caller configuration: Cursor
uses its existing sanitized-display version and validation references. Claude
uses `claude-statusline-sanitized-2026.11.02`, passive source kind, capability
`claude-statusline:documented-windows`, and the two refs
`claude-statusline:five_hour-used-percent` and
`claude-statusline:seven_day-used-percent`. Codex uses
`codex-usage-limits-sanitized-2026.11.02`, native RPC source kind, capability
`codex-native:get-usage-limits` and ref `codex-native:primary-used-percent` for
window `codex-primary`. Percent metrics require used direction, bounds 0–100
and historical-only semantics; declared complements additionally require bound
100. Source and metric allowlists, account rules and any shared entitlement
remain external trust, never copied from provider payloads.
