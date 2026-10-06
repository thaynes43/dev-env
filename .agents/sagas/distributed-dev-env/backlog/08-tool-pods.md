# 08: tool pods

**Status:** backlog
**Depends on:** 02 (idle detection and drain machinery); spike S-10
**Parallel with:** 03, 04

## Goal

Agents start, use and release specialised tools that run in their own pods,
possibly on other nodes: Blender, audio generation, image generation,
transcription, 3D-printer tools and video generation. Tools start on demand and
stop when idle. blender-authoring and audio-authoring become the first two pools.
DESIGN-001 8.1, D-28 and D-29.

## In this repo

- CRDs `ToolPool` and `ToolSession`.
- Operator: create or scale up instances for claims (`dedicated`, `shared`,
  `external`); ready checks; release on request, on the holder's suspend or reap,
  and on holder idle; scale to zero after `scaleToZeroAfter` with the volume kept;
  revisions drained only when the tool reports not busy; at most 4 tool sessions per
  agent session; record each pool's `tools/list` manifest in its status; label holder
  pods and write each instance's ingress policy.
- `GET /v1/tools`, `POST /v1/tools/sessions`, `DELETE /v1/tools/sessions/{id}`.
- agentd: the loopback gateway on `127.0.0.1:7700` (cached `initialize` and
  `tools/list`, attach on the first `tools/call`, progress notifications, a
  "starting, retry" error after 60 s); registration of the session's `tools:` list
  for Claude Code, Codex and opencode; the built-in `dev-env` MCP server's
  `attach_tool`.
- `agent-run tools list|attach|release|get|put`.
- The tool contract written down for image authors: MCP over streamable HTTP,
  `/readyz`, `/status` with `busy`, confined `/artifacts/`, bounded `/inputs/`,
  SIGTERM behaviour.

## In haynes-ops (GitOps PRs)

- Namespace `dev-tools` contents: the operator's Roles there, LimitRange, the
  pool ExternalSecrets.
- ToolPools for `blender` (dedicated) and `audio` (shared), pinning the images the
  existing apps run, with workspaces on `gasha01-rbd`. Only after each pool passes
  its acceptance below, remove the matching app in `apps/dev/` (keep its PVC until
  its saved files are copied over).
- New pools, one PR each, each with its image's own build: `whisper` (shared),
  `printer` (dedicated, the printer's LAN address in its egress), `video` (shared,
  the vendor's API key in a `dev-tools` Secret, egress to the vendor only), `image`
  (dedicated, CPU until plan 09 gives it a GPU claim).
- Profile defaults for `tools:` (for example haynes-quest sessions: `blender`,
  `audio`, `image`).

## Acceptance

- With no Blender pod running, an agent's first Blender tool call starts one,
  returns the result, and the pod stops 30 minutes after release. Its saved `.blend`
  is still there on the next claim.
- Two sessions each get their own Blender instance at the same time.
- Two sessions share one audio instance; its queue serves both; it scales to zero
  when both release.
- `agent-run tools get` returns a saved GLB and a WAV whose checksums match the
  tool's.
- A tool pod accepts traffic only from its holder (or claim holders) and the
  operator, mounts no ServiceAccount token, and reaches nothing outside its declared
  egress.
- A ToolPool image bump reaches an idle instance and waits for a busy one.
- Session start time does not grow with the number of registered pools.
