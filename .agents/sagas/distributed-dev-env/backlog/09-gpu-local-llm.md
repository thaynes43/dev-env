# 09: GPUs, satellites and local LLMs

**Status:** backlog
**Depends on:** 08 (tool pods); Q-06 (decided 2026-10-06: dynamic allocation and
satellites); Q-09 (which household apps lend idle VRAM) and Q-10 (when satellites may
be used); spikes S-9 (VRAM units), S-11 (opencode), S-13 (budget) and S-14
(satellites)
**Parallel with:** nothing

## Goal

The scheduler sees GPU memory. Household AI keeps first claim on every card, and the
agent share of each card, control-plane cards included, grows and shrinks with what
the household needs at the moment. Tom's Mac and PCs serve the larger models while
he is not using them. Agents get GPU tool pods, LLM leases, and local-model sessions
on opencode that are fleet members beside Claude Code and Codex. DESIGN-001 6.13,
8.2 to 8.4; D-30 to D-35.

## Order

1. **GPU accounting** (household work): no agent GPU pod runs until every GPU
   workload on the card it could land on declares its VRAM.
2. **The dynamic budget**: reservations, probes, reserve pods, reclaim. Agent GPU
   pods start only after this works on one card.
3. **LLM pools and leases** with `household` and `cluster` backends, and opencode.
4. **Satellites**, one machine at a time, the 5090 PC first.

## In haynes-ops (GitOps PRs, one app per PR, verified after each)

- NVIDIA device plugin: the time-slicing configs (one replica per GiB of VRAM under
  `nvidia.com/gpu.shared`) per card model, chosen by a node label from new NFD
  rules beside the existing model rules. Use the mechanism S-9 confirmed.
- Household GPU workloads request their steady VRAM in those units and drop their
  `NVIDIA_VISIBLE_DEVICES` UUID pins: llama-server, ollama-prime, ollama-assist02,
  ComfyUI, whisper, vexa-whisper, kokoro, speech-to-phrase, Immich ML. After each
  PR, confirm the app runs on the same card and still serves (Home Assistant voice
  for llama-server; a render for ComfyUI).
- A `GpuReservation` entry beside each household GPU app: floor, burst, class
  (`interactive` or `batch`, per Q-09), probe, schedule (DESIGN-001 D-34).
- PriorityClass `dev-env-gpu-reserve` (value -1: above agents, below household).
- `dev-env-gpu-guard`'s DaemonSet, its RBAC (pods get, list and eviction in
  `dev-tools`, read on the budgeter's Lease) and the nvidia RuntimeClass.
