# Codex 0.169.1 protocol fixtures

Exact `v2/` bytes from upstream tag `rust-v0.169.1`, commit
`c376cf7396eff83ce4c6ac61b9553df142e7a0be`, embedded file
`codex-rs/app-server-protocol/schema/precomputed/app-server-exports-experimental.json.zst`
(SHA-256 `015daa0d0c1f645c1085ca11ae8e2057d02df357b50f2eaa5fc3b6726972014f`).
Extracted offline on 2026-11-18; no client, daemon, credential or provider used.
Upstream's `precomputed_exports.rs` writes these exact strings for:

```sh
codex app-server generate-json-schema --experimental --out <scratch>
```

The agent image smoke test executes that command with no network or login and
compares all retained schema bytes. Go tests pin hashes and the fields consumed
by dev-env. These are protocol contracts, not live rollout, model entitlement,
phone pairing or acceptance evidence. See [the upgrade audit](../../../../docs/codex-169-upgrade-audit.md).
