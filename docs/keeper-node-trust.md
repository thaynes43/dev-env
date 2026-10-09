# Keeper SSH CA: owner node-trust quick start

Prepared 2026-10-09, America/New_York. This is the next owner provisioning step
after saving the fresh CA in `HaynesKube/dev-env`. It is a reviewable recipe,
not evidence that trust is installed. The reset work order assigns node trust
to Tom unless he explicitly delegates it. No agent has changed the nodes.

This uses the existing Unix `dev-env` account on each of the five PVE nodes.
It appends a restricted CA trust entry and preserves the existing v1 SSH key.
It changes no sudo rule and needs no sshd reload/restart. Keeper and broker
Proxmox minting stay disabled after this step.

The read-only audit found OpenSSH 9.2 on all five nodes, the expected account,
`.ssh` mode 0700 and `authorized_keys` mode 0600, owned by `dev-env`; one existing
plain key and no CA entry; `/usr/bin/pvesh` and the required noninteractive sudo
inspection scope. The commands below recheck the relevant filesystem facts.

## 1. Use the public key already created

Use the retained owner-local file `$HOME/dev-env-keeper-ca/ssh_ca.pub`. If it is
missing, save the existing item's `SSH_CA_PUBLIC_KEY` field to that file locally.
Do not generate another CA, transfer its private key, or paste key values into
chat. Keep an authenticated root session open on the node while doing its step.
Do one node at a time and avoid concurrent SSH-key provisioning.

From the trusted owner computer, substitute that node's existing SSH destination
for `<PVE-NODE>`:

```bash
scp "$HOME/dev-env-keeper-ca/ssh_ca.pub" \
  root@<PVE-NODE>:/root/dev-env-keeper-ssh-ca.pub
```

Use normal host-key verification. The node needs only this public file.

## 2. Append the restricted trust entry

Run this block **as root on that node**. It refuses unexpected paths, ownership,
permissions and conflicting entries. It saves a rollback backup and verifies
the original key file's bytes are still present after the atomic replacement.

```bash
(
  set -euo pipefail
  umask 077

  ca_file=/root/dev-env-keeper-ssh-ca.pub
  ssh_dir=/home/dev-env/.ssh
  authorized=$ssh_dir/authorized_keys
  staged=''
  trap '[ -z "$staged" ] || rm -f -- "$staged"' EXIT

  test "$(id -u)" -eq 0
  test "$(getent passwd dev-env | cut -d: -f6)" = /home/dev-env
  test -d "$ssh_dir" && test ! -L "$ssh_dir"
  test -f "$authorized" && test ! -L "$authorized"
  test -f "$ca_file" && test ! -L "$ca_file"
  test "$(stat -c '%a %U:%G' "$ssh_dir")" = '700 dev-env:dev-env'
  test "$(stat -c '%a %U:%G' "$authorized")" = '600 dev-env:dev-env'

  /usr/sbin/sshd -t
  runuser -u dev-env -- sudo -n -l /usr/bin/pvesh get \
    /access/users/dev-env@pve/token --output-format json >/dev/null

  ca_key=$(awk '
    NF {
      if (seen++ || $1 != "ssh-ed25519" || NF < 2) exit 1
      print $1 " " $2
    }
    END { if (seen != 1) exit 1 }
  ' "$ca_file")
  ssh-keygen -lf "$ca_file" >/dev/null
  keeper_line='restrict,cert-authority,principals="dev-env-keeper-proxmox-minter" '"$ca_key"' dev-env-keeper-proxmox-minter-ca'

  if grep -Fqx -- "$keeper_line" "$authorized"; then
    printf 'Keeper CA entry already installed.\n'
    exit 0
  fi
  if grep -Fq -- "$ca_key" "$authorized" ||
     grep -Fq -- 'dev-env-keeper-proxmox-minter-ca' "$authorized"; then
    printf 'Conflicting CA/key entry; stop and inspect locally.\n' >&2
    exit 1
  fi

  backup=$(mktemp /root/dev-env-authorized-keys.before-keeper-ca.XXXXXX)
  cp -p -- "$authorized" "$backup"
  staged=$(mktemp "$ssh_dir/.authorized_keys.keeper-ca.XXXXXX")
  cat -- "$backup" > "$staged"
  printf '\n%s\n' "$keeper_line" >> "$staged"
  chown --reference="$authorized" "$staged"
  chmod --reference="$authorized" "$staged"

  cmp -s -- "$authorized" "$backup"
  mv -f -- "$staged" "$authorized"
  staged=''
  head -c "$(stat -c %s "$backup")" "$authorized" | cmp -s - "$backup"
  tail -n 1 "$authorized" | grep -Fqx -- "$keeper_line"
  test "$(stat -c '%a %U:%G' "$authorized")" = '600 dev-env:dev-env'
  printf 'Keeper CA entry installed; original keys preserved.\n'
  printf 'Rollback backup: %s\n' "$backup"
)
```

