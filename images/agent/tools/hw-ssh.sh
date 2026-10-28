#!/usr/bin/env bash
# hw-ssh — SSH to the non-Talos hardware from inside dev-env (2026-09-17, backlog 14
# "SSH tier"). One ed25519 key for all of it, materialised by dev-init from the pod
# env (HW_SSH_PRIVATE_KEY_B64 ← 1Password `dev-env`) to ~/.ssh/dev-env-hw.
#
#   Proxmox nodes  → user `dev-env`, key-only, sudo allowlist (see runbook) — root-
#                    equivalent in practice via `sudo qm`, same tier Tom ruled for the
#                    API operator token (2026-09-09, Q-1).
#   HaynesTower    → root (Unraid has no other SSH user); key in /boot/config/ssh/root.pubkeys.
#   PiKVM          → root (2026-10-03, Tom). Console + ATX on every bare-metal master via
#                    the TESmart KVM, so root-equivalent on the masters. Read-only rootfs:
#                    `rw` only for a config deploy, `ro` straight after.
#
# usage: hw-ssh list
#        hw-ssh <host> [command...]         # no command = interactive shell
#        hw-ssh pve-all <command...>        # same command on all five PVE nodes
#        hw-ssh --raw <user@fqdn> [cmd...]  # anything else the CNP allows on :22
#
# Key (first match wins):
#   1. HW_SSH_KEY, if set.
#   2. A leased key: an approved hw-ssh credential grant (D-89) has the keeper install
#      the key at $XDG_RUNTIME_DIR/dev-env-hwssh/<grant>/key, with a manifest.json.
#      An expired lease is deleted and ignored. This is the ONE shared key for every
#      host above, not a per-grant credential: removing the lease at expiry, release or
#      session end is neither containment (anything in the pod can copy the key while it
#      is installed) nor revocation (no cryptographic expiry; revoking means rotating the
#      key on every host, out of band).
#   3. ~/.ssh/dev-env-hw, where dev-init puts the key in a profile that carries it.
#
# Rules (runbook .agents/runbooks/proxmox-access.md): read-first; `declare-activity`
# before anything disruptive; no node reboots or Unraid array stop/start from here; no
# PiKVM ATX power/reset, Wake-on-LAN or console keystrokes without Tom's go-ahead.
set -uo pipefail

KEY="${HW_SSH_KEY:-}"
LEASES="${HW_SSH_LEASE_DIR:-${XDG_RUNTIME_DIR:-/dev/shm/run-$(id -u)}/dev-env-hwssh}"
KNOWN="$HOME/.ssh/known_hosts_hw"
DOMAIN=haynesnetwork
PVE_NODES=(haynesintelligence twin-top twin-bottom pve04 pve-filet02)

die() { printf 'hw-ssh: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '/^# usage:/,/^#$/{/^#$/d;s/^# \{0,1\}//;p;}' "$0"; exit "${1:-0}"; }

# leased_key prints the key path of the live lease that expires last, deleting expired
# leases it finds. Nothing is printed when there is none.
leased_key() {
  local d m exp now best="" best_at=0 at
  now=$(date -u +%s)
  for d in "$LEASES"/grant-*; do
    m="$d/manifest.json"
    [[ -d "$d" && ! -L "$d" && -f "$m" && -f "$d/key" && ! -L "$d/key" ]] || continue
    exp=$(sed -n 's/.*"expires":"\([^"]*\)".*/\1/p' "$m")
    at=$(date -u -d "$exp" +%s 2>/dev/null) || continue
    if (( at > now )); then
      (( at > best_at )) && { best="$d/key"; best_at=$at; }
    else
      rm -f "$d/key" "$d/manifest.json" 2>/dev/null; rmdir "$d" 2>/dev/null || true
    fi
  done
  [[ -z "$best" ]] || { printf '%s %s\n' "$best" "$best_at"; }
}

resolve_key() {
  [[ -z "$KEY" ]] || return 0
  local lease
  if lease=$(leased_key) && [[ -n "$lease" ]]; then
    KEY="${lease% *}"
    printf 'hw-ssh: using the leased shared key (until %s). It is not revocable and not contained: it stays valid on every host after the lease is removed.\n' \
      "$(date -u -d "@${lease##* }" +%Y-%m-%dT%H:%M:%SZ)" >&2
  else
    KEY="$HOME/.ssh/dev-env-hw"
  fi
}

target() {  # host alias → user@fqdn
  case "$1" in
    haynesintelligence|twin-top|twin-bottom|pve04|pve-filet02) printf 'dev-env@%s.%s' "$1" "$DOMAIN" ;;
    haynestower|unraid|tower) printf 'root@haynestower.%s' "$DOMAIN" ;;
    pikvm|kvm) printf 'root@pikvm.%s' "$DOMAIN" ;;
    *) return 1 ;;
  esac
}

run() {  # user@fqdn [cmd...]
  local dest="$1"; shift
  resolve_key
  [[ -r "$KEY" ]] || die "no key at $KEY — no live hw-ssh lease (ask for an hw-ssh credential grant) and HW_SSH_PRIVATE_KEY_B64 not in the pod env (1Password dev-env field + pod bounce; see runbook)"
  exec_or_run ssh -i "$KEY" -o IdentitiesOnly=yes -o BatchMode="$([[ $# -eq 0 ]] && echo no || echo yes)" \
    -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$KNOWN" -o ConnectTimeout=10 \
    -o LogLevel=ERROR "$dest" "$@"
}
exec_or_run() { if [[ -n "${HW_SSH_NOEXEC:-}" ]]; then "$@"; else exec "$@"; fi; }

[[ $# -ge 1 ]] || usage 1
case "$1" in
  -h|--help|help) usage ;;
  list)
    for n in "${PVE_NODES[@]}"; do printf '%-20s %s\n' "$n" "$(target "$n")"; done
    printf '%-20s %s\n' haynestower "$(target haynestower)"
    printf '%-20s %s\n' pikvm "$(target pikvm)"
    ;;
  pve-all)
    shift; [[ $# -ge 1 ]] || die "pve-all needs a command"
    rc=0
    for n in "${PVE_NODES[@]}"; do
      printf '===== %s =====\n' "$n"
      HW_SSH_NOEXEC=1 run "$(target "$n")" "$@" || { rc=$?; printf 'hw-ssh: %s exited %s\n' "$n" "$rc" >&2; }
    done
    exit "$rc"
    ;;
  --raw)
    shift; [[ $# -ge 1 ]] || die "--raw needs user@host"
    dest="$1"; shift; run "$dest" "$@"
    ;;
  *)
    dest="$(target "$1")" || die "unknown host '$1' — try: hw-ssh list"
    shift; run "$dest" "$@"
    ;;
esac
