#!/usr/bin/env bash
# Smoke test for the agent image, ported from haynes-ops dev-env-build.yml (v1) and
# extended for what v2 bakes in. Run by .github/workflows/ci.yml on PRs and by
# publish-agent.yml before it pushes, so the image that ships is the image that passed.
#
#   images/agent/smoke-test.sh <image> [expected-commit]
#
# It runs the image as the pod does: uid 1000, read-only root, tmpfs /tmp and /home/dev
# (the session volume's stand-in), no network. It needs Docker and runs small commands
# only (about 2 CPUs for a few seconds); it is for CI, not for the dev-env pod, where the
# image should not be built or pulled at all.
set -euo pipefail

image="${1:?usage: smoke-test.sh <image> [expected-commit]}"
expect_commit="${2:-}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

run() {
  docker run --rm --read-only --network none \
    --cpus 2 --memory 2g \
    --tmpfs /tmp:rw,exec,size=256m \
    --tmpfs /home/dev:rw,uid=1000,gid=1000,size=256m \
    --user 1000:1000 \
    "$@"
}

echo "::group::default entrypoint: tini then agentd"
# No operator in a bare `docker run`, so `agentd version` is the check that the ENTRYPOINT
# chain starts: tini execs agentd, agentd prints its stamped identity and exits 0. Then
# agent-run, which the entrypoint does not reach, must carry the same stamp.
out="$(run "${image}" version)"
echo "${out}"
case "${out}" in agentd\ *) ;; *) echo "FAIL: expected 'agentd <version> ...'" >&2; exit 1 ;; esac
if [ -n "${expect_commit}" ]; then
  case "${out}" in *"${expect_commit:0:12}"*) ;; *) echo "FAIL: agentd does not report commit ${expect_commit:0:12}" >&2; exit 1 ;; esac
fi
out="$(run --entrypoint /usr/local/bin/agent-run "${image}" version)"
echo "${out}"
case "${out}" in agent-run\ *) ;; *) echo "FAIL: expected 'agent-run <version> ...'" >&2; exit 1 ;; esac
if [ -n "${expect_commit}" ]; then
  case "${out}" in *"${expect_commit:0:12}"*) ;; *) echo "FAIL: agent-run does not report commit ${expect_commit:0:12}" >&2; exit 1 ;; esac
fi
echo "::endgroup::"

