# Coordination archive shelf startup — source verification (2026-10-15)

Story: R2-SHELF-STARTUP-1015, owned by the existing Chapter6 coordinator.

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

The initial filter above selects the three coordination-named methods and the
history-export regression; it does not select the separately named
`TestShelfBothArchivesRequireBothGuards`. The coordinator ran that omitted
method on 2026-10-15 at 13:21:34–13:21:36 UTC, with the same cached toolchain,
shared lock, GOMAXPROCS=2/-p1/nice19 and downloads off:

```text
go test -p 1 -count=1 -v -run '^TestShelfBothArchivesRequireBothGuards$' ./cmd/agentd
=== RUN   TestShelfBothArchivesRequireBothGuards
--- PASS: TestShelfBothArchivesRequireBothGuards (0.00s)
PASS
ok github.com/thaynes43/dev-env/cmd/agentd 0.019s
```

This additional method actually ran without skips. It verifies both flag
orders with history refusal, then both orders with history succeeding and
coordination refusing. Executable source is unchanged by this evidence-only
correction. The production-code review found no blocker; no runtime archive
or credential action was performed.

*Update, 2026-10-18:* the shelf-limit get measurement is recorded in
[coordination-archive-get-memory-2026-10-18](coordination-archive-get-memory-2026-10-18.md).
Runtime mount, clock chronology and installed acceptance remain unresolved.
