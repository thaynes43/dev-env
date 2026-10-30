# Pinned Codex thread/start protocol

Generated offline by coordinator dev-env-1030-005602 on 2026-10-30:

```
codex --version
# codex-cli 0.160.1
codex app-server generate-json-schema --experimental --out <scratch>
```

Only the complete `v2/ThreadStartParams.json` and `v2/ThreadStartResponse.json`
outputs were copied, unchanged. No daemon, proxy, socket or provider was used.

SHA-256:

- ThreadStartParams.json: `80a40a7fac15b4bf70efb7f893fb353acc0a0d30c68f54aee4f01923deca85de`
- ThreadStartResponse.json: `92f5ddc37717b922de1dc0b0405e9a6d463f370554e2091610c6e595bc2616d4`

The request has no required fields. `cwd`, `model` and `projectId` are optional;
the create adapter sends cwd/model without guessing a connector project ID.
These fixtures establish protocol shape, not live pairing, caller authority,
provider model entitlement, RC visibility or a usable launch route.
