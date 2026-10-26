# Disposable asynchronous session-recovery proof

Only the existing hosted PostgreSQL workflow invokes this fixed-action helper.
It uses the digest-pinned official PostgreSQL 16 service's own binaries, two
postmasters inside that disposable service (CPU 2 / memory 512 MiB), no image build.
One suite at a time, nice 19, GOMAXPROCS=2 / -p 1; no stress/looped suites.

After a real session insertion, take a streaming physical basebackup and wait
for the row on the standby. Stop the standby, restart it without
primary_conninfo and promote it. Assert the WAL timeline and postmaster start
time changed, then show that an app pool on the promoted server serves the
session with no witness or human action, and that a rotated generation denies
it. (Until 2026-10-26 this proof showed a recovery witness denying the session;
DESIGN-029 records why that witness was dropped.) The helper grants no runtime
power and is never run against production.

No environment means explicit skipped disposable proof, never a success claim.
This is PostgreSQL protocol evidence; real CNPG/role/auth/isolation/Gate A
acceptance remains independent.
