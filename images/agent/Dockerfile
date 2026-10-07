# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# ghcr.io/thaynes43/dev-env: the dev-env v2 agent image, tag line 2.x (KICKOFF B5).
#
# One image for every session pod in `dev-agents` (DESIGN-001 3.6, D-44): `tini` is
# PID 1, `agentd` (built from this repo) is its child, and the agent CLIs, the ops
# toolchain and the SDKs sit under them. It is a copy of haynes-ops
# `scripts/dev-env/Dockerfile` (v1, tag line 0.x) with these changes:
#   * added: tini, agentd and agent-run (stage `go`), the Codex standalone and
#     kubectl-cnpg (v1 downloaded both at boot onto the PVC), pve and hw-ssh
#     (copies of v1's scripts, images/agent/tools/);
#   * removed: code-server (v1's PID 1; nothing in a session pod serves a browser),
#     github-app-token.sh (only the keeper mints tokens, D-11);
#   * changed: the ENTRYPOINT, and the Codex install path (below).
# Everything the pod mounts or sets at runtime (config, scripts, credentials, the
# session) comes from haynes-ops; the image holds binaries only.
#
# Unlike v1 this file is built from this repo's root (`docker build -f
# images/agent/Dockerfile --build-arg COMMIT=$(git rev-parse HEAD) .`). It is about
# 3.2 GB on disk (measured in CI, 2026-10-06): build it in CI only, never in the
# dev-env pod (CLAUDE.md, "No CPU burners").
# .github/workflows/ci.yml builds and smoke-tests it on every PR that touches
# images/agent/**, and publish-agent.yml pushes and signs it from a release tag.
#
# Debian/glibc (kubectl/claude glibc-linked); amd64-only (all Talos nodes x86_64).

# agentd and agent-run, built the way CI and developers build them. The build context
# has no .git (.dockerignore), so COMMIT is always passed and VERSION on a release
# build; without them the binaries report "dev" (internal/version). Only the Go
# sources are copied, so an edit to the tools below does not rebuild them.
FROM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS go
WORKDIR /src
# go-licenses collects the license texts of the modules linked into the binaries below
# (the issue #55 work). It goes in before the sources, so a Go change does not rebuild it.
# renovate: datasource=github-releases depName=google/go-licenses
ARG GO_LICENSES_VERSION=v2.0.1
RUN CGO_ENABLED=0 go install "github.com/google/go-licenses/v2@${GO_LICENSES_VERSION}"
COPY go.mod go.sum ./
RUN go mod download
COPY Makefile ./
COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
ARG COMMIT=
ARG VERSION=
# `make build` also checks that agent-run is static (D-06). It builds the operator and
# the keeper too, which this image drops; they cost seconds.
RUN make build COMMIT="${COMMIT}" VERSION="${VERSION}"
# The texts of the third-party modules agentd and agent-run link, plus the Go
# standard library's own license. `--ignore` skips this repo's own packages (MIT, the
# LICENSE copied beside them in the final stage). CGO_ENABLED=0 matches the Makefile,
# so go-licenses sees the package set the binaries were built from. It refuses an
# existing save path and may write none when no third-party module is linked, hence the
# late mkdir.
RUN mkdir -p /out/licenses/go-stdlib \
 && CGO_ENABLED=0 go-licenses save ./cmd/agentd ./cmd/agent-run \
      --ignore github.com/thaynes43/dev-env --save_path=/out/licenses/go-modules \
 && mkdir -p /out/licenses/go-modules \
 && cp "$(go env GOROOT)/LICENSE" /out/licenses/go-stdlib/LICENSE

# Pinned by digest, as every base image here is; Renovate (docker:pinDigests) keeps it fresh.
FROM node:24-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20

