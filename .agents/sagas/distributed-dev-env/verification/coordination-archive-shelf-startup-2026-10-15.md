# Coordination archive shelf startup — source verification (2026-10-15)

Story: R2-SHELF-STARTUP-1015, under coordinator
`01a13866-214e-7aa1-9d26-98d11bc537cd`.

Source-only result at 2026-10-15T13:17:37Z: `agentd shelf` accepts the explicit
`--coordination-archive` flag alongside the existing optional
`--history-archive`, in either order. The default with no flags retains the
legacy shared-volume check. A requested coordination archive calls the existing
`NewCoordArchive(s).CheckMounted()` guard before the shelf reports ready; this
retains its no-follow and mount checks. When both flags are requested, both
archive guards must succeed after the existing shared-volume check.

Focused synthetic command tests passed with the cached Go toolchain and bounded
settings:

```text
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off <cached-go> test -p 1 ./cmd/agentd -run 'TestShelfCoordinationArchive|TestHistoryExportCommands' -count=1
ok   github.com/thaynes43/dev-env/cmd/agentd  0.014s
```

The fixtures cover strict refusal of unknown, positional, value-bearing and
duplicate flags; unavailable coordination archive refusal before ready; clean
exit after cancellation when the guard succeeds; and both archive flags in
either order requiring each guard. They use only temporary directories and the
existing local `/dev/shm` mount; no archive was mounted, activated or accessed.

This evidence is source/test only. It does not claim the archive is mounted,
installed, measured under shelf limits, or usable MVP parity. R2 runtime,
measurement and clock-chronology gates remain unresolved.
