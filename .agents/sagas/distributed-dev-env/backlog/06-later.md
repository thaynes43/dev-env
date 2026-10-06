# 06: later

**Status:** backlog. Each item becomes its own plan when picked up.
**Depends on:** 05

| Item | Sketch | Design |
|---|---|---|
| Local-LLM and GPU leases | `LLMLease` resource and `/v1/leases`; the operator labels the holder's pod and a CNP opens egress to the pool endpoint; household use always wins | DESIGN-001 section 8 |
| Per-profile egress and Secrets | Split profile `full` into narrower ones (for example `dev` without Proxmox and hw-ssh credentials) and make the narrow one the default | 6.10 |
| Authentik OIDC for the laptop | Replace the minted-token-plus-port-forward path with a device-code login and an internal ingress for the API | 3.4 |
| Codex exec-server isolation | If not done in phase 4 | 6.3 |
| Web terminal route | Attach to a session from a browser through the operator, without code-server | 3.5 |
| Warm pools | Pre-started session pods for instant start, if pre-pull is not enough | 9 |
