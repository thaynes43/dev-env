# DESIGN-069: Bounded capacity for ordinary Codex hosts (OP922)

Status: proposed source contract, 2026-11-18. Nothing installed or accepted.
Owner: story 922's coordinator; this branch is its worker's delivery.
Companion decision: ADR-031. This supplements DESIGN-063 and preserves
DESIGN-067 section 6.2's ordinary/workbench isolation.

## Investigation at pinned main adce1019344a62ebdb65ade85927410e241776c2

1. `internal/agentrun/create.go:createSession` calls
   `codexBeforeCreate` for a non-restore Codex interactive request.
   `internal/agentrun/codexhost.go` sends a read-only
   `POST /v1/codex-hosts/select?check=true` with repository, profile and launch ID.
   It sends neither requested size nor model. `selectCodexHost` resolves the
   account/profile, collects host facts and calls `codexhostpool.Select`.
   `Select` picks any suitable running host before resume/create. An older pod
   revision sets `outdated=true` but is usable with reported agentd >=2.102.0.
   The CLI prints that `use` object and never calls session create. This is the
   code path explaining the size L request answered with the existing size S
   host. It does not prove a new create was submitted to the API.
2. `at_capacity` counts Running, undeleted, unarchived, unprotected ordinary
   Codex remote hosts, suitable or not and pool-labelled or not. Failed rescue,
   hands-off, reserved, workbench and unresolved accounts are excluded. Defaults
   are one per resolved profile and two fleet-wide. Busy leases count hosts;
   neither memory, thread count nor lease count is a capacity input. A running
   unsuitable host filling the profile is normally `report` with its reason.
3. There is no CLI fresh-host flag or select API fresh field. The supported
   `POST /v1/sessions` Codex remote create accepts human, trusted client and
   workbench-session callers (the latter only on the default profile). It
   validates in `newSession`, admits via `admitCreateDirect`, and writes via
   `createWithCallerCap`; it does **not** call the pool selector or enforce its
   caps. It can therefore create a second same-profile host directly. A named
   resume can also bring an eligible suspended host back; neither route is an
   automatic capacity-aware fallback. Ordinary sessions/coordinators cannot
   create Codex remote hosts. CLI rescue restore skips the selection preflight,
   but restore validation and ordinary remote creation authority still apply.
4. `protocol.Status`/`AgentStatus` report agent state, attached tmux clients,
   heartbeat, reporting pod UID, version and repository bootstrap; remote status
   reports pairing/daemon state. There is no native live-thread count, memory
   usage or memory headroom. Attached tmux clients are not phone threads. The
   single annotation lease serializes launches for at most 15 minutes and is
   not a workload measurement. Session size is requested/default size; the owned
   pod's agent-container resource limit is the actual memory budget (including
   any approved raise). Fleet resources describe requests/limits, not usage.
5. OP695 supplies automatic folder trust. OP696 records Tom's 2026-11-10 reuse
   ruling: keep a few reusable hosts so he does not pair constantly. OP709
   permits a usable older image and exposes `outdated`, without restarting it.
   This work does not change those rules or touch any running host.

## Proposed rule and boundaries

Agentd on an ordinary Codex remote host passively reads its own cgroup v2
`memory.current` and `memory.max`. It reports only byte counts, with sample time;
no process list, native RPC, path or transcript is sent. Missing/unreadable,
unlimited, malformed, stale or old-pod reports are unknown and preserve reuse.
A host is full when a fresh current-pod sample leaves less than 1 GiB OR less
than 20% of its finite container memory limit. This reserves one modest turn's
headroom; it is a conservative admission estimate, not an OOM guarantee. Memory
can grow after the sample and native phone starts bypass operator selection.
Use another suitable host first; preserve leases and all safety exclusions.
Only after no host has room may capacity yield `create` with `host_full`.

An explicit `freshHost` request is also needed: the existing old host cannot
report the new observation and must not be restarted to add it. Only the
existing human or trusted-client caller classes may ask; no session, including
a workbench session, and no coordinator may do so. It does not grant creation
rights. CLI spelling is `--fresh-host`, only for new Codex interactive hosts.
Record the reason and authenticated requester as operator-owned annotations on
the new session. Do not expose new fields in legacy Session/list/fleet views.

Reuse stays the default. Normal profile cap remains one; `host_full` or an
explicit fresh request may use a second slot, with a fixed upper bound of two
ordinary retained hosts per profile and the existing fleet running cap. Pending
creates count. A shared ConfigMap reservation fences concurrent API replicas;
uncertain creates retain their slot rather than expire into duplicate launches.
The create API rechecks selection and bounds, so bypassing CLI cannot exceed
them. No host is stopped, resumed, resized, reimaged or paired by this work.

## Owner question

Q-069-01: Is the proposed 1 GiB / 20% headroom reserve suitable for routine use?
Recommendation: start conservatively and measure after installation. It cannot
predict a large turn, a tool's allocation, or launches directly from the phone.
This source proposal grants no deployment or live-host authority. The coordinator
reviews the contract and Claude Opus reads the PR before merge.

## Evidence and delivery

Source, tests, installation and acceptance are separate. This PR will carry the
focused cases, all apiserver tests, lint/layout/generation and CI results, plus a
table of decisions and actual mutation failures. Installation requires a later
CRD/operator/agent image delivery. An owner obtaining and pairing a second host
is acceptance and is outside this story's worker scope.
