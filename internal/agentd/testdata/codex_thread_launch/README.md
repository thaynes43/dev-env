# Pinned Codex thread observation protocol (DESIGN-057)

Generated offline on 2026-11-09 for OpenProject #489, the same way as
`../codex_rc_create`:

```
codex --version
# codex-cli 0.160.1
codex app-server generate-json-schema --experimental --out <scratch>
```

Copied from `<scratch>/v2/`: `ThreadListParams`, `ThreadListResponse`,
`ThreadTurnsListParams`, `ThreadTurnsListResponse`, `ThreadItemsListParams`
and `ThreadItemsListResponse`. `TestCodexThreadPinnedSchema` pins their
digests and the fields the thread-launch observer reads. No live daemon,
credential or account was involved.
