# Shared tool reference

A name-to-source map for the ten MCP servers in the shared session
declaration, and for six everyday read-only CLI tools. Work package
[#96](https://work.haynesnetwork.com/work_packages/96).

This page records declarations and dated historical notes. Live parity is
unaccepted. A binary in the image, a credential variable, a network rule, and
a successful call are separate facts.

## Source baseline

Read 2026-10-24 (UTC) from source. These revisions are not a claim about a
deployed image.

| Source | Revision |
| --- | --- |
| dev-env `main` | `480e0cde55c7863dbc88084316ccdb0ffe0e0ae5` |
| haynes-ops `main` | `ff285613b8f764ae40b18c8db6f4fd91559affe1` |

The v1 file
[`kubernetes/main/apps/dev/dev-env/app/resources/config/claude/mcp.json`](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev/dev-env/app/resources/config/claude/mcp.json)
and the v2 file
[`kubernetes/main/apps/dev-env-system/session-config/app/config/claude/mcp.json`](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev-env-system/session-config/app/config/claude/mcp.json)
are the same git blob, `52ee3deb3715950f94e815502f4c5127974201e4`. The blob
has ten servers. This page does not repeat URLs, header values, or secret
values from it.

The comparison in this slice is that v1 declaration against v2 profiles
`full` and `workbench`. Other profiles are outside this slice. The
[launch profile preflight](launch-profile-preflight.md) maps the existing
selectors. It grants neither a credential nor an approval.

## Legend

| Label | Meaning |
| --- | --- |
| declared/configured | The pinned source names the tool, flag, file, variable, or rule. |
| historically observed | A dated source reports a live result. The date and the limit stay with the claim. |
| not observed in this task | This task did not run the tool, the server, or a cluster command. |
| Unknown | The pinned sources do not establish the fact. |

## Proof levels

Shared levels for the ten MCP servers:

- **declared/configured.** Both pinned JSON files contain the same ten names.
  The [session-config kustomization](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev-env-system/session-config/app/kustomization.yaml)
  puts that file in ConfigMap `dev-env-config-claude`. Template mounts apply
  to every session pod
  ([`templates.go`](../internal/templates/templates.go)); the controller adds
  those mounts and then the profile mounts
  ([`pod.go`](../internal/controller/pod.go)). The pinned
  [templates](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev-env-system/templates/app/templates.yaml)
  mount that ConfigMap at `/opt/dev-env/config/claude` for `full` and for
  `workbench`. That path is the config mount, and this page does not add
  credential paths.
- **Credential environment, `full` and `workbench`.** The workbench comments
  in those templates say the profile copies `full`: `CIGAR_JOURNAL_TOKEN`
  from Secret `dev-env-cigar-secret`, `UNIFI_MCP_AUTH_TOKEN` from Secret
  `dev-env-unifi-secret` (optional), and `envFrom` Secret
  `dev-env-mcp-secret`. The same comments name `HA_MCP_SECRET_PATH`,
  `OUTLINE_API_KEY`, and `VEXA_API_KEY` as the MCP material in
  `dev-env-mcp-secret`. This task did not read a Secret.
- **historically observed, 2026-10-20, workbench only.** The
  [v2 quick start](v2-quickstart.md#what-a-workbench-session-can-do) says a
  fresh workbench session on 2.38.0 connected all ten servers for Claude,
  that Codex had the same servers configured, and that Codex's HTTP servers
  answered a direct check. [R-04](../.agents/sagas/distributed-dev-env/research/R-04-v1-capability-parity.md#status-2026-10-20)
  records the same date: P-20 closed for a workbench session, with every
  HTTP server answering `initialize` (LIVE-238b rows 14 and 15). Those row
  files are outside this repository and were not re-read. The 2026-10-08
  audit in the same file still said a read-only MCP smoke was due. The later
  closure is the workbench group claim, not a per-server log in either
  source.
- **not observed in this task.** No MCP call, grant probe, or cluster command
  was run while writing this page.
- **`full` operation: Unknown.** The 2026-10-20 proof names a workbench
  session. These sources do not record a successful MCP call from `full`.
- **Codex stdio operation: Unknown.** The 2026-10-20 sentences put the Codex
  check on the HTTP servers. They do not separately record a successful
  stdio call.

v1 Codex rendering is
[`mcp-json-to-codex-toml.sh`](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev/dev-env/app/resources/mcp-json-to-codex-toml.sh),
which
[`dev-init.sh`](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev/dev-env/app/resources/dev-init.sh)
runs. That script is the v1 boot path. v2 Codex tables are rendered by
agentd from the same JSON
([`internal/agentd/mcp.go`](../internal/agentd/mcp.go)), a port of the
script. The v2 session-config kustomization does not ship the script. The
v1 script's presence does not establish a current v2 Codex installation.
The pinned JSON has no `type: sse` entry, so the renderer's SSE skip drops
none of these ten. That is source logic, not a live registration.

`agent-run mcp` is a separate stdio server named `dev-env`
([`internal/agentrun/mcp.go`](../internal/agentrun/mcp.go)). agentd registers
it when `mcp.json` does not already name `dev-env`. The pinned file does not
name it. It is not one of the ten, and this page does not document its tools.

The [platform network policy](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev-env-system/network/app/session-platform.yaml)
names in-cluster MCP egress and skips pods labeled
`dev-env.haynesops.com/project-canary`. Its comment says the haynesnetwork
hop and the audio and Blender services admit callers on their own policies.
This page does not repeat addresses or ports. A policy name is
declared/configured reachability, not a connection observed in this task.

## MCP servers

### home-assistant

Home Assistant MCP. The declared URL is in-cluster HTTP, the host identifies
`ha-mcp`, and the path uses the placeholder `${HA_MCP_SECRET_PATH}`.

- Transport: HTTP (`type: http`).
- Provider: [`ha-mcp` HelmRelease](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/home-automation/ha-mcp/app/helmrelease.yaml).
  The platform policy admits `app.kubernetes.io/name: ha-mcp`.
- Profiles: same declaration on v1, `full`, and `workbench`. Both v2
  profiles take `HA_MCP_SECRET_PATH` from the shared MCP credential
  environment above.
- Proof: [shared levels](#proof-levels). Codex HTTP `initialize` is inside
  the 2026-10-20 group claim.

### grafana-mcp

Grafana MCP. The declared URL is in-cluster HTTP, the host identifies
`mcp-grafana`, and the path contains `/mcp`. The declaration has no header
and no environment entry.

- Transport: HTTP (`type: http`).
- Provider: [`mcp-grafana` HelmRelease](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/observability/mcp-grafana/app/helmrelease.yaml).
  The platform policy admits `app.kubernetes.io/name: mcp-grafana`.
- Profiles: same declaration on v1, `full`, and `workbench`. No credential
  variable is declared for this server. Whether the service requires one is
  Unknown.
- Proof: [shared levels](#proof-levels). Codex HTTP `initialize` is inside
  the 2026-10-20 group claim.

### mcp-unifi

UniFi MCP. The declared URL is in-cluster HTTP, the host identifies
`mcp-unifi`, and the path contains `/mcp`. The Authorization header is a
Bearer placeholder `${UNIFI_MCP_AUTH_TOKEN}`.

- Transport: HTTP (`type: http`).
- Provider: [`mcp-unifi` HelmRelease](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/observability/mcp-unifi/app/helmrelease.yaml).
  The platform policy admits `app.kubernetes.io/name: mcp-unifi`.
- Profiles: same declaration on v1, `full`, and `workbench`. Both v2
  profiles set `UNIFI_MCP_AUTH_TOKEN` from Secret `dev-env-unifi-secret`,
  and the template marks that reference optional.
- Proof: [shared levels](#proof-levels). Codex HTTP `initialize` is inside
  the 2026-10-20 group claim.

### playwright

Headless Chromium MCP for UI checks of cluster apps. The declaration runs
`playwright-mcp` with `--browser chromium --headless` and sets no `type`.
The v1 converter and agentd treat a command and no type as stdio. The agent
image installs `@playwright/mcp` at `PLAYWRIGHT_MCP_VERSION` 0.0.83
([`images/agent/Dockerfile`](../images/agent/Dockerfile)). Published package
metadata for that version maps the bin `playwright-mcp` to `cli.js`
([registry metadata for 0.0.83](https://registry.npmjs.org/@playwright/mcp/0.0.83)).

- Transport: stdio.
- Provider: the image package above. There is no cluster Service in the
  declaration. The Dockerfile comment describes a Chromium staged for the
  MCP. `PLAYWRIGHT_BROWSERS_PATH` is an environment name the Codex renderer
  forwards; this page does not repeat the cache path.
- Profiles: same declaration on v1, `full`, and `workbench`. No credential
  variable is declared for this server.
- Proof: [shared levels](#proof-levels). A successful Codex stdio call is
  Unknown.

### outline

Outline wiki MCP. The v1
[network policy](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev/dev-env/app/networkpolicy.yaml)
comment names `mcp-outline` as the wiki client. The declaration runs `uvx`
with argument `mcp-outline`. The image installs `uvx` from uv
`UV_VERSION` 0.13.0. `OUTLINE_API_KEY` is the placeholder
`${OUTLINE_API_KEY}`. `OUTLINE_API_URL` is a literal public HTTPS URL, and
this page does not repeat it. The service identity of that host is Unknown.

- Transport: stdio. The wiki URL is client configuration, not the MCP
  transport.
- Provider: package `mcp-outline`, invoked by `uvx`. The v2 platform MCP
  egress list does not name an Outline Service.
- Profiles: same declaration on v1, `full`, and `workbench`. Both v2
  profiles take `OUTLINE_API_KEY` from the shared MCP credential
  environment above.
- Proof: [shared levels](#proof-levels). A successful Codex stdio call is
  Unknown.

### vexa

MCP front for Vexa's public API. The
[`vexa` MCP manifest](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/ai/vexa/app/mcp.yaml)
says the front forwards the caller's `X-API-Key`. The declaration sets that
header from `${VEXA_API_KEY}`. The declared URL is in-cluster HTTP, the host
identifies `vexa`, and the path contains `/mcp`.

- Transport: HTTP (`type: http`).
- Provider: the manifest above. The platform policy admits the vexa gateway
  (`app.kubernetes.io/instance: vexa`, component `gateway`). The declared
  host identifies `vexa`. Whether that host is the gateway Service name is
  Unknown.
- Profiles: same declaration on v1, `full`, and `workbench`. Both v2
  profiles take `VEXA_API_KEY` from the shared MCP credential environment
  above.
- Proof: [shared levels](#proof-levels). Codex HTTP `initialize` is inside
  the 2026-10-20 group claim.

### cigar-journal

cigar-journal MCP client. The declaration is public HTTPS, the URL contains
`cigar` and `/mcp`, and it is not cluster DNS and not the in-cluster hop.
The Authorization header is a Bearer placeholder `${CIGAR_JOURNAL_TOKEN}`.
The v1 network policy comment calls this the public MCP client. The
[cigar-mcp-hop HelmRelease](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/home-automation/cigar-mcp-hop/app/helmrelease.yaml)
is a separate header-injecting hop for Home Assistant. This declaration does
not name that hop.

- Transport: HTTP (`type: http`) on public HTTPS.
- Provider: the declaration and the v1 network-policy comment. The v2
  platform MCP egress list does not name this host. The
  [web tier](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev-env-system/network/app/session-web.yaml)
  is the documented public TCP 443 rule for session pods other than profile
  `ops`. `ops` is outside this slice.
- Profiles: same declaration on v1, `full`, and `workbench`. Both v2
  profiles set `CIGAR_JOURNAL_TOKEN` from Secret `dev-env-cigar-secret`.
- Proof: [shared levels](#proof-levels). Codex HTTP `initialize` is inside
  the 2026-10-20 group claim. This page does not repeat the public host.

### haynesnetwork

Watch Companion MCP through the in-cluster hop. The
[hop HelmRelease](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/frontend/haynesnetwork-mcp-hop/app/helmrelease.yaml)
says Home Assistant and dev-env call the hop without their own credential,
and the hop injects the consumer token. The declaration has no headers. The
URL is in-cluster HTTP, the host identifies `haynesnetwork-mcp-hop`, and the
path contains `/mcp`.

- Transport: HTTP (`type: http`).
- Provider: the hop HelmRelease. The platform policy admits
  `app.kubernetes.io/name: haynesnetwork-mcp-hop`.
- Profiles: same declaration on v1, `full`, and `workbench`. No session
  credential variable is declared for this server.
- Proof: [shared levels](#proof-levels). Codex HTTP `initialize` is inside
  the 2026-10-20 group claim.

### blender

Blender authoring MCP. The declared URL is in-cluster HTTP, the host
identifies `blender-authoring` (the first instance, not
`blender-authoring-2`), and the path contains `/mcp`. The declaration has
no credential field.

- Transport: HTTP (`type: http`).
- Provider: [`blender-authoring` HelmRelease](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev/blender-authoring/app/helmrelease.yaml).
  The platform policy admits both `blender-authoring` and
  `blender-authoring-2`. The configured host selects the first.
- Profiles: same declaration on v1, `full`, and `workbench`.
- Proof: [shared levels](#proof-levels). Codex HTTP `initialize` is inside
  the 2026-10-20 group claim.

### audio

Audio authoring MCP. The declared URL is in-cluster HTTP, the host
identifies `audio-authoring`, and the path contains `/mcp`. The declaration
has no credential field. The platform rule also selects component `runtime`.

- Transport: HTTP (`type: http`).
- Provider: [`audio-authoring` HelmRelease](https://github.com/thaynes43/haynes-ops/blob/ff285613b8f764ae40b18c8db6f4fd91559affe1/kubernetes/main/apps/dev/audio-authoring/app/helmrelease.yaml).
  The platform policy admits `app.kubernetes.io/name: audio-authoring` with
  that runtime component.
- Profiles: same declaration on v1, `full`, and `workbench`.
- Proof: [shared levels](#proof-levels). Codex HTTP `initialize` is inside
  the 2026-10-20 group claim.

## CLI tools

The commands below are illustrative. They were not run against a cluster or
a service for this page. Versions are the agent image recipe at the dev-env
revision above ([`images/agent/Dockerfile`](../images/agent/Dockerfile)).
What a running image actually contains is Unknown.

### agent-run

Read-only session discovery: `list`, `show`, and `fleet`. Help text is
[`internal/agentrun/help.go`](../internal/agentrun/help.go) at this revision.
The image copies the built binary to `/usr/local/bin/agent-run`.

`list` prints sessions, newest first. `show` prints one session. `fleet`
reports counts and waiting sessions and gates nothing. `--resources` on
`fleet` adds the bound Pod spec, which is not measured use. A deployed
binary can be older than this help text; that match is Unknown.

```bash
agent-run list --repo dev-env
agent-run show <name>
agent-run fleet
```

- Proof: declared/configured in this commit's help and image recipe. Not
  observed in this task. A historical live call of these three commands is
  Unknown in the sources read here.

### opwork

OpenProject backlog reader. The image command `opwork` executes
[`opwork.py`](../images/agent/tools/openproject/opwork.py) through
[`opwork.sh`](../images/agent/tools/opwork.sh). `search` lists work items.
`show` is marked read-only in its command help. Search flags in that source
are `--status`, `--type`, `--parent`, `--source-key`, `--owner`, `--all`,
`--page`, and `--limit`. Show flags are `--history`, `--relations`, and
`--full`.

```bash
opwork search "text"
opwork show <id>
```

- Prerequisite: the platform policy admits the OpenProject instance. The
  Dockerfile says that without that path the tool reports the tracker
  unavailable. Before each API call, `request` calls `token()` in the pinned
  client [`agent-api.py`](../images/agent/tools/openproject/agent-api.py),
  which reads a dedicated token-only helper with `kubectl exec`. Successful
  helper access from `full` and `workbench` is Unknown; these sources have
  no dated evidence of that access. This task did not call the tracker or
  the helper.
- Proof: declared/configured. Not observed in this task. A historical
  successful search or show is Unknown in the sources read here.

### git

Read-only inspection. The image installs Debian `git` from apt and does not
pin the package version, so the image version is Unknown. On the writing
host, Git 2.39.5 accepted `git status`, `git log -1`, `git diff`, and
`git show HEAD`. That host check is not the image package.

```bash
git status
git log -1
git diff
git show HEAD
```

- Proof: the binary is declared/configured by the image recipe. The image
  version is Unknown. Not observed in this task as an image command.

### gh

GitHub read-only discovery. The image pins `GH_VERSION` 2.102.0. Command
spelling was checked against cli/cli tag `v2.102.0`:
[`repo view`](https://github.com/cli/cli/blob/v2.102.0/pkg/cmd/repo/view/view.go)
is `view [<repository>]`, and
[`pr list`](https://github.com/cli/cli/blob/v2.102.0/pkg/cmd/pr/list/list.go)
is `list`, with `--state` values `open`, `closed`, `merged`, and `all`, and
a `-R, --repo` override.

```bash
gh repo view thaynes43/dev-env
gh pr list --repo thaynes43/dev-env --state open
```

- Proof: declared/configured at the image pin, with flags from that upstream
  tag. Not observed in this task as an image command. A deployed binary
  match is Unknown.

### kubectl

Read-only API discovery. The image pins `KUBECTL_VERSION` `v1.37.1`.
[`kubectl get`](https://github.com/kubernetes/kubernetes/blob/v1.37.1/staging/src/k8s.io/kubectl/pkg/cmd/get/get.go)
displays resources, and `-A` is `--all-namespaces`.
[`kubectl api-resources`](https://github.com/kubernetes/kubernetes/blob/v1.37.1/staging/src/k8s.io/kubectl/pkg/cmd/apiresources/apiresources.go)
prints supported API resources.

```bash
kubectl get pods -A
kubectl api-resources
```

- Historically observed, 2026-10-20, workbench: the quick start says
  `kubectl get pods -A` worked in that fresh session. The same paragraph
  says `kubectl get secrets` was refused. Keep that refusal as a denial.
- Proof: command spelling is declared/configured at v1.37.1. The pods
  example is historically observed on that date for workbench. Not observed
  in this task. A `full` success for these commands is Unknown here. A
  deployed binary match is Unknown.

### flux

Read-only Flux discovery. The image pins `FLUX_VERSION` 2.9.6.
[`flux get`](https://github.com/fluxcd/flux2/blob/v2.9.6/cmd/flux/get.go)
gets resource status, and `-A` is `--all-namespaces`.
[`flux get kustomizations`](https://github.com/fluxcd/flux2/blob/v2.9.6/cmd/flux/get_kustomization.go)
has aliases `ks` and `kustomization`.

```bash
flux get ks -A
```

- Historically observed, 2026-10-20, workbench: the quick start says
  `flux get ks -A` worked in that fresh session.
- Proof: command spelling is declared/configured at 2.9.6. That example is
  historically observed on that date for workbench. Not observed in this
  task. A `full` success is Unknown here. A deployed binary match is
  Unknown.

## When a tool is unavailable

Record the exact failure and the resolved profile. `agent-run show <name> -o json`
is the existing read for the stored profile. A missing stored profile is not
proof of `workbench` or of `full`; the launch profile preflight says to
resolve an omitted profile from the caller's route and the deployed template
default.

Consult the [v2 quick start](v2-quickstart.md) and the
[launch profile preflight](launch-profile-preflight.md), and ask the owner
when the source does not answer. Preserve a denial. The 2026-10-20 workbench
note that `kubectl get secrets` was refused is one such denial. This page
does not add another route, a credential, or a network setup.