# ╔══════════════════════════════════════════════════════════════════════════════╗
# ║ DO NOT put a version number in a comment BETWEEN a `# renovate:` annotation  ║
# ║ and its `ARG`. RENOVATE SILENTLY REWRITES IT.                               ║
# ╚══════════════════════════════════════════════════════════════════════════════╝
# The Dockerfile customManager (.github/renovate.json5) matches from the
# `renovate:` annotation, THROUGH any intervening full comment lines, to the ARG —
# and the replacement rewrites every occurrence of the old version inside that whole
# match, comments included. So explanatory prose placed under the annotation gets
# its numbers edited on each bump, turning accurate history into confident fiction.
#
# It had done exactly that twice by 2026-09-13: the claude-code note's "2.1.263
# (2026-09-01): Fable 5.1" became "2.1.270 (2026-09-01)", re-attributing 2.1.263's
# fixes to a release that had not shipped on that date; and the pnpm note's claim
# about haynesnetwork's `packageManager` pin was rewritten every single bump into a
# version haynesnetwork has never used (it pins 11.9.0). Neither shows up in review —
# the diff looks like the intended bump.
#
# Prose goes ABOVE the annotation, where it is outside the match and safe. The codex
# block was already written that way, which is why its text survived untouched.

# renovate: datasource=github-releases depName=kubernetes/kubernetes
ARG KUBECTL_VERSION=v1.37.1
# renovate: datasource=github-releases depName=fluxcd/flux2
ARG FLUX_VERSION=2.9.6
# renovate: datasource=github-releases depName=cli/cli
ARG GH_VERSION=2.102.0
# renovate: datasource=github-releases depName=helm/helm
ARG HELM_VERSION=4.3.0
# renovate: datasource=github-releases depName=kubernetes-sigs/kustomize extractVersion=^kustomize/v(?<version>.+)$
ARG KUSTOMIZE_VERSION=5.8.2
# renovate: datasource=github-releases depName=getsops/sops
ARG SOPS_VERSION=3.13.3
# renovate: datasource=github-releases depName=FiloSottile/age extractVersion=^v(?<version>.+)$
ARG AGE_VERSION=1.3.2
# renovate: datasource=github-releases depName=mikefarah/yq extractVersion=^v(?<version>.+)$
ARG YQ_VERSION=4.54.1
# renovate: datasource=github-releases depName=go-task/task extractVersion=^v(?<version>.+)$
ARG TASK_VERSION=3.54.0
# renovate: datasource=github-releases depName=opentofu/opentofu extractVersion=^v(?<version>.+)$
ARG OPENTOFU_VERSION=1.13.1
# renovate: datasource=github-releases depName=restic/restic extractVersion=^v(?<version>.+)$
ARG RESTIC_VERSION=0.19.1
# talosctl — read-only node triage (dmesg/logs/get) via apid; MUST track the cluster's
# Talos version (nodes run v1.13.3). Same style as kubectl: the `v` lives in the ARG.
# renovate: datasource=github-releases depName=siderolabs/talos
ARG TALOSCTL_VERSION=v1.14.2
# omnictl — read-only Omni machine/cluster status against SaaS Omni (the self-hosted
# instance was decommissioned 2026-09-22); tracks upstream siderolabs/omni releases,
# which stay API-compatible with nearby omnictl versions. renovate: datasource=github-releases depName=siderolabs/omni
ARG OMNICTL_VERSION=v1.12.3
# renovate: datasource=github-releases depName=astral-sh/uv
ARG UV_VERSION=0.12.23
# tini is PID 1 in every session pod (D-07): it reaps, which v1's PID 1 (code-server)
# never did (2,970 zombies on 2026-09-28). The static build needs no libc. Its release
# publishes a .sha256sum per asset; the install step checks it.
# renovate: datasource=github-releases depName=krallin/tini
ARG TINI_VERSION=0.19.0
# kubectl-cnpg: CNPG instance surgery (`kubectl cnpg destroy`, backlog 05). v1 pinned
# it in dev-init.sh as CNPG_PLUGIN_VERSION and downloaded it at every boot; the image
# carries it now.
# renovate: datasource=github-releases depName=cloudnative-pg/cloudnative-pg
ARG CNPG_PLUGIN_VERSION=1.30.1
# ── claude-code ── NOTE: this prose sits ABOVE the `# renovate:` line ON PURPOSE.
# See the "version numbers in comments" warning at the top of this ARG block.
# 2.1.251 (2026-08-29): catching up 35 releases of drift. 2.1.217 was pinned by
# hand on 2026-07-22 and never moved again because NOTHING was watching this ARG
# — the `# renovate:` line was decoration until customManagers.json5 grew a
# Dockerfile manager the same day. The drift was not cosmetic: claude resolves
# the `opus` MODEL ALIAS client-side from a table baked into the binary, so
# 2.1.217 served claude-opus-4-8 for every `--model opus` session (agent-run's
# picker, and the dev-env-ops curation lane after #2663) long after Opus 5 shipped.
# 2.1.263 (2026-09-01): Fable 5.1. `claude-fable-5-1` needs claude-code >=2.1.255 —
# an older CLI REJECTS the id outright ("Claude Code does not support this model";
# no fallback, the session never starts), so that floor is load-bearing for the
# dev-env default model, not cosmetic. 2.1.257 repointed the `fable` alias to 5.1
# and made --effort lift a model's default-effort hold per session; 2.1.263 fixed
# a 2.1.255 launch regression and a remote-session "user messages must have
# non-empty content" failure after a re-sent permission approval.
# 2.1.270 (2026-09-13): patch over 2.1.269, which shipped `claude plugin eval` and
# `/output-style`. It fixes a 2.1.269 regression where read-only git commands in
# Bash started asking for permission after a session had been running a while —
# directly relevant here, where agent sessions are long-lived by design.
# 2.1.280 (2026-09-23): Claude Opus 5.5. `claude-opus-5-5` (launched 2026-09-22) needs
# claude-code >=2.1.280 — 2.1.270 answers "unrecognized_model" with no fallback — so
# this floor is load-bearing for the agent-run picker row and for the Opus 5.5
# subagent default in config/claude/CLAUDE.md. 2.1.280 also repoints the `opus`
# alias to 5.5 (another reason the pod pins full ids, never aliases).
# 2.1.284 (2026-09-28): Claude Sonnet 5.5. `claude-sonnet-5-5` needs claude-code
# >=2.1.284, and this version also repoints the `sonnet` alias to it. That floor is
# load-bearing: Sonnet 5.5 is the subagent tier for routine coding and tests in
# config/claude/CLAUDE.md, and it has an agent-run picker row.
# renovate: datasource=npm depName=@anthropic-ai/claude-code
ARG CLAUDE_CODE_VERSION=2.1.292
# Codex is the managed STANDALONE, not the npm package — only the standalone can run
# `codex remote-control`. v1 installed it at boot onto the PVC (dev-init.sh); here the
# image installs it, under /opt/dev-env/codex (never under CODEX_HOME, which is the
# session volume at runtime). This pins that standalone version; @openai/codex npm
# releases track the same numbers as the rust-v* GitHub releases the standalone
# installer pulls, so the npm datasource still tracks it.
# 0.153.4 (2026-09-04): GPT-6-Astra. Codex serves its model catalog PER CLIENT
# VERSION, so on 0.151.0 `gpt-6-astra` does not exist at all — not in the
# agent-run picker, not in the remote-control phone picker. 0.153.1 shipped the
# model hidden; 0.153.4 is the first release that lists it in the picker and makes
# it the bundled default. (It was installed by hand on the PVC on 2026-09-06 and
# landed here on 2026-09-08, so the by-hand install no longer reverts at a roll.)
# 0.154.0 (2026-09-13): adds `max`/`ultra` reasoning-effort values and experimental
# worktree support. It also REMOVES the deprecated `codex mcp-server` entry point —
# harmless here, because this pod reaches MCP the other way round: mcp-json-to-codex-
# toml.sh renders claude's mcp.json into `[mcp_servers.*]` and codex is the CLIENT.
# Keep /etc/codex/requirements.toml's allowed_sandbox_modes including "read-only"
# (#2829) — codex refuses to load the config without it and remote-control never
# starts, silently, at every boot.
# 0.156.1 (2026-09-23): GPT-6 Sol + GPT-6 Luna enter the model catalog (a hotfix over
# 0.156.0, which shipped without them). `gpt-6-sol` is the Codex subagent default in
# config/claude/CLAUDE.md and, like gpt-6-astra before it, does not exist to an older
# client: 0.154.0's cache never lists it. This catalog also delists gpt-5.4-mini;
# agent-run's codex fallback rows were refreshed to match (a slug mismatch WARNs).
# 0.158.0 (2026-09-28, via 0.157.0): 0.157.0 auto-starts a background server for
# eligible interactive sessions. 0.158.0 turns on terminal input approval by default
# for elevated commands. Both need checking against this pod's single remote-control
# daemon and its approval-`never` requirements.toml after the roll that ships them.
# 0.160.0 (2026-10-01, via 0.159.1): GPT-6.1 Sol enters the model catalog as the
# bundled default. `gpt-6.1-sol` is the Codex subagent default in config/claude/
# CLAUDE.md since 2026-10-03 and does not exist to an older client: 0.158.0's cache
# never lists it.
# renovate: datasource=npm depName=@openai/codex
ARG CODEX_VERSION=0.160.1
# renovate: datasource=npm depName=tsx
ARG TSX_VERSION=4.23.15
# ── pnpm ── prose ABOVE the annotation on purpose (see the warning above).
# This global pnpm is only the FALLBACK. Since pnpm 11, a repo whose package.json
# pins `packageManager: pnpm@<x>` gets that exact version downloaded and run
# (`pmOnFail` defaults to `download`), so haynesnetwork, cigar-journal and
# haynes-quest each run their own pin in the pod, and their CI pins its own via
# pnpm/action-setup. All three lockfiles are format 9.0, which the 12 line reads
# unchanged. Only an UNPINNED checkout runs this global: its first re-resolving
# install may re-key cyclic peer variants once (a one-time lockfile diff), and
# `--frozen-lockfile false` is gone — use `--no-frozen-lockfile`. The old "stay on
# the 11 line" rule (haynesnetwork's `engines.pnpm: ">=11"`) is satisfied by any
# newer major too; moved to the 12 line 2026-09-24 (#3037, Tom: upgrade everything).
# (Until 2026-09-13 this comment claimed haynesnetwork pinned packageManager to
# whatever this ARG happened to be. It never did — Renovate had been rewriting the
# number inside the comment on every bump, manufacturing a fact that was never true.)
# renovate: datasource=npm depName=pnpm
ARG PNPM_VERSION=12.9.1
# renovate: datasource=npm depName=@playwright/mcp
ARG PLAYWRIGHT_MCP_VERSION=0.0.83

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Base packages: dev essentials + python3 (hass-sandbox/makejinja) + tmux (agent
# sessions live in tmux) + ripgrep (agents search with rg) + build-essential
# (native npm modules, C extensions).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git openssh-client jq openssl bash gettext-base \
      tar gzip unzip bzip2 xz-utils \
      tmux ripgrep less procps rsync file make build-essential \
      python3 python3-venv python3-pip \
 && rm -rf /var/lib/apt/lists/*

# kubectl (checksum-verified) — in-cluster via kubernetes.default.svc + the
# operator-tier SA (saga Decision #1).
RUN curl -fsSLo /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
 && curl -fsSLo /tmp/kubectl.sha256 \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl.sha256" \
 && echo "$(cat /tmp/kubectl.sha256)  /usr/local/bin/kubectl" | sha256sum -c - \
 && chmod +x /usr/local/bin/kubectl && rm -f /tmp/kubectl.sha256 \
 && mkdir -p /usr/share/licenses/kubectl \
 && curl -fsSLo /usr/share/licenses/kubectl/LICENSE \
      "https://raw.githubusercontent.com/kubernetes/kubernetes/${KUBECTL_VERSION}/LICENSE"

# flux CLI.
RUN curl -fsSLo /tmp/flux.tar.gz \
      "https://github.com/fluxcd/flux2/releases/download/v${FLUX_VERSION}/flux_${FLUX_VERSION}_linux_amd64.tar.gz" \
 && tar -xzf /tmp/flux.tar.gz -C /usr/local/bin flux \
 && chmod +x /usr/local/bin/flux && rm -f /tmp/flux.tar.gz \
 && mkdir -p /usr/share/licenses/flux \
 && curl -fsSLo /usr/share/licenses/flux/LICENSE \
      "https://raw.githubusercontent.com/fluxcd/flux2/v${FLUX_VERSION}/LICENSE"

# gh CLI.
RUN curl -fsSLo /tmp/gh.tar.gz \
      "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_amd64.tar.gz" \
 && tar -xzf /tmp/gh.tar.gz -C /tmp \
 && install -m 0755 "/tmp/gh_${GH_VERSION}_linux_amd64/bin/gh" /usr/local/bin/gh \
 && install -D -m 0644 "/tmp/gh_${GH_VERSION}_linux_amd64/LICENSE" /usr/share/licenses/gh/LICENSE \
 && rm -rf /tmp/gh.tar.gz "/tmp/gh_${GH_VERSION}_linux_amd64"

# helm.
RUN curl -fsSLo /tmp/helm.tar.gz \
      "https://get.helm.sh/helm-v${HELM_VERSION}-linux-amd64.tar.gz" \
 && tar -xzf /tmp/helm.tar.gz -C /tmp \
 && install -m 0755 /tmp/linux-amd64/helm /usr/local/bin/helm \
 && install -D -m 0644 /tmp/linux-amd64/LICENSE /usr/share/licenses/helm/LICENSE \
 && rm -rf /tmp/helm.tar.gz /tmp/linux-amd64

# kustomize (release tags are kustomize/vX.Y.Z — hence the URL-encoded path).
RUN curl -fsSLo /tmp/kustomize.tar.gz \
      "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv${KUSTOMIZE_VERSION}/kustomize_v${KUSTOMIZE_VERSION}_linux_amd64.tar.gz" \
 && tar -xzf /tmp/kustomize.tar.gz -C /usr/local/bin kustomize \
 && chmod +x /usr/local/bin/kustomize && rm -f /tmp/kustomize.tar.gz \
 && mkdir -p /usr/share/licenses/kustomize \
 && curl -fsSLo /usr/share/licenses/kustomize/LICENSE \
      "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/kustomize/v${KUSTOMIZE_VERSION}/LICENSE"

# sops + age (SOPS_AGE_KEY_FILE workflows, same as the Mac direnv setup).
RUN curl -fsSLo /usr/local/bin/sops \
      "https://github.com/getsops/sops/releases/download/v${SOPS_VERSION}/sops-v${SOPS_VERSION}.linux.amd64" \
 && chmod +x /usr/local/bin/sops \
 && mkdir -p /usr/share/licenses/sops \
 && curl -fsSLo /usr/share/licenses/sops/LICENSE \
      "https://raw.githubusercontent.com/getsops/sops/v${SOPS_VERSION}/LICENSE" \
 && curl -fsSLo /tmp/age.tar.gz \
      "https://github.com/FiloSottile/age/releases/download/v${AGE_VERSION}/age-v${AGE_VERSION}-linux-amd64.tar.gz" \
 && tar -xzf /tmp/age.tar.gz -C /tmp \
 && install -m 0755 /tmp/age/age /usr/local/bin/age \
 && install -m 0755 /tmp/age/age-keygen /usr/local/bin/age-keygen \
 && install -D -m 0644 /tmp/age/LICENSE /usr/share/licenses/age/LICENSE \
 && rm -rf /tmp/age.tar.gz /tmp/age

# yq.
RUN curl -fsSLo /usr/local/bin/yq \
      "https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/yq_linux_amd64" \
 && chmod +x /usr/local/bin/yq \
 && mkdir -p /usr/share/licenses/yq \
 && curl -fsSLo /usr/share/licenses/yq/LICENSE \
      "https://raw.githubusercontent.com/mikefarah/yq/v${YQ_VERSION}/LICENSE"

# go-task (the repo's Taskfile runner).
RUN curl -fsSLo /tmp/task.tar.gz \
      "https://github.com/go-task/task/releases/download/v${TASK_VERSION}/task_linux_amd64.tar.gz" \
 && tar -xzf /tmp/task.tar.gz -C /usr/local/bin task \
 && chmod +x /usr/local/bin/task \
 && mkdir -p /usr/share/licenses/task \
 && tar -xzf /tmp/task.tar.gz -C /usr/share/licenses/task LICENSE \
 && rm -f /tmp/task.tar.gz

# opentofu.
RUN curl -fsSLo /tmp/tofu.zip \
      "https://github.com/opentofu/opentofu/releases/download/v${OPENTOFU_VERSION}/tofu_${OPENTOFU_VERSION}_linux_amd64.zip" \
 && unzip -q /tmp/tofu.zip -d /tmp/tofu \
 && install -m 0755 /tmp/tofu/tofu /usr/local/bin/tofu \
 && install -D -m 0644 /tmp/tofu/LICENSE /usr/share/licenses/tofu/LICENSE \
 && rm -rf /tmp/tofu.zip /tmp/tofu

# restic (volsync debugging per the runbooks).
RUN curl -fsSLo /tmp/restic.bz2 \
      "https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_amd64.bz2" \
 && bunzip2 /tmp/restic.bz2 \
 && install -m 0755 /tmp/restic /usr/local/bin/restic && rm -f /tmp/restic \
 && mkdir -p /usr/share/licenses/restic \
 && curl -fsSLo /usr/share/licenses/restic/LICENSE \
      "https://raw.githubusercontent.com/restic/restic/v${RESTIC_VERSION}/LICENSE"

# talosctl + omnictl (single static binaries, siderolabs releases) — READ-ONLY node
# triage (saga backlog 10). talosctl reads dmesg/logs/machine resources directly from
# each node's apid (tcp/50000); omnictl reads Omni machine/cluster status. Read-only
# is enforced by the CREDENTIAL role at runtime (os:reader talosconfig, Omni Reader
# SA), NOT the binary — the tools themselves are full-featured, but the pod only ever
# holds reader creds (PR B mounts them).
RUN curl -fsSLo /usr/local/bin/talosctl \
      "https://github.com/siderolabs/talos/releases/download/${TALOSCTL_VERSION}/talosctl-linux-amd64" \
 && chmod +x /usr/local/bin/talosctl \
 && curl -fsSLo /usr/local/bin/omnictl \
      "https://github.com/siderolabs/omni/releases/download/${OMNICTL_VERSION}/omnictl-linux-amd64" \
 && chmod +x /usr/local/bin/omnictl \
 && mkdir -p /usr/share/licenses/talosctl /usr/share/licenses/omnictl \
 && curl -fsSLo /usr/share/licenses/talosctl/LICENSE \
      "https://raw.githubusercontent.com/siderolabs/talos/${TALOSCTL_VERSION}/LICENSE" \
 && curl -fsSLo /usr/share/licenses/omnictl/LICENSE \
      "https://raw.githubusercontent.com/siderolabs/omni/${OMNICTL_VERSION}/LICENSE" \
 && curl -fsSLo /usr/share/licenses/omnictl/LICENSE.client-MPL-2.0 \
      "https://raw.githubusercontent.com/siderolabs/omni/${OMNICTL_VERSION}/client/LICENSE"

# tini (checksum-verified): PID 1, the one process that reaps (D-07).
RUN curl -fsSLo /usr/local/bin/tini \
      "https://github.com/krallin/tini/releases/download/v${TINI_VERSION}/tini-static-amd64" \
 && curl -fsSLo /tmp/tini.sha256sum \
      "https://github.com/krallin/tini/releases/download/v${TINI_VERSION}/tini-static-amd64.sha256sum" \
 && echo "$(cut -d' ' -f1 /tmp/tini.sha256sum)  /usr/local/bin/tini" | sha256sum -c - \
 && chmod +x /usr/local/bin/tini && rm -f /tmp/tini.sha256sum \
 && mkdir -p /usr/share/licenses/tini \
 && curl -fsSLo /usr/share/licenses/tini/LICENSE \
      "https://raw.githubusercontent.com/krallin/tini/v${TINI_VERSION}/LICENSE"

# kubectl-cnpg: the CloudNativePG plugin, found by `kubectl cnpg ...` on PATH.
# The archive ships its own LICENSE and a `licenses/` tree with the texts of the modules it links.
RUN curl -fsSLo /tmp/kubectl-cnpg.tar.gz \
      "https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v${CNPG_PLUGIN_VERSION}/kubectl-cnpg_${CNPG_PLUGIN_VERSION}_linux_x86_64.tar.gz" \
 && tar -xzf /tmp/kubectl-cnpg.tar.gz -C /usr/local/bin kubectl-cnpg \
 && chmod +x /usr/local/bin/kubectl-cnpg \
 && mkdir -p /usr/share/licenses/kubectl-cnpg \
 && tar -xzf /tmp/kubectl-cnpg.tar.gz -C /usr/share/licenses/kubectl-cnpg LICENSE licenses \
 && rm -f /tmp/kubectl-cnpg.tar.gz \
 && kubectl-cnpg version

# Codex standalone, from the release's own installer (it checks the archive digest).
# CODEX_HOME makes the install tree /opt/dev-env/codex/packages/standalone, which is
# world-readable and read-only to the dev user at runtime; CODEX_INSTALL_DIR puts the
# `codex` launcher (a symlink into that tree) on PATH. HOME points at a scratch dir
# because the installer appends a PATH line to the shell profile of whoever runs it.
# No self-update: the version is this pin, and a new one is a new image (Tom,
# 2026-09-10; the updater strands zombies, openai/codex#34721).
RUN curl -fsSLo /tmp/codex-install.sh \
      "https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}/install.sh" \
 && mkdir /tmp/codex-home \
 && HOME=/tmp/codex-home CODEX_HOME=/opt/dev-env/codex CODEX_INSTALL_DIR=/usr/local/bin \
      CODEX_NON_INTERACTIVE=1 sh /tmp/codex-install.sh --release "${CODEX_VERSION}" \
 && chmod -R a+rX /opt/dev-env/codex \
 && rm -rf /tmp/codex-install.sh /tmp/codex-home \
 && mkdir -p /usr/share/licenses/codex \
 && curl -fsSLo /usr/share/licenses/codex/LICENSE \
      "https://raw.githubusercontent.com/openai/codex/rust-v${CODEX_VERSION}/LICENSE" \
 && curl -fsSLo /usr/share/licenses/codex/NOTICE \
      "https://raw.githubusercontent.com/openai/codex/rust-v${CODEX_VERSION}/NOTICE" \
 && codex --version

# uv — modern python env/dependency management (hass-sandbox, makejinja venvs).
RUN curl -fsSLo /tmp/uv.tar.gz \
      "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-x86_64-unknown-linux-gnu.tar.gz" \
 && tar -xzf /tmp/uv.tar.gz -C /tmp \
 && install -m 0755 /tmp/uv-x86_64-unknown-linux-gnu/uv /usr/local/bin/uv \
 && install -m 0755 /tmp/uv-x86_64-unknown-linux-gnu/uvx /usr/local/bin/uvx \
 && mkdir -p /usr/share/licenses/uv \
 && curl -fsSLo /usr/share/licenses/uv/LICENSE-MIT \
      "https://raw.githubusercontent.com/astral-sh/uv/${UV_VERSION}/LICENSE-MIT" \
 && curl -fsSLo /usr/share/licenses/uv/LICENSE-APACHE \
      "https://raw.githubusercontent.com/astral-sh/uv/${UV_VERSION}/LICENSE-APACHE" \
 && rm -rf /tmp/uv.tar.gz /tmp/uv-x86_64-unknown-linux-gnu

# Agent CLIs + TS tooling (haynesnetwork runs tsx).
RUN npm install -g \
      "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
      "tsx@${TSX_VERSION}" \
      "pnpm@${PNPM_VERSION}" \
 && npm cache clean --force

# Playwright MCP + a baked chromium (agents do UI/UX testing of the cluster's own
# apps — saga plan 03). Browsers + system deps install at BUILD time to a shared
# path so the runtime pod needs no CDN egress and no root. --with-deps pulls the
# chromium system libraries via apt.
# Install the browser via the MCP's OWN playwright-core CLI so the chromium build
# revision matches what the MCP expects (a bare `npx playwright install` can pull a
# mismatched playwright version).
#
# Browsers are STAGED at /opt/dev-env/ms-playwright, not used in place: at runtime
# PLAYWRIGHT_BROWSERS_PATH points at the PVC (~/.cache/ms-playwright) so a repo whose
# own playwright pins a DIFFERENT chromium revision (haynesnetwork: @playwright/test
# 1.61.x) can `playwright install` it alongside — impossible under
# readOnlyRootFilesystem if the path lived in an image layer. agentd links the
# staged revisions into that cache at boot (D-40, AGENTD_PLAYWRIGHT_STAGING), so the MCP
# works with zero downloads.
RUN npm install -g "@playwright/mcp@${PLAYWRIGHT_MCP_VERSION}" \
 && PLAYWRIGHT_BROWSERS_PATH=/opt/dev-env/ms-playwright \
      node "$(npm root -g)/@playwright/mcp/node_modules/playwright-core/cli.js" \
      install --with-deps chromium \
 && chmod -R a+rX /opt/dev-env/ms-playwright \
 && npm cache clean --force && rm -rf /var/lib/apt/lists/*

# pve and hw-ssh: copies of v1's scripts (haynes-ops dev-env app resources), byte for
# byte, so a fix lands in both places until v1 is retired. They read their credentials
# from the environment and from ~/.ssh/dev-env-hw, which agentd writes; Q-07 moves
# both credentials to short-lived grants (plan 07). Neither is a secret holder.
COPY images/agent/tools/pve.sh /usr/local/bin/pve
COPY images/agent/tools/hw-ssh.sh /usr/local/bin/hw-ssh
RUN chmod 0755 /usr/local/bin/pve /usr/local/bin/hw-ssh

# The binaries of this repo come last, so a Go change rebuilds only these layers.
# agentd is the one thing tini starts; agent-run is the v2 CLI (D-50).
COPY --from=go /src/bin/agentd /src/bin/agent-run /usr/local/bin/

# This repo's license, the third-party list and the texts of the Go modules the binaries
# above link (#55). Beside them: /usr/share/licenses/<tool>/ for each downloaded tool
# (above), /usr/share/doc/<package>/copyright for the Debian packages and each npm
# package's own LICENSE. images/agent/smoke-test.sh checks these paths.
COPY LICENSE images/THIRD_PARTY.md /usr/share/doc/dev-env/
COPY --from=go /out/licenses/ /usr/share/licenses/dev-env/

# /opt/dev-env/config and /opt/dev-env/scripts are ConfigMap mounts (D-49); the
# browsers staged above and the Codex tree live in the same directory.
# Non-root: rename the base image's uid-1000 'node' user to 'dev' with /home/dev —
# the session volume mounts over /home/dev at runtime (agentd renders its config).
RUN usermod -l dev -d /home/dev node && groupmod -n dev node \
 && mkdir -p /home/dev && chown dev:dev /home/dev

USER dev
WORKDIR /home/dev
# CODEX_VERSION is informational (the pin that built this image). The operator sets
# HOME, XDG_RUNTIME_DIR and AGENTD_*; the templates set CLAUDE_CONFIG_DIR, CODEX_HOME
# and DEV_ENV_CLAUDE_MODEL (D-44), and they are repeated here so a bare
# `docker run` behaves the same. ~/.local/bin leads PATH so agentd's `gh` wrapper
# (token injection) shadows /usr/local/bin/gh in every context.
ENV HOME=/home/dev \
    CLAUDE_CONFIG_DIR=/home/dev/.claude \
    CODEX_HOME=/home/dev/.codex \
    CODEX_VERSION=${CODEX_VERSION} \
    PLAYWRIGHT_BROWSERS_PATH=/home/dev/.cache/ms-playwright \
    PATH=/home/dev/.local/bin:/usr/local/bin:/usr/bin:/bin

# Provenance. `source` also links the GHCR package to this repo. Declared last so a
# new commit or version changes only this metadata layer, never a tool layer.
ARG COMMIT=
ARG VERSION=
LABEL org.opencontainers.image.source="https://github.com/thaynes43/dev-env" \
      org.opencontainers.image.title="dev-env agent" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.version="${VERSION}"

# tini reaps, and forwards the pod's SIGTERM to agentd alone: no `-g`, because agentd
# forwards it to the CLI and waits for the CLI's own shutdown (D-42), and a group-wide
# signal would hit the CLI first. The operator's container sets no command or args
# (D-44), so the pod runs `agentd run`, the daemon, from CMD; `docker run <image>
# version` prints agentd's version instead.
ENTRYPOINT ["/usr/local/bin/tini", "--", "/usr/local/bin/agentd"]
CMD ["run"]