echo "::group::toolchain as the runtime user"
run --entrypoint /bin/bash "${image}" -euc '
  test "$(id -u)" = 1000
  test "$HOME" = /home/dev
  test "$GOTMPDIR" = /home/dev/.cache/go-tmp
  # agentd creates ~/.codex at boot; codex warns when CODEX_HOME is missing.
  mkdir -p "$CODEX_HOME"
  # PID 1 is only tini under the real ENTRYPOINT; here we check the binary and the
  # tools the Go code calls by name (agentd: git, tmux, claude; the gh wrapper: gh).
  tini --version
  agentd version
  agentd help | grep -q "project-refresh"
  # Private catalog preparation is explicitly off in this credential-free
  # image; disabled command dispatch reads no catalog or provider state.
  if agentd project-refresh >/tmp/project-refresh.out 2>/tmp/project-refresh.err; then
    echo "disabled project-refresh unexpectedly succeeded" >&2
    exit 1
  fi
  test ! -s /tmp/project-refresh.out
  grep -q "private project bootstrap is disabled" /tmp/project-refresh.err
  test ! -d "$HOME/.agentd"
  agent-run version
  # declare-activity is agent-run by another name (D-66); its help needs no API.
  declare-activity help | grep -q "Usage: agent-run declare-activity"
  tmux -V
  git --version
  gh --version
  claude --version
  codex --version
  kubectl version --client=true
  kubectl-cnpg version
  kubectl plugin list 2>/dev/null | grep -q kubectl-cnpg
  flux --version
  helm version --short
  node --version
  python3 --version
  uv --version
  pnpm --version
  sops --disable-version-check --version
  yq --version
  talosctl version --client >/dev/null
  omnictl --help >/dev/null
  pve --help >/dev/null
  hw-ssh list >/dev/null
  opwork --help >/dev/null
  opwork pending | grep -q "no pending records"
  # Baked in the image, not downloaded at boot, and installed outside CODEX_HOME so the
  # session volume cannot hide it.
  case "$(readlink -f "$(command -v codex)")" in /opt/dev-env/codex/*) ;; *) echo "codex is not under /opt/dev-env/codex" >&2; exit 1 ;; esac
  # The browsers agentd links into the session volume (D-40).
  ls /opt/dev-env/ms-playwright | grep -q chromium
  # What this image deliberately lacks.
  test ! -e /usr/local/bin/code-server
  test ! -e /opt/dev-env/github-app-token.sh
  # /etc/codex is a ConfigMap mount (D-49): the image must not ship a requirements file.
  test ! -e /etc/codex/requirements.toml
  # The license texts the image must carry (#55): this repo license and the third-party
  # list, the Go module and standard library texts, and one directory per downloaded tool.
  test -s /usr/share/doc/dev-env/LICENSE
  test -s /usr/share/doc/dev-env/THIRD_PARTY.md
  test -s /usr/share/licenses/dev-env/go-stdlib/LICENSE
  test -d /usr/share/licenses/dev-env/go-modules
  for tool in kubectl flux gh helm kustomize sops age yq task tofu restic talosctl omnictl \
              tini kubectl-cnpg codex uv; do
    n="$(find "/usr/share/licenses/$tool" -type f -size +0 2>/dev/null | wc -l)"
    test "$n" -gt 0 || { echo "no license text for $tool in /usr/share/licenses/$tool" >&2; exit 1; }
  done
  # Both licenses of omnictl (BSL 1.1 and its MPL-2.0 client library) and uv (MIT or Apache).
  test -s /usr/share/licenses/omnictl/LICENSE
  test -s /usr/share/licenses/omnictl/LICENSE.client-MPL-2.0
  test -s /usr/share/licenses/uv/LICENSE-MIT
  test -s /usr/share/licenses/uv/LICENSE-APACHE
  # The shell can write where the pod lets it.
  touch "$HOME/.writable" /tmp/.writable
'
echo "::endgroup::"

echo "::group::cursor acp helpers"
# Same read-only, network-none, empty-HOME run as the toolchain group.
# Checker exit 0 is not protocol or task success. This group does not launch Cursor.
run --entrypoint /bin/bash "${image}" -euc '
  helper=/usr/local/lib/dev-env-cursor-acp
  test -x $helper/cursor-acp-session.py
  test -x $helper/cursor-acp-smoke-check.py
  case $helper in
    /home/*|/opt/dev-env/scripts/*) echo "ACP helpers are not image-owned" >&2; exit 1 ;;
  esac
  test ! -e /opt/dev-env/scripts/cursor-acp-session.py
  test ! -e $HOME/cursor-acp-session.py
  test ! -e /usr/local/bin/cursor-agent
  python3 -c "import importlib.util, pathlib; root=pathlib.Path(\"/usr/local/lib/dev-env-cursor-acp\"); session=root/\"cursor-acp-session.py\"; checker=root/\"cursor-acp-smoke-check.py\"; spec=importlib.util.spec_from_file_location(\"cursor_acp_session\", session); mod=importlib.util.module_from_spec(spec); spec.loader.exec_module(mod); assert \"reject_once\" in mod.PERMISSION_KINDS; other=importlib.util.spec_from_file_location(\"cursor_acp_smoke_check\", checker); checker_mod=importlib.util.module_from_spec(other); other.loader.exec_module(checker_mod); assert checker_mod.__file__ == str(checker)"
  python3 $helper/cursor-acp-session.py --help > /tmp/acp-session-help.txt
  grep -q -- "--client" /tmp/acp-session-help.txt
  code=0
  python3 $helper/cursor-acp-smoke-check.py - </dev/null > /tmp/acp-smoke-empty.txt || code=$?
  test "$code" = 2
  grep -q empty-input /tmp/acp-smoke-empty.txt
  code=0
  python3 $helper/cursor-acp-smoke-check.py --help > /tmp/acp-smoke-help.txt || code=$?
  test "$code" = 2
'
echo "::endgroup::"

echo "::group::codex launch policy from the GitOps requirements (#482)"
# Session pods get /etc/codex/requirements.toml and the base of ~/.codex/config.toml
# from haynes-ops; testdata/codex-policy holds test copies. `codex debug prompt-input`
# prints the instructions a new thread's model would get, and they name the effective
# sandbox and approval policy. It needs no login and calls no model.
policy="${here}/testdata/codex-policy"
# effective FLAGS...: "<sandbox> <approval>" for a new thread started with those root
# flags, from a fresh CODEX_HOME holding the test config. Runs inside the image.
codex_effective='
effective() {
  rm -rf "$CODEX_HOME"; mkdir -p "$CODEX_HOME"
  cp /opt/codex-policy/config.toml "$CODEX_HOME/config.toml"
  timeout 30 codex "$@" debug prompt-input >/tmp/prompt.json || echo "codex $* exited $?" >&2
  sandbox="$(grep -o "sandbox_mode. is .[a-z-]*" /tmp/prompt.json | sed "s/.*[^a-z-]//" | sort -u | paste -sd, -)"
  approval="$(grep -o "Approval policy is currently [a-z-]*" /tmp/prompt.json | sed "s/.* //" | sort -u | paste -sd, -)"
  echo "${sandbox:-none} ${approval:-none}"
}
'
run -v "${policy}/requirements.toml:/etc/codex/requirements.toml:ro" \
  -v "${policy}/config.toml:/opt/codex-policy/config.toml:ro" \
  --entrypoint /bin/bash "${image}" -euc "${codex_effective}"'
  expect() {
    want="$1"; shift
    got="$(effective "$@")"
    echo "codex $* -> ${got}"
    test "${got}" = "${want}" || { echo "FAIL: codex $* gives \"${got}\", want \"${want}\"" >&2; exit 1; }
  }
  mkdir -p "$CODEX_HOME"
  codex --version
  # A thread that asks for no sandbox or approval, and what the managed routes pass.
  expect "danger-full-access never"
  expect "danger-full-access never" --dangerously-bypass-approvals-and-sandbox
  # A request for approvals is refused; approval stays never.
  expect "danger-full-access never" -a on-request
  # KNOWN TRAP (#482): a workspace-write request is refused, and Codex falls back to
  # read-only, not to the configured danger-full-access. bwrap cannot run in a session
  # pod, so that thread fails every shell and MCP call. These two lines pin the current
  # behaviour. If a Codex bump changes it they fail: update them and the
  # owner-interactive Codex recipe in docs/workflow-guide.md together.
  expect "read-only never" -s workspace-write
  expect "read-only never" --approve-for-me
'
echo "::endgroup::"

echo "::group::tini reaps"
# An orphan whose parent exits is re-parented to PID 1. Under tini it is reaped, so no
# defunct process is left behind (v1's PID 1 left thousands).
run --entrypoint /usr/local/bin/tini "${image}" -- /bin/bash -euc '
  ( sleep 0.2 & disown ) ; sleep 1
  if ps -eo stat= | grep -q "^Z"; then echo "FAIL: zombie process" >&2; ps -eo pid,ppid,stat,comm >&2; exit 1; fi
  ps -o comm= -p 1 | grep -qx tini
'
echo "::endgroup::"

size="$(docker image inspect --format '{{.Size}}' "${image}")"
echo "image size: ${size} bytes ($((size / 1024 / 1024)) MiB)"