Record which nodes succeeded and retain each printed backup path locally.
Repeat sections 1–2 for the other nodes. Do not rerun the old hardware account
provisioning script: it overwrites `authorized_keys`.

## 3. Verify existing access before closing root sessions

From v1's existing pod, use each configured hardware alias:

```bash
hw-ssh <NODE-ALIAS> id -un
hw-ssh <NODE-ALIAS> sudo -n /usr/bin/pvesh version >/dev/null
```

The first command should report `dev-env`; the second should succeed. These are
read-only checks. If access changes, keep the root session and use the guarded
rollback below. Report node completion and check results only; no key values.

The new authorized-key entry is shaped like this:

```text
restrict,cert-authority,principals="dev-env-keeper-proxmox-minter" ssh-ed25519 <CA-PUBLIC-KEY-BLOB> dev-env-keeper-proxmox-minter-ca
```

OpenSSH checks the CA signature, principal, certificate validity and present
critical `force-command`. `restrict` independently disables PTY, forwarding
and user SSH rc. The keeper's signer always includes one bounded per-operation
forced command and no certificate extensions. This relies on exclusive keeper
custody of the CA: the authorized-key entry itself does not require every
possible CA-issued certificate to contain `force-command`.
[OpenSSH authorized-key rules](https://man.openbsd.org/sshd.8),
[OpenSSH 9.2 certificate format](https://github.com/openssh/openssh-portable/blob/V_9_2_P1/PROTOCOL.certkeys).

Do not add a separate `command="..."` option to this entry: it must match the
certificate command exactly and would conflict with the current per-operation
commands. No account-wide `TrustedUserCAKeys`/principal change is required.
`AuthorizedPrincipalsFile` does not govern a CA trusted through this key file.
[OpenSSH principal configuration](https://man.openbsd.org/sshd_config.5).

## Guarded rollback

In the retained root session, substitute the backup path printed for that node.
This restores the original only when the current file exactly matches the
expected installation. Later edits cause it to stop rather than overwrite them.

```bash
(
  set -euo pipefail
  umask 077
  authorized=/home/dev-env/.ssh/authorized_keys
  backup='<PRINTED-BACKUP-PATH>'
  ca_file=/root/dev-env-keeper-ssh-ca.pub
  test "$(id -u)" -eq 0
  test -f "$authorized" && test ! -L "$authorized"
  test -f "$backup" && test ! -L "$backup"
  test -f "$ca_file" && test ! -L "$ca_file"
  ca_key=$(awk 'NF { print $1 " " $2 }' "$ca_file")
  keeper_line='restrict,cert-authority,principals="dev-env-keeper-proxmox-minter" '"$ca_key"' dev-env-keeper-proxmox-minter-ca'
  staged=$(mktemp /home/dev-env/.ssh/.authorized_keys.rollback.XXXXXX)
  trap 'rm -f -- "$staged"' EXIT
  cp -p -- "$backup" "$staged"
  printf '\n%s\n' "$keeper_line" >> "$staged"
  if ! cmp -s -- "$staged" "$authorized"; then
    printf 'Later edits detected; stop rather than overwrite them.\n' >&2
    exit 1
  fi
  cp -p -- "$backup" "$staged"
  mv -f -- "$staged" "$authorized"
  printf 'Original authorized_keys restored.\n'
)
```

## What this step proves, and what follows

Appending the line and preserving v1 access does not prove certificate
authentication or enable PVE grants. `sshd -t` checks server configuration; it
does not validate the new key line or demonstrate a certificate login.

Activation still needs CA pair validation in the keeper, explicit SSH targets
and pinned host keys, keeper-only TCP/22 egress, effective certificate settings,
policy wiring and bounded real-provider acceptance. That acceptance must test
the actual keeper certificate's forced-command/PTY/forwarding behavior, mint,
UID-fenced installation, expiry/release, restart recovery and confirmed token
cleanup. Both enable flags stay false until these prerequisites pass.

This recipe covers keeper minting. General hw-ssh grants remain separate work.
See [the workflow checkpoint](workflow-guide.md#proxmox-activation-checkpoint),
[D-69/D-72](../.agents/sagas/distributed-dev-env/designs/001-dev-env-v2.md#612-access-no-prompts-in-the-pod-control-at-the-platform),
and [plan 07](../.agents/sagas/distributed-dev-env/backlog/07-access-broker.md).
