# 06: later

**Status:** backlog. Each item becomes its own plan when picked up.
**Depends on:** 05

Local-LLM and GPU leases, listed here first, became [plan 09](09-gpu-local-llm.md) on
2026-10-06.

| Item | Sketch | Design |
|---|---|---|
| Narrower default profile | Make `dev` (from plan 09) the default for every agent kind, with `full` by request; egress tiers already landed in 01 | 6.10 |
| Authentik OIDC for the laptop | Replace the minted-token-plus-port-forward path with a device-code login and an internal ingress for the API | 3.4 |
| Codex exec-server isolation | If not done in phase 4 | 6.3 |
| Web terminal route | Attach to a session from a browser through the operator, without code-server | 3.5 |
| Warm pools | Pre-started session pods for instant start, if pre-pull is not enough | 9 |
| DRA for GPUs | Move VRAM claims from device-plugin units to ResourceClaims once S-9 finds consumable capacity usable on these cards; makes the budget per card on multi-card nodes | 8.2 |
| Satellite tool backends | ComfyUI or video generation on the 5090 PC as a ToolPool backend, under the same owner-first rules | 8.4 |
| CephFS from gasha01 | A ceph-csi-cephfs driver for the Proxmox Ceph's `k8s-cephfs`, if tool workspaces need RWX off the in-cluster MDS | 6.6 |
| More harnesses | goose or Qwen Code as agentd adapters, if opencode falls short with a model | 6.13 |
