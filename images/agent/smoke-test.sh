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
# chain starts: tini execs agentd, agentd prints its stamped identity and exits 0.
out="$(run "${image}" version)"
echo "${out}"
case "${out}" in agentd\ *) ;; *) echo "FAIL: expected 'agentd <version> ...'" >&2; exit 1 ;; esac
if [ -n "${expect_commit}" ]; then
  case "${out}" in *"${expect_commit:0:12}"*) ;; *) echo "FAIL: agentd does not report commit ${expect_commit:0:12}" >&2; exit 1 ;; esac
fi
echo "::endgroup::"

echo "::group::toolchain as the runtime user"
run --entrypoint /bin/bash "${image}" -euc '
  test "$(id -u)" = 1000
  test "$HOME" = /home/dev
  # PID 1 is only tini under the real ENTRYPOINT; here we check the binary and the
  # tools the Go code calls by name (agentd: git, tmux, claude; the gh wrapper: gh).
  tini --version
  agentd version
  agent-run version
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
  sops --version
  yq --version
  talosctl version --client >/dev/null
  omnictl --help >/dev/null
  pve --help >/dev/null
  hw-ssh list >/dev/null
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
  # The shell can write where the pod lets it.
  touch "$HOME/.writable" /tmp/.writable
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
