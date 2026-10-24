# Disposable asynchronous session-recovery proof

Only the existing hosted PostgreSQL workflow invokes this fixed-action helper.
It uses the digest-pinned official PostgreSQL 16 service's own binaries, two
postmasters inside that disposable service (CPU 2 / memory 512 MiB), no image build.
One suite at a time, nice 19, GOMAXPROCS=2 / -p 1; no stress/looped suites.

After real session/login insertion, take a streaming physical basebackup and
wait for both rows on the standby. Stop its receiver before acknowledged primary
DELETEs. Restart it without primary_conninfo and promote. Assert both rows
resurrected and active WAL timeline changed; leave witness unchanged. Two app
pools directed to the promoted server must deny both session and callback before
any observer/controller. Then model the existing recovery owner's explicit fresh
witness/generation open and verify old pooled-primary connections deny as well.
The helper grants no runtime power and is never run against production.

No environment means explicit skipped disposable proof, never a success claim.
This is PostgreSQL protocol evidence; real CNPG/role/auth/isolation/Gate A and
external admission RBAC acceptance remain independent. API transport negatives
use disposable TLS servers, not a live Kubernetes credential or witness.