- `GpuMissing` and the GPU exporter alerts keep working with the new resource name.
- Pools as data: `household-llama` (llama-server's slot count less one for agents),
  `ollama-prime` (household), `llm-coder` (backends: 5090, 4090, cluster),
  `llm-big` (backend: the Mac), `llm-small` (backends: any cluster card with room,
  4090). Exact model ids and file checksums pinned.
- GrantPolicy: normal leases approved by policy.
- talosw04's `dev-env.haynesops.com/gpu-lend` label, documented in its runbook.
- For each satellite: an `InferenceWorker` (address, OS, model memory, pools, lend
  policy per Q-10), a DHCP reservation for its address (Tom, or an agent with his
  confirmation), and a clusterwide egress policy from lease-holding pods to its
  address and port.
- The internal route `dev-env-api.haynesops.com` on traefik-internal, TLS passed
  through by SNI to the operator's satellite listener (9443).

## In this repo

- `ToolPool.spec.gpu.memoryGiB` turned into a request for VRAM units and a framework
  VRAM cap from the claim (environment for llama.cpp, vLLM, PyTorch apps). Every
  agent GPU pod is created with the `dev-env.haynesops.com/gpu-budget` scheduling
  gate.
- `dev-env-gpu-guard`, a DaemonSet on GPU nodes: NVML every 5 s; evicts agent GPU
  pods on a near-full card, and all of them on its node when the budgeter's Lease is
  over 5 minutes old. GPU pools may run on
  control-plane nodes only with limits of at most 2 CPU and 16Gi (CRD validation).
- The budgeter in the operator: read reservations, probes (every 15 s) and the
  exporter; compute each card's reserve and agent budget; hold the reserve with
  reserve pods at priority -1; remove agent pods' scheduling gates only toward nodes
  with budget that are not being reclaimed; renew its Lease; reclaim in order with notices and graces (60 s for LLM backends,
  120 s for tools); shrink after a 15-minute cool-down; `GET /v1/gpus`,
  `POST /v1/gpus/{node}/hold` (Tom only); metrics for a Grafana panel.
- The tool contract's `POST /reclaim`.
- `LLMLease` CRD and `/v1/leases`: pool slots, the FIFO queue, the holder label,
  expiry, policy check through the broker. Pools with ordered backends; one active
  backend per pool; move a pool by starting the next backend, waiting for ready,
  switching, then stopping the old one.
- agentd's LLM route on the loopback gateway (`/llm/<pool>/v1`): forward to the
  active backend, add the lease token for satellites, retry once on a backend
  change.
- `kind: llm` cluster backends: start on the first lease, scale to zero 30 minutes
  after the last.
- `dev-env-satellite` for macOS arm64 and Windows amd64 (the Mac installer adds the
  root LaunchDaemon that sets `iogpu.wired_limit_mb` at boot): enrol, heartbeat, run
  `llama-server` (or `mlx_lm.server` where S-14 favoured it), download and verify
  pinned model files, HTTPS with the operator-issued certificate, lease-token
  checks, owner-first detection, `pause`, menu-bar and tray icon. Signed release
  binaries.
- Operator: the satellite listener on 9443 (client certificates required except on
  enrol); `InferenceWorker` status from heartbeats; enrolment with one-time codes
  (`agent-run satellite enroll`, Tom only; bound to a name, 15 minutes, single use,
  carrying the CA fingerprint, rate-limited); certificate issue and renewal; lease
  tokens.
- agentd's opencode adapter (start, resume, status, deliver, MCP registration),
  opencode's config rendered with every permission `allow` and its provider pointed
  at agentd's LLM route, and `waiting` and resume when no backend is available.
- `agent: opencode` and profile `dev` in `POST /v1/sessions`; `agent-run --agent
  opencode`, `agent-run lease`, `agent-run gpu`.
- The `image` pool's GPU claim, and GPU render Jobs for Blender if measured previews
  justify them.

## Acceptance

- `kubectl describe node` shows VRAM units per GPU node, and every household GPU pod
  requests them.
- `agent-run gpu` shows each card's reserve and agent budget, control-plane cards
  included.
- An agent GPU tool pod runs on a card with budget. When a batch household app on
  that card becomes active, the tool gets its reclaim notice, finishes or records its
  job as interrupted, and is gone within its grace; the household app then runs. A
  second claim that does not fit stays Pending with the scheduler's reason.
- 15 minutes after the household app goes idle, the agent budget on that card grows
  back, and a Pending claim starts.
- An interactive household app's burst is never lent: Home Assistant voice latency
  is unchanged across the test.
- With the operator scaled to zero for 6 minutes, the guard evicts the agent GPU pods
  on each GPU node and no new one starts.
- A household pod that does not fit preempts a reserve pod, never waits behind one.
- An opencode session on `llm-coder` opens a PR on a small real task while the 5090
  PC serves it. Tom starts a game mid-run: the satellite drains within 30 seconds,
  the pool moves to the next backend, and the session carries on (or waits, then
  resumes) without being restarted.
- A request to a satellite without a valid lease token is refused; a lease past its
  time loses its egress label and its token stops working.
- `llm-big` on the Mac serves a 100B-class model to one session.
- No household GPU workload lost a request or a model load across the migration
  (Grafana, Home Assistant voice traces).
