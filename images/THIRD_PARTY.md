# Third-party software in the published images

This repo's own code is MIT-licensed ([LICENSE](../LICENSE)), except files with an
explicit different license, including the diagnostic marker listed below. Its
published images also carry other people's software, and each component stays under its own
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
| Go modules linked into the two binaries | Apache-2.0, BSD-3-Clause, MIT, BSD-2-Clause, ISC | Kubernetes client libraries, controller-runtime and their dependencies, pinned in [`go.mod`](../go.mod) and [`go.sum`](../go.sum). The operator links 63 third-party modules and the keeper 64; their combined inventory has 64 modules. The YAML modules retain both Apache-2.0 and MIT file notices. Each binary's linked license texts are collected during its image build. |
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
| `agentd`, `agent-run` | MIT | This repo, plus the Go standard library and runtime (BSD-3-Clause, [go.dev/LICENSE](https://go.dev/LICENSE)). `agentd` links `github.com/pelletier/go-toml/v2` under MIT; `agent-run` links no third-party module. Linked module license texts are retained by the image build. |
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

## Codex login helper: `ghcr.io/thaynes43/dev-env-codex-login`

Built from [`codex-login/Dockerfile`](codex-login/Dockerfile). This bounded helper
contains the keeper control binary and pinned native Codex CLI for a fresh login
ceremony. It contains no Claude Code or other agent-image tools.

| Component | License | Notes |
|---|---|---|
| `dev-env-keeper` and its linked Go modules/runtime | MIT plus the module/runtime licenses listed for the operator image | This repo's helper control and the same keeper dependency inventory. |
| Codex CLI | Apache-2.0 | [openai/codex](https://github.com/openai/codex); the pinned upstream `LICENSE` and `NOTICE` are retained under `/usr/share/licenses/codex/`. |
| Node.js, npm and Yarn from `node:24-slim` | The component licenses listed for the agent base above | The helper uses the same pinned Node base. |
| Debian base, `ca-certificates`, `curl` and their dependencies | Each package's own license | Copyright/license files remain under `/usr/share/doc/<package>/copyright`; Debian source archives are linked above. |

## Git wait diagnostic: `ghcr.io/thaynes43/dev-env:git-wait-sha-*`

Built from [`git-wait/Dockerfile`](git-wait/Dockerfile). This fixture inherits the
agent image and its component licenses above. It adds a separate modified Git
binary for a bounded storage diagnostic. The installed `/usr/bin/git` remains the
base image's Debian Git.

| Component | License | Notes |
|---|---|---|
| `git-control` and modified `git-wait` | GPL-2.0, with the upstream file notices retained | Built from the pinned Debian Git source and its patch set. Git's [COPYING at the pinned upstream revision](https://github.com/git/git/blob/cc7d11c16782041a6bb73e2fb56417b7d4c6d186/COPYING) supplies the license. The diagnostic patch is included with the source. |
| Native marker, reader library and marker test (`marker.c`, `marker.h`, `marker-test.c`) | GPL-2.0-only | These files carry explicit SPDX notices. The reader is `/opt/dev-env/trials/libgit-wait-reader.so`; the test is `/opt/dev-env/trials/marker-test`. They are not covered by this repo's MIT license. |

The fixture retains the full corresponding source in
`/opt/dev-env/trials/git-wait-source.tar.gz`: the complete patched Git tree,
marker sources, diagnostic patch, build recipe, exact build options and
provenance. The recipe is also available under `/opt/dev-env/trials/recipe/`.
The upstream license text is `/usr/share/licenses/git-wait/COPYING`, and this
inventory is `/usr/share/doc/dev-env/THIRD_PARTY.md`. Hosted image checks verify
the retained license and source inventory alongside the binary and recipe hashes.

## Where the license texts are in the images

Every image carries the same two paths for this repo's own material:

* `/usr/share/doc/dev-env/LICENSE` and `/usr/share/doc/dev-env/THIRD_PARTY.md`: this
  repo's license and this page.
* `/usr/share/licenses/dev-env/go-modules/`: the license texts of the third-party Go
  modules linked into this repo's binaries, collected by
  [go-licenses](https://github.com/google/go-licenses) at the version pinned in each
  Dockerfile. `/usr/share/licenses/dev-env/go-stdlib/LICENSE` is the Go standard
  library's license.

The agent image adds one directory per downloaded tool under
`/usr/share/licenses/<tool>/`: `kubectl`, `flux`, `gh`, `helm`, `kustomize`, `sops`,
`age`, `yq`, `task`, `tofu`, `restic`, `talosctl`, `omnictl`, `tini`, `kubectl-cnpg`,
`codex` and `uv`. Where a release archive holds the license file (`gh`, `helm`, `age`,
`task`, `tofu`, `kubectl-cnpg`), that file is the one kept. Where it does not (the
other tools), the Dockerfile downloads the license file from the upstream git tag that
matches the pinned version, so the text cannot drift from the binary. `omnictl` has two
files, the BSL 1.1 text (`LICENSE`) and its client library's MPL-2.0 text
(`LICENSE.client-MPL-2.0`); `uv` has `LICENSE-MIT` and `LICENSE-APACHE`;
`kubectl-cnpg` also keeps the `licenses/` tree of the modules it links. The npm
packages and the Debian packages keep their own license files, as above.

The CI image jobs check that these paths exist, so a Dockerfile change that drops one
fails the PR.
