# Third-party software in the published images

This repo's own code is MIT-licensed ([LICENSE](../LICENSE)). The two images it
publishes also carry other people's software, and each component stays under its own
license. This page lists them. The license that ships with each component is the one
that governs; this list is a guide to it.

Keep this page in step with the Dockerfiles: a PR that adds or removes a bundled
component updates the table here in the same PR. Versions are the pins in each
Dockerfile, so they are not repeated here.

## Operator image: `ghcr.io/thaynes43/dev-env-operator`

Built from [`operator/Dockerfile`](operator/Dockerfile). Everything in it is open
source.

| Component | License | Notes |
|---|---|---|
| `dev-env-operator`, `dev-env-keeper` | MIT | This repo. |
| Go modules linked into the binaries | Apache-2.0, BSD-3-Clause, MIT, BSD-2-Clause, ISC | Kubernetes client libraries, controller-runtime and their dependencies, pinned in [`go.mod`](../go.mod) and [`go.sum`](../go.sum). On 2026-10-07 the four binaries linked 63 modules: 38 Apache-2.0, 16 BSD-3-Clause, 7 MIT, 1 BSD-2-Clause and 1 ISC. |
| Go standard library and runtime | BSD-3-Clause | [go.dev/LICENSE](https://go.dev/LICENSE) |
| Base image `gcr.io/distroless/static:nonroot` | Apache-2.0 (the [distroless](https://github.com/GoogleContainerTools/distroless) project) | Its Debian files (`ca-certificates`, `tzdata`, `base-files`, `netbase`) keep their licenses in `/usr/share/doc/*/copyright` inside the image. |

## Agent image: `ghcr.io/thaynes43/dev-env` (tag line 2.x)

Built from [`agent/Dockerfile`](agent/Dockerfile) (KICKOFF B5, D-53) and published from
release tags. **The agent image as a whole is not open source**, because it bundles
Claude Code.

### Proprietary

| Component | License | Notes |
|---|---|---|
| Claude Code (`@anthropic-ai/claude-code`, from npm) | Proprietary: "© Anthropic PBC. All rights reserved. Use is subject to Anthropic's [Commercial Terms of Service](https://www.anthropic.com/legal/commercial-terms)." | Not open source, and not covered by this repo's MIT license. Running it needs your own Anthropic account, under Anthropic's terms. |

### Source-available

| Component | License | Notes |
|---|---|---|
| `omnictl` ([siderolabs/omni](https://github.com/siderolabs/omni)) | Business Source License 1.1 (its entry point) and MPL-2.0 (its client library) | The BSL is not an open-source license. It allows copying and redistribution; its Additional Use Grant allows non-production use, including personal use in a home lab. Read the license at the pinned version before other use. |

### Open source

| Component | License | Upstream |
|---|---|---|
| `agentd`, `agent-run` | MIT | This repo |
| `pve`, `hw-ssh` | MIT | This repo (copies of the v1 scripts from [thaynes43/haynes-ops](https://github.com/thaynes43/haynes-ops), also MIT) |
| `tini` | MIT | [krallin/tini](https://github.com/krallin/tini) |
| Codex CLI | Apache-2.0 | [openai/codex](https://github.com/openai/codex) |
| `kubectl` | Apache-2.0 | [kubernetes/kubernetes](https://github.com/kubernetes/kubernetes) |
| `flux` | Apache-2.0 | [fluxcd/flux2](https://github.com/fluxcd/flux2) |
| `gh` | MIT | [cli/cli](https://github.com/cli/cli) |
| `helm` | Apache-2.0 | [helm/helm](https://github.com/helm/helm) |
| `kustomize` | Apache-2.0 | [kubernetes-sigs/kustomize](https://github.com/kubernetes-sigs/kustomize) |
| `kubectl-cnpg` | Apache-2.0 | [cloudnative-pg/cloudnative-pg](https://github.com/cloudnative-pg/cloudnative-pg) |
| `sops` | MPL-2.0 | [getsops/sops](https://github.com/getsops/sops) |
| `age` | BSD-3-Clause | [FiloSottile/age](https://github.com/FiloSottile/age) |
| `yq` | MIT | [mikefarah/yq](https://github.com/mikefarah/yq) |
| `task` | MIT | [go-task/task](https://github.com/go-task/task) |
| `tofu` (OpenTofu) | MPL-2.0 | [opentofu/opentofu](https://github.com/opentofu/opentofu) |
| `restic` | BSD-2-Clause | [restic/restic](https://github.com/restic/restic) |
| `talosctl` | MPL-2.0 | [siderolabs/talos](https://github.com/siderolabs/talos) |
| `uv` | Apache-2.0 or MIT | [astral-sh/uv](https://github.com/astral-sh/uv) |
| `tsx` | MIT | [privatenumber/tsx](https://github.com/privatenumber/tsx) |
| `pnpm` | MIT | [pnpm/pnpm](https://github.com/pnpm/pnpm) |
| Playwright MCP and Playwright | Apache-2.0 | [microsoft/playwright-mcp](https://github.com/microsoft/playwright-mcp), [microsoft/playwright](https://github.com/microsoft/playwright) |
| Chromium, as built by Playwright | BSD-3-Clause, plus the licenses of the code it bundles | [chromium.org](https://www.chromium.org/) |
| Node.js, npm and Yarn 1, from the `node:24-slim` base | Node.js: MIT, plus the licenses of its bundled dependencies; npm: Artistic-2.0; Yarn: BSD-2-Clause | [nodejs/node](https://github.com/nodejs/node), [npm/cli](https://github.com/npm/cli), [yarnpkg/yarn](https://github.com/yarnpkg/yarn) |
| Debian base and the `apt` packages (git, openssh-client, curl, jq, tmux, ripgrep, rsync, make, build-essential, python3 and the rest of the Dockerfile's list, plus Chromium's system libraries) | Each package's own license (GPL, LGPL, BSD, MIT and others) | Each package's terms are in `/usr/share/doc/<package>/copyright` inside the image. Source for every package, the GPL ones included, is in the Debian archive ([sources.debian.org](https://sources.debian.org/), [snapshot.debian.org](https://snapshot.debian.org/)). |

The source for the MPL-2.0 components is in their upstream repositories, at the tag
the Dockerfile pins.

The npm and Debian packages keep their license files inside the image. The release
binaries downloaded by the Dockerfile do not yet: their archives' license files are
dropped when the binary is extracted. Tracked in
[#55](https://github.com/thaynes43/dev-env/issues/55).
