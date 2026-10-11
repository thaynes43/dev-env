# DESIGN-009: Private native Codex startup preflight

**Scope:** the next bounded #173 source slice after roots #181. **Decision:**
ADR-003. **Activation:** default off. Q-24 remains open; this unit changes no
pairing, enrollment, fleet UX, admission policy or existing host.

## Contract

`AGENTD_ENABLE_PRIVATE_CODEX_HOST=true` selects a private CodexHost startup
preflight. It requires trusted private catalog bootstrap and roots. Use the
existing named, uncached in-cluster catalog reader, never a caller file/hash as
private authority. Reject contradictory shared configuration and legacy shared
paths. Prove repository/worktree/project and provider-state directories belong
to the writable private HOME with no redirected filesystem/Git identity.
Keep `$HOME/.codex` enrollment/history separate from project directories.

Prepare roots and managed host configuration only after the existing lifecycle
has positively established daemon absence. Passive observation, a live/busy
host, Unknown or an unconfirmed startup intent must produce no catalog refresh,
configuration rewrite or replacement execution. Preserve the original causal
intent, exact host/profile/version ownership and all owned-stop/Unknown rules.
No repair, reset or rearm of the retained failed native fixture follows.
Existing keeper access-only installation and renewal keep their current
lifecycle rules; the prohibition above concerns catalog/root preparation and
managed host configuration. It grants no new credential installation path,
refresh owner or enrollment copy.

Consume current Ready private-root ownership/outcome and accepted catalog
identity, rule snapshot and source provenance. Refuse stale/changed authority,
foreign/modified roots, unconfirmed publication or redirected nested anchors.
Reuse the established preservation/receipt helpers rather than broadening
legacy `--catalog-file` trust. New configuration must be managed and confirmed;
preserve foreign or edited provider configuration. Keep updater/version guards
and private enrollment behavior unchanged. No credential or enrollment copy,
provider permission bypass or `skip-git-repo-check` is introduced.

Legacy mode remains available only under its existing explicit contract when
the private flag is off. A private host never falls back to shared preflight,
mounts or locks. This preflight does not change the current API/Pod renderer,
which still needs a separately reviewed private-host profile before runtime
admission can satisfy the private-volume contract.

## Paths and acceptance

- `cmd/agentd/codex_host.go` and the trusted project reader constructor.
- `internal/agentd/codex_host_config.go`, `codex_host.go`, settings and focused
  lifecycle/configuration tests; minimal reusable private-root proof helper.
- DESIGN-009, workflow guide and HANDOFF delivery boundaries.

Prove missing private startup prepares trusted roots without a shared lock or
preflight; current root/anchor trust is receipt-bound; stale catalog, modified
rules, foreign paths/provider state and ambiguous Git identity refuse before
start; disabled mode preserves existing behavior; and live/Unknown/retained
intent cases perform no refresh/configuration/start. Reuse native lifecycle
regressions. Run focused tests sequentially at low parallelism under nice.
Required CI and native independent review govern merge; Claude reviews are
optional and currently unavailable. No local image build, stress, live pairing
or provider turn belongs in this source check.

## Remaining acceptance

Source preflight is not an isolated native-session/runtime pass. Private API
profile/rendering, one-host-per-independent-native-session behavior and the
owner's Q-24 UX ruling remain separate gates. A retained computer name after
sequential replacement does not prove concurrent routing.

Managed project tasks already compose common and repository instructions, but
their actual Claude/Codex canary acceptance still needs reviewed admission,
nonempty rules and an existing keeper access-only delivery profile. A successful
managed task cannot establish native nested rule discovery: nested Git roots
may omit parent wrappers. Define a native project start/injection contract and
test it separately without overwriting repository instructions. New-task fetch
and immutable pin remain mandatory; native preflight cannot replace them.
Keep #173/#91 open and preserve all failed fixtures, ledgers and uncertain
lifecycle receipts. Local inference waits for GPU readiness and reservations.
