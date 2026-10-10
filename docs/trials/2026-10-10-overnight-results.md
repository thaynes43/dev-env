# Overnight delivery results — 2026-10-10 UTC

This records the campaign that began at 05:29 UTC. Source, deployment and
provider acceptance are separate results. The full shared-project Codex MVP
has not passed acceptance. Use the [morning test guide](../morning-test-guide.md)
and [workflow diagrams](../workflow-guide.md) to review the intended product.

## Delivered

Release **2.11.0** is published and signed. It includes the retained task
budget ledger/API, owned native executor, authenticated local readiness,
managed-worker enforcement and isolated native admission inspector. Required
checks and actual Claude advisory reviews passed before the source merges.

The current schema is applied. The v2 operator is 2/2 Ready at the reviewed
digest; budget storage, its protection and the isolated network profile are
installed. General shared-project/coordinator opt-ins remain disabled.

**Real Claude task:** one bounded v2 task completed with provider exit code 0,
`success` and two turns. It used `claude-opus-5-5` at `xhigh`, a two-minute
timeout and source commit `739872fc`. Its actual admitted Pod had no init
containers and CPU 2 / memory 4 GiB limits. The namespace timezone-injection fix
in [haynes-ops#3741](https://github.com/thaynes43/haynes-ops/pull/3741) was
reviewed, merged and applied first. The supported reap completed; the session,
Pod and home PVC were verified absent, and the activity was ended.

The final provider sentence was not retained because the readable log stream
was parsed incorrectly before reap. The rescue list was empty. This proves
task execution and cleanup; it does not verify rescue availability or private
home retention. No second provider task was used to fill those evidence gaps.

The NFS read-only metadata check succeeded as UID 1000 over NFS4.2. Its Job,
pods, PVC, PV and child Kustomization were subsequently verified absent.
Cleanup required restoring lost Flux inventory before the reviewed retirement;
the original completed probe was not rerun. This establishes read access and
cleanup, not write authorization, quota or shared-workspace acceptance.

Installed external Ceph manager source explained the cache behavior. HTTP
success alone can serve an old cached payload. Paired publication counters
demonstrated a successful new payload with a conservative age bound of about
20 seconds. This is not daemon-report age or service queue latency proof.
Replica-adjusted free capacity was about 109 TiB; export directory quota
remains Unknown.

## Results that did not pass

**Native Codex:** the first Pod dry-run exposed a ServiceAccount dependency
ordering defect. [Haynes-ops#3739](https://github.com/thaynes43/haynes-ops/pull/3739)
moved the unchanged identity into the existing RBAC prerequisite. Corrected
GitOps delivery then created the exact fixed Pod. Its preflight and admitted
profile passed, and one budget creation was recorded. The sole acceptance
client exited unsuccessfully; its wrapper discarded captured stderr, so the
failed stage and cause remain Unknown. The client ran through a separate
`kubectl exec`; the container's PID 1 was a finite sleep. Its exit code does
not report the client's result. Native readiness and owned stop did
not pass acceptance. The original container terminated with exit code 0 and no
restart; the failed Pod and ledger are retained. Container termination does
not establish ledger `StopConfirmed` or the complete campaign-stop requirement.

A subsequent read-only audit found a concrete profile defect: the immutable
signed image sets `CODEX_HOME=/home/dev/.codex`, while the fixture overrides
`HOME=/fixture/home` and omits `CODEX_HOME`. The owned CLI requires its Codex
home to match the private home before its first authority GET. The source fix
merged in [dev-env#170](https://github.com/thaynes43/dev-env/pull/170) explicitly sets
the private `CODEX_HOME` and adds independent profile and admission regressions.
It does not recover the discarded historic error or prove
which check actually failed in that run. A source-only diagnostic candidate
now retains bounded redacted stderr and the last completed phase; it has not
run against another native fixture. Neither correction has been applied to a
new fixture. The original failed Pod and budget history remain intact.

**Storage observer:** three delivery/setup failures stopped this route. The
first window missed scheduled slots; the second refused insufficient time
before any acquisition; the third rejected a timestamp passed to the metrics
API. The third attempt's parallel cache pair and MQTT check completed, but
zero complete baseline points passed. No 30-minute/95% baseline or writable
NFS test resulted. One earlier correlated external OSD latency warning is
preserved; it is not two consecutive breaches. No thresholds were weakened.

**Writable NFS helper:** three advisory passes exposed related marker and
whole-tree scan handoff races. The proposal remains unmerged at
[haynes-ops#3737](https://github.com/thaynes43/haynes-ops/pull/3737), with suspended
Jobs and no writable execution. The final already-in-flight source change is
saved. Its already-running final review also found that 15-second handoff
waits can expire during the peer's finite Git/archive work. That finding is
recorded for the next bounded design decision; no further repair was started.
Root-export RW exposure is disclosed and has no launch sign-off.

The two stopped storage routes carry their original failure history. Native
question cards request bounded next decisions; arrival on the owner's phone
and an answer are not claimed. Neither a new agent nor a new Pod resets the
three-failure rule. Independent work continued rather than silently retrying
the stopped routes.

## Time spent by piece

These are delivery-unit wall times, not provider billing. Work overlapped;
adding these durations does not give elapsed clock time. Approximate values
are labelled. Root coordination/document writing and hosted CI wait overlap
with these units; no precise token accounting is available.

| Piece | Unit time | Result |
|---|---:|---|
| Schema delivery | 8m31s | Applied, source/live equality verified |
| Owned native executor | 39m33s | Source merged #162 |
| Retained ledger/API | About 43m | Source merged #163 |
| Initial gateway/client route discovery | About 12m | Existing Proxmox route works; guest SSH is not required for NFS |
| Read-only NFS delivery | 27m10s | Metadata check passed; unlimited injected init deviation preserved |
| Cleanup recovery and initial helper source | 43m10s | Recovery source delivered; finite helper prepared |
| Native HTTPS/readiness integration | 25m09s | Source merged #166 |
| Managed-worker budget enforcement | About 45m | Source merged #167 |
| Isolated native inspector | 28m36s | Source merged #168 |
| Retained storage/catalog GitOps foundation | 20m53s | Applied through #3731 |
| Installed Ceph cache source audit | 13m59.6s | Cache publication witness established |
| Final NFS discovery retirement | 8m48s | All disposable resources verified absent |
| Native staged rollout | 31m43s | Operator/profile installed; no test claimed |
| Writable NFS source proposal | 30m | Stopped with same-blocker review history |
| First incomplete observer window | 12m26s | Missed schedule; no baseline pass |
| Observer automation repair | 10m12s | Source prepared; 12s cap overrun recorded |
| Observer causal/coverage completion | 4m54s | 26 small tests passed; no live storage acceptance |
| Second observer setup | About 4m10s | Refused before acquisition |
| Third observer acquisition | 20.34s, plus setup | API timestamp rejected; no complete point |
| Native one-shot acceptance | 17m12s | Failed/Unknown; original container termination verified |
| Native failure audit | 5m09s allocated window | API 201 is the last proven stage; 9s administrative receipt overrun recorded |
| Real Claude CLI/task verification | 19m26s from first command | One provider task succeeded; namespace fix applied; cleanup verified; rescue unverified |
| Private Codex-home source fix | 4m33s from first command | #170, focused tests and actual advisory passed; no new runtime trial |

The parent window ends at 09:30 UTC with an eight-hour estimated aggregate
child-effort cap. Failed units release unused reservations against their actual
duration. Estimates do not turn into a claim of measured provider usage.

## Remaining scope

The [morning guide](../morning-test-guide.md#finish-the-shared-project-codex-mvp)
estimates 7–14 engineering hours plus 20–40 minutes of owner device acceptance for
the remaining shared-project Codex MVP, assuming storage health is acceptable.
This includes storage qualification, two retained hosts, budget/decision
integration, task ownership and both-provider rules. The failed native
acceptance and observer diagnostics need explicit evidence before another run.

Full capability parity, management console, automatic image drains and v1
cutover are additional work. The existing v1 host, enrollment, keeper refresh
owner and shelf remain preserved. [Issue130](https://github.com/thaynes43/dev-env/issues/130)
and [issue154](https://github.com/thaynes43/dev-env/issues/154) remain open.
