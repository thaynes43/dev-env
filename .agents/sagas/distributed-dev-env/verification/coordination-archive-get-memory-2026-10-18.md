# Coordination archive worst-case get memory — 2026-10-18

Offline measurement of one DESIGN-024 R2 `get`. This does not accept
coordination retention, mount either archive, start R5, raise the shelf's
256 MiB limit, open a cohort, or retire v1.

The shelf image is amd64 (`images/agent/Dockerfile` says amd64-only because
the Talos nodes are x86_64). These runs are not that installed binary. R5
must not treat either peak below as the Talos shelf's figure.

## How the archive was filled

`TestCoordArchiveWorstCasePrepare` writes one canonical task claim of exactly
`MaxCoordArchiveObjectBytes` (1 MiB). It runs only when all three variables
are set:

- `DEV_ENV_COORD_PREPARE_ROOT`: archive directory, a different filesystem
  device from the home below
- `DEV_ENV_COORD_PREPARE_HOME`: home passed to the mount check
- `DEV_ENV_COORD_PREPARE_RECEIPT`: JSON file with `resource`, `name`, `uid`,
  `resourceVersion` and `sha256`

The measurement container then copies that directory onto a tmpfs mounted at
`$HOME/.coordination-archive` and runs `agentd`. The production device check
sees the tmpfs, not HOME. The copy is `cp -a "$DEV_ENV_COORD_PREPARE_ROOT/."
"$HOME/.coordination-archive/"`.

`TestCoordArchiveWorstCaseGetRoundTrip` puts and reads the same ceiling-sized
object through the command path. It does not measure memory.

## What ran

`agentd ctl coord-archive get` with resource, name, UID, resourceVersion and
SHA256 pins. The container was `debian:bookworm-slim` image
`sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587`,
`--cpus=0.25`, `--memory=256m`, `--memory-swap=256m`. No cluster PVC, shelf
pod, or history archive was mounted.

Binaries are `CGO_ENABLED=0 GOOS=linux go build -buildvcs=false ./cmd/agentd`
with toolchain go1.27.2. The Go sources are commit `55e4c25`. This record's
later wording is not in the binary. `-buildvcs=false` keeps the hash off
the git stamp. Test files are not in that binary. The waiter is
`cmd/agentd/coordarchive_shelf_measure_wait.go` (`//go:build ignore`),
built with `go build -buildvcs=false -o coord-get-measure-wait
./cmd/agentd/coordarchive_shelf_measure_wait.go`. It refuses to run unless
cgroup `memory.max` is 268435456 and prints the child's Linux `ru_maxrss`.

The get invocation, after the copy, is:

```sh
HOME=/home/measure \
DEV_ENV_COORD_EXPECT_SHA=2b0438ed32c96226a12f97fc4211f6bd2b432e571d31294d0c81b757fde65a1c \
DEV_ENV_COORD_GET_OUT=/tmp/get.out \
coord-get-measure-wait -- agentd ctl coord-archive get \
  --resource taskclaims \
  --name task-claim.cccccccccccccccccccccccccccccccc.dddddddddddddddddddddddddddddddd \
  --uid 44444444-4444-4444-8444-444444444444 \
  --resource-version 42 \
  --sha256 2b0438ed32c96226a12f97fc4211f6bd2b432e571d31294d0c81b757fde65a1c
```

| Arch | Where | agentd SHA-256 | waiter SHA-256 |
|---|---|---|---|
| arm64 | native container on this Mac | `ae2769c72099ed41f991aae26db0161ee4a109ce5320602f083eb4f0dd94ef62` | `2b538794c27f3ccf0f9d510c0feb7bc5d2ec07044e3bbb0c673511aeffc3cdd7` |
| amd64 | `uname -m` is x86_64 and the CPU is `VirtualApple @ 2.50GHz` | `27a2b150070ad080e0931dacde7960691bdd393aaf2cbba802671c51bb842802` | `d4405fc533ebcb0f0b9793a48d6ee8749f1f2598407fd6269210bef26ac3824a` |

The amd64 row is this Mac's emulator. It is not a Talos node.

## Result

| Run | Arch | `ru_maxrss` | `memory.max` | OOM events | Exit | SHA found |
|---|---|---:|---:|---|---:|---|
| 1 | arm64 | 38480 KiB | 268435456 | oom 0, oom_kill 0 | 0 | yes |
| 2 | arm64 | 41140 KiB | 268435456 | oom 0, oom_kill 0 | 0 | yes |
| 3 | amd64, VirtualApple | 84688 KiB | 268435456 | oom 0, oom_kill 0 | 0 | yes |
| 4 | amd64, VirtualApple | 80328 KiB | 268435456 | oom 0, oom_kill 0 | 0 | yes |

The object SHA-256 is
`2b0438ed32c96226a12f97fc4211f6bd2b432e571d31294d0c81b757fde65a1c`.
Stdout was 1049136 bytes each time. One get fit in the 256 MiB limit in
all four runs. Four times the higher VirtualApple peak is 338752 KiB, which is above
256 MiB. That arithmetic is not four concurrent gets and it is not a prune.
R5 still makes that judgment, and R5 is not started.
