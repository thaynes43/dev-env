# Coordination archive concurrent get and prune memory — 2026-10-18

Offline measurement of four DESIGN-024 worst-case `coord-archive get`s and one
`ctl prune` in one container. This does not accept coordination retention,
mount either archive, start R5, raise the shelf's 256 MiB limit, open a
cohort, or retire v1.

The shelf image is amd64. These runs are not that installed binary. R5 must
not treat either peak below as the Talos shelf's figure.

The procedure is
[coordination-archive-concurrent-measure.sh](coordination-archive-concurrent-measure.sh).

## What ran

`TestCoordArchiveWorstCasePrepare` wrote one canonical task claim of exactly
1 MiB, the same object as the
[one-get record](coordination-archive-get-memory-2026-10-18.md). Its SHA-256
is `2b0438ed32c96226a12f97fc4211f6bd2b432e571d31294d0c81b757fde65a1c`. The
measurement container copied that directory onto a tmpfs at
`$HOME/.coordination-archive`. A second tmpfs at `$HOME/.shared` held one old
rescue directory, one old session log, and one young rescue. `agentd ctl
prune` was given `{"olderThan":"720h","keep":[]}`. It removed the old rescue
and the old log and kept the young rescue. That is one real prune of a
10-byte rescue and a 10-byte log, not a production shared volume.

The four gets are four processes reading that same object. Each stdout is a
pipe the waiter does not read until the prune has returned, so each get stays
resident while the prune runs. All four were alive, and each VmRSS was at
least 30000 KiB, before the prune started.

The container was `debian:bookworm-slim` index
`sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587`,
with `--platform linux/arm64` or `--platform linux/amd64`, `--cpus=0.25`,
`--memory=256m`, `--memory-swap=256m`, `--network=none`, and a read-only
root. The arm64 manifest is
`sha256:a1b86db52ce3daef089e45aabe36dfec4091f82464c25c1fdcf03de197cbe82a`.
The amd64 manifest is
`sha256:a4672c0cb26fbdde88e38fa2dfb6c681942306680e41e4378b28770b6e79ee91`.
`cpu.max` was `25000 100000`. `memory.max` was 268435456. `memory.swap.max`
was 0. HOME, the archive tmpfs, and the shared tmpfs had different device
numbers. No cluster PVC, shelf pod, or history archive was mounted.

Binaries are `CGO_ENABLED=0 GOOS=linux go build -buildvcs=false` with
toolchain go1.27.2. `agentd` is `./cmd/agentd`. The waiter is
`cmd/agentd/coordarchive_shelf_measure_concurrent_wait.go`
(`//go:build ignore`), source SHA-256
`c6d52c90cf2e61f7d818377c205ebb5e1ec8bdef5014f82bb1224585aa5ba6b6`.
`-buildvcs=false` keeps the hash off the git stamp. The waiter refuses any
other command shape, any other memory limit, and a CPU quota that is not
0.25.

| Arch | Where | agentd SHA-256 | waiter SHA-256 |
|---|---|---|---|
| arm64 | native container on this Mac (`uname -m` aarch64) | `e85a6cab9bf9cd75f1a31ae9be887cf34bc343bb73136eb64cbd9c95d0299f75` | `a428074297ea4ad2c9f1a1dff5b56cda3f2aa1c9b813e54d3c044799615404f8` |
| amd64 | `uname -m` is x86_64 and the CPU is `VirtualApple @ 2.50GHz` | `9a21f5abb010a20c3d47f4a04e4bf65ecea5d6f39824bd00e31cd50d72e360da` | `f0e84a8379547d923b9d65355d801d09a21d9bb671bd599cbd8c4ac52a0e2f1e` |

The amd64 row is this Mac's emulator. It is not a Talos node. The waiter
process is inside the same cgroup. `agentd shelf` is not.

## Result

`memory.peak` is the cgroup high-water mark while the four gets were still
resident and the prune had returned. `ru_maxrss` is each child's Linux peak.

| Run | Arch | cgroup `memory.peak` | OOM | Prune | Gets |
|---|---|---:|---|---|---|
| 1 | arm64 | 143159296 bytes | oom 0, oom_kill 0 | exit 0, 25460 KiB | four exits 0, SHA found, 30400–36712 KiB |
| 2 | arm64 | 143355904 bytes | oom 0, oom_kill 0 | exit 0, 25700 KiB | four exits 0, SHA found, 34280–36776 KiB |
| 3 | amd64, VirtualApple | 268443648 bytes | oom 1, oom_kill 1 | exit 0, 61416 KiB | three exits 0 with the SHA; one exit -1, stdout empty |
| 4 | amd64, VirtualApple | 268439552 bytes | oom 1, oom_kill 1 | exit 0, 58144 KiB | three exits 0 with the SHA; one exit -1, stdout empty |

Stdout of each finished get was 1049136 bytes. On both amd64 runs the
container's `OOMKilled` status was true and the waiter exited 1. The killed
process was one of the four gets. The prune had removed
`rescue/measure-old/20260801-0000` and `logs/measure-old.log` and kept one
rescue. Docker's exit was the waiter's, after it had printed that receipt.

Native arm64 stayed under the 256 MiB limit. The higher arm64 peak,
143355904 bytes, left 125079552 bytes under `memory.max`. The VirtualApple
amd64 binary reached the limit and the kernel killed one get. That is not a
measurement of the installed amd64 shelf. R5 still decides whether the
Talos shelf's 256 MiB limit changes. This record does not raise it, start
R5, or mount either archive.
