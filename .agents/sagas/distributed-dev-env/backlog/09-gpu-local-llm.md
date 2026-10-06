# 09: GPUs and local LLMs

**Status:** backlog
**Depends on:** 08 (tool pods); Q-06 (control-plane GPUs); spikes S-9 (VRAM units)
and S-11 (opencode)
**Parallel with:** nothing

## Goal

The scheduler sees GPU memory. Household AI keeps first claim on every card. Agents
get GPU tool pods, LLM leases, and local-model sessions on opencode that are fleet
members beside Claude Code and Codex. DESIGN-001 6.13, 8.2, 8.3; D-30 to D-33.

## Order

GPU accounting comes first and is household work: no agent GPU pod runs until every
GPU workload on the card it could land on declares its VRAM.

## In haynes-ops (GitOps PRs, one app per PR, verified after each)

- NVIDIA device plugin: the time-slicing configs (one replica per GiB of VRAM under
  `nvidia.com/gpu.shared`) per card model, chosen by a node label from new NFD
  rules beside the existing model rules. Use the mechanism S-9 confirmed.
- Household GPU workloads request their steady VRAM in those units and drop their
  `NVIDIA_VISIBLE_DEVICES` UUID pins: llama-server, ollama-prime, ollama-assist02,
  ComfyUI, whisper, vexa-whisper, kokoro, speech-to-phrase, Immich ML. After each
  PR, confirm the app runs on the same card and still serves (Home Assistant voice
  for llama-server; a render for ComfyUI).
- `GpuMissing` and the GPU exporter alerts keep working with the new resource name.
- Pools as data: `household-llama` (shared: llama-server's slot count less one for
  agents), `ollama-prime` (shared), `llm-coder` (dedicated, `kind: llm`; stays at
  zero until a card has room).
- GrantPolicy: normal leases on shared pools approved by policy.
- Per Q-06: GPU tool pods allowed on GPU control-plane nodes with the caps it sets,
  or workers only.
- talosw04's `dev-env.haynesops.com/gpu-lend` label, documented in its runbook.

## In this repo

- `ToolPool.spec.gpu.memoryGiB` turned into a request for VRAM units, a
  framework VRAM cap from the claim (environment for llama.cpp, vLLM, PyTorch
  apps), and the eviction guard that reads `nvidia-gpu-exporter`.
- `LLMLease` CRD and `/v1/leases`: pool slots, the FIFO queue, the holder label and
  its CNP, expiry, policy check through the broker.
- `kind: llm` pools: start on the first lease, scale to zero 30 minutes after the
  last.
- agentd's opencode adapter (start, resume, status, deliver, MCP registration),
  opencode's config rendered with every permission `allow`, and `waiting` and
  resume when a lease is preempted or expires.
- `agent: opencode` and profile `dev` in `POST /v1/sessions`; `agent-run --agent
  opencode`, `agent-run lease`.
- The `image` pool's GPU claim, and GPU render Jobs for Blender if measured previews
  justify them.

## Acceptance

- `kubectl describe node` shows VRAM units per GPU node, and every household GPU pod
  requests them.
- An agent GPU tool pod lands on a card with free units and runs. When a household
  pod needs those units, the agent pod is preempted within its grace period, and its
  job is recorded as interrupted.
- A second GPU claim that does not fit stays Pending with the scheduler's reason.
- An opencode session on the shared pool opens a PR on a small real task. Household
  voice requests made during the run are not delayed by it.
- A lease past its time loses its egress label, and the session waits rather than
  fails.
- No household GPU workload lost a request or a model load across the migration
  (Grafana, Home Assistant voice traces).
