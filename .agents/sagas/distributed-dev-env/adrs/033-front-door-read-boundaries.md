# ADR-033 | Front-door resource reads keep explicit source authority

Date 2026-11-23. Status Proposed. Tracking OP528, with OP509 and OP529.
[DESIGN-071](../designs/071-front-door-discovery-and-handoffs.md)

Use MCP resources on the existing `/mcp` endpoint. Package the catalog and
versioned operating rules with the binary. Derive a bounded work-item handoff
from the existing opwork item and read-only activity contracts. Keep one
stable versioned format with owner, source, item scope, state, evidence,
next action and capture time. Unknown checkpoint state remains explicit.

The handoff transports references and context. Existing tracker and caller
authorizations remain required for every write, dispatch and lifecycle
action. Usage stays on its separately authorized read until the operator
admits a reviewed GET-only Dot scope. A scope or identity refusal stops the
operation. Missing or stale telemetry remains uncertainty.

The retirement classifier proposes candidates when preservation and reviewed
retirement are confirmed. A supported reap is a separate authorized lifecycle
action and needs observed completion. The dry run keeps every unconfirmed
record. No access, deployment or retention change is authorized here.
