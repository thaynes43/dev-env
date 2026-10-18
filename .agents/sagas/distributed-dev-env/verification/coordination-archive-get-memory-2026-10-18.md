# Coordination archive worst-case get memory — 2026-10-18

Offline measurement of one DESIGN-024 R2 `get`. This does not accept
coordination retention, mount either archive, start R5, raise the shelf's
256 MiB limit, open a cohort, or retire v1.

## What ran

`agentd ctl coord-archive get` with resource, name, UID, resourceVersion and
SHA256 pins. The object is one canonical task claim of exactly
`MaxCoordArchiveObjectBytes` (1 MiB). `TestCoordArchiveWorstCaseGetRoundTrip`
puts and reads that object through the command path. The memory run uses the
production `agentd` binary, not the test process.

The container was `debian:bookworm-slim` image
`sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587`,
`linux/arm64`, `--cpus=0.25`, `--memory=256m`, `--memory-swap=256m`. The
archive directory was a tmpfs mounted at `$HOME/.coordination-archive`, so
the production device check saw a filesystem other than HOME. No cluster
PVC, shelf pod, or history archive was mounted.

The measured binary is `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build
./cmd/agentd`. SHA-256
`2a3946232d880ca9699a41532be6f813940e3a43a24c989e57ac99be3158b4c2`.
Test files are not in that binary. The waiter is
`cmd/agentd/coordarchive_shelf_measure_wait.go` (`//go:build ignore`),
SHA-256
`2b538794c27f3ccf0f9d510c0feb7bc5d2ec07044e3bbb0c673511aeffc3cdd7`.
It refuses to run unless cgroup `memory.max` is 268435456 and prints the
child's Linux `ru_maxrss`.

## Result

| Run | `ru_maxrss` | `memory.max` | OOM events | Exit | SHA found |
|---|---:|---:|---|---:|---|
| 1 | 36052 KiB | 268435456 | oom 0, oom_kill 0 | 0 | yes |
| 2 | 36932 KiB | 268435456 | oom 0, oom_kill 0 | 0 | yes |

The object SHA-256 is
`2b0438ed32c96226a12f97fc4211f6bd2b432e571d31294d0c81b757fde65a1c`.
Stdout was 1049136 bytes both times. The recorded peak is the higher run,
36932 KiB. Four times that peak is 147728 KiB, which is under 256 MiB.
That arithmetic is not a measurement of four concurrent gets or of a prune.
R5 still decides whether those fit, and R5 is not started.
