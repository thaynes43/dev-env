# Practical baseline review — 2026-10-18

Reviewed baseline `3507f1f16a861acb65c2418d0fc3019608894587`, its dated MVP handoff, current HANDOFF, relevant source/history and open PRs. Initial current main `8e6183afde28b8e28a1e19a2a35c8308d0a31481` changes only the recorded prompt SHA. [Story](../backlog/2026-10-18-practical-review.md).

Execution host: `dev-env-68b4b9f6cf-lt6j7`. The supplied `/home/dev/work/dev-env-codex` checkout is absent in this runtime. Corrections use `/home/dev/work/dev-env-practical-review-1018`, branch `agent/practical-review-1018`. Existing sessions and worktrees are preserved. Root's driving model/effort is not exposed by these tools; one independent read-only reviewer was explicitly dispatched as `gpt-6.1-sol`, `ultra`. No runtime provider agent or paid API was used.

## Findings and corrections

1. **Retention hold rejected at the shelf boundary (P2).** `Runnable.resetKept` and the final release recheck append a durable `hold` before clearing an existing grace. `shelf.CoordinationArchive.Mark` accepted only present/reset and also rejected the non-null grace returned by a valid hold. Fake archive tests bypassed this seam. Accept the existing hold event and its preserved UTC grace; keep present/reset's cleared-grace checks. The real shelf transport -> command dispatcher -> filesystem regression fails before the fix and passes after it. This demonstrates a blocked reset path, not an observed production deletion or loss.
2. **Archived inbox acknowledgements listed as corrupt (P2).** Their manifests and indices contain recipient UIDs, but the list validator accepted recipient UIDs only for messages. Recognize acknowledgements under the existing inbox index contract. A real temporary archive regression reproduces the corrupt result before the fix and a valid metadata projection afterward.
3. **Lowercase MCP egress protocols rejected downstream (P2).** `request_access` locally accepted `tcp`/`udp` but sent that spelling to an API whose CRD accepts TCP/UDP. Normalize accepted spellings before HTTP serialization, as the CLI already does. The stdin-MCP-to-HTTP regression fails only the lowercase cases before the fix; lowercase, uppercase and omitted protocols pass afterward.
4. **Handoff source/image ambiguity.** HANDOFF placed #370 under the v2.29.5 image list. Git history puts #370 after that tag; the fix is in the locally run source harness, not the 2.29.5 image. Clarified that distinction. The dated source-pass handoff already separates later source from installation.

## Claim versus evidence

| Claim | Evidence checked | Result and practical limit |
| --- | --- | --- |
| PRs 373–415 are source/offline proof | Git history, v2.29.5 tag, release #369, current GitOps pins | Confirmed; no task release or GitOps installation of that chain. Operator main publications are separate from installation. |
| Archive before delete for all four records | Source order, isolated API-server fixtures and real temporary archive | Targeted checks recorded below; no live archive activation. |
| Replaced owner, stale deletion and subsequent-pass ACK retries | UID/resource-version preconditions and corresponding envtests | Targeted checks recorded below; no live deletion attempted. |
| A returning owner invalidates an old grace durably | Runnable, real shelf transport and command round-trip | Baseline defect found and corrected; fake-backed leader-change test alone missed it. |
| Acknowledgements remain discoverable in the archive | Actual filesystem put/list | Baseline list defect found and corrected. |
| Missing/changed archives prevent release | No-volume/shelf, missing-revision, changed-byte and forbidden-delete fixtures | Targeted checks recorded below; protections remain enabled. |
| Grant CLI and MCP are functional source | CLI/MCP HTTP tests, CRD protocol enum, local grant-use adapter | Lowercase MCP defect corrected; live grant use/refusal/expiry/revocation still not accepted by this pass. |
| Idle drain is source-only | Controller tests, GitOps operator still `sha-f315635` | Source behavior checked; no live roll, session stop or replacement attempted. |
| Pilot command mistakes are rejected | Offline rendezvous tests and command validators | Source check only. Both-claim -> inbox ACK -> context publish -> post-terminal read remains unaccepted. |
| Private budget ownership is unwired | Operator main wires ManagedPodAdmissionInspector; PrivateManagedAdmissionInspector remains separate and refusing | Confirmed; wiring it is forward work. |
| v2 is not adoptable | General private admission false; shelf 2.9.1 lacks archives; plan 03 backlog; native/phone and consumer evidence absent | Correct for general adoption/v1 replacement. Existing narrow task/terminal capability and isolated source checks are useful; they do not establish full adoption. |

GitOps evidence was fetched from haynes-ops `origin/main` at `d06784a3`: operator `sha-f315635`, task agent 2.29.5, general private admission false, shelf 2.9.1 with no archive volume. These are current desired-state source checks, not a new observation of live convergence. No cluster request was needed for this review.

## Acceptance checks and results

All local Go commands run serially through `/tmp/dev-env-go-test.lock`, under `nice -n 19`, `GOMAXPROCS=2`, `-p 1`. The cached Go 1.27.2 toolchain was reused; isolated envtest assets are Kubernetes 1.35.0. No load/stress runs or image builds occurred in the pod.

- **Baseline regressions:** three new boundary tests fail against unchanged product source. MCP uppercase/omitted controls pass. Logs: `regressions-before.log`.
- **Focused corrected suites:** grant CLI/MCP, archive storage/commands/transport, retention retry/reset behavior and rendezvous command checks pass across `internal/agentrun`, `internal/agentd`, `cmd/agentd`, `internal/shelf`, `internal/coordretention` and `scripts/trials/pilot_rendezvous`. Log: `focused-after.log`.
- **Isolated API-server checks:** passed: `internal/agentd` (85.675s), `internal/coordretention` (23.550s), and selected idle-drain/controller checks (16.270s). Coverage includes all four archive-before-delete paths, replaced owners, stale deletes, ACK retry, missing/changed archives, no shelf/volume, forbidden delete, worst-case publication, identity-guard refusal and same-volume idle drain. These run local kube-apiserver/etcd, not the live cluster. Log: `envtest-drain-retention.log`.
- **Independent native review:** source review of the full correction diff returned no actionable findings; reviewer ran no tests or writes.
- **Required hosted CI and advisory review:** delivery result and exact final head are recorded in the correction PR conversation. Merge requires `CI - Success` on the current head and disposition of all available claude[bot] findings.

Supporting logs remain under `/home/dev/work/orders/dev-env-practical-review-1018/`. The first MCP regression preparation consumed an already-read mock request body; that test fixture was corrected before the three meaningful baseline failures recorded above. The acknowledgement expectation was also corrected to honor the existing metadata projection's intentional text omission. Neither preparation error changes the reproduced defects.

## Readiness and next safe steps

Finish this correction PR's required checks and squash merge. Normal main image publication may follow; no deployment or release #369 merge is included. Grok keeps forward implementation. Tom's fresh rendezvous cohort, H7 shelf mount, live grant-use proof, release #369, drafts #292/#231 and v1 retirement remain untouched. No new canary, grant, credential, pairing, cluster mutation or disruptive live test was performed.

The next acceptance evidence belongs to those existing gates: a fresh authorized cohort completes both claims, ACK, publication and post-terminal read; archive installation and required bounded resource checks precede enablement; private budget/recovery and native/phone workflows meet their existing contracts; live grants prove useful access and expiry/revocation. This review starts none of them.
