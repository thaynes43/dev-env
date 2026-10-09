# Cross-pod Git wait observations

**Source preparation only; no new trial is authorized by this document.** This
adds bounded observations to the failed-status path for
[#130](https://github.com/thaynes43/dev-env/issues/130). The
[third result](2026-10-09-cephfs-cost-result.md) remains incomplete, and its
original protocol and limits remain the record of that execution.

The native local smoke test passed with one tiny Git status operation, preserved
staged/untracked WIP and index bytes, and cleaned its fixture and trace resources.
A separate sleeping-child check reached the five-second checkpoint and genuinely
reaped its child. Its syscall observation was permission denied. A read-only
check of the existing shelf pod also found Yama mode 2 on `talosw01`; `talosw02`
was not covered. No capability, sysctl, security profile or workload change was
made. A readable wait channel alone does not identify a blocked Git syscall.

These findings prevent launching another trial that requires this unavailable
evidence. Review an in-process alternative or prove that the actual admitted
fixture can supply the required evidence under its existing privileges before
reviewing a new execution plan. Preserve denied or unavailable observations;
never convert them into a successful diagnosis.

## Source behavior

Only the A/B fixture status owner changes. It retains the exact Git status flags,
`GIT_OPTIONAL_LOCKS=0`, native Trace2, staged-WIP assertions and original command
deadline. Reconnect keeps its existing subprocess path.

If the same child is still running at five seconds, its sole parent makes one
bounded observation. A non-consuming wait check proves ownership; an exited,
already reaped or uncertain child is refused. The collector never reaps that PID.
Read at most 1,024 syscall bytes and 128 wait-channel bytes, then retain only
fixed categories/reasons in a receipt of at most 1KiB. Discard syscall arguments,
register addresses, pointers and raw wait-channel names. No ptrace, polling,
extra Git operation, privilege increase or filesystem warmup is introduced.

After a status timeout and genuine child reap, at most one separate metadata
child reads two fixed synthetic peer paths and their index tuples. It is admitted
only with at least one second left on the original helper deadline and has a
one-second command cap. Genuine reap remains mandatory; a deadline is never
permission to force-delete a writer. The bounded SHA-1 index parser accepts
versions 2/3, validates checksums and bounds, and refuses split, sparse or unknown
mandatory extensions. Status duration, child CPU and Trace2 finish before this
optional sample. Cancellation preserves the interruption receipt and propagates.

All existing ceilings remain: 15-second ordinary commands, 145-second helper,
180-second Jobs, two worker fixture pods, 250m/256Mi main and 100m/128Mi init,
2,048 entries, 16MiB and one fresh disposable 1Gi claim. There is no retry,
resource increase, configuration remedy, reconnect/remount, real remote, provider
login, model call or v1 cutover in this source unit.

## Required next execution review

A later predeclared plan must identify the exact merged helper checksum, signed
image and decoded A/B Job arguments. Review actual placement, CPU/init bounds,
mounts, disabled timezone injection, no service-account token and no other PVC.
Do not reuse an old claim or a previous baseline.

Freeze the latest 30-minute telemetry window immediately before that execution,
after confirming the exact required query series are available and fresh.
Coverage must remain at least 95%. Every limit is the lesser of the original
quiet limit and the new formula limit. Preserve active/standby MDS distinctions,
30-second Ceph and 150-second household freshness, all immediate stop rules and
the original consecutive fresh-observation thresholds. Missing required series
is a stop even if the exporter is still up. Distinct underlying scrape times
are required for consecutive observations.

The observer must schedule household samples on fixed 60-second slots anchored
to its start. Warning-triggered samples cannot shift that grid. Preserve the
10-second storage cadence, finite acquisition/overall caps, missing-data and
cadence-gap stops, UID/readiness/restart checks and the complete five-minute
post-stop window. The earlier 60–70-second intervals and three telemetry gaps
remain recorded limitations, not evidence of zero household impact.

The root launch checkpoint must review fresh gates and exact artifacts. Normal
Job deletion and genuine UID termination proof precede GitOps removal of the
disposable claim/PV. Record protected identities again, verify them after cleanup
and end the activity declaration. Any uncertainty remains incomplete. Passing
this diagnostic alone would still leave remedy, retained-state/remount, household
impact and independent provider/lifecycle/phone acceptance gates open.
