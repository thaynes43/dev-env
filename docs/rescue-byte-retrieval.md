# Rescue byte retrieval

`agent-run rescue read` copies one rescue-branch tree into a new directory. It does not start a model, create an AgentSession, check out a repository, or run hooks.

```
agent-run rescue read <session>/<stamp> \
  --repo <name> --commit <oid> \
  --session-uid <uid> --pod-uid <uid> \
  [--project <name>] --out <new-directory>
```

The API is `GET /v1/rescues/{session}/{stamp}/tree`. The operator reads it by exec of `agentd ctl rescue-tree` in the existing shelf pod. There is no new service, credential, ServiceAccount, Role, or network policy.

## What the bytes are

A rescue of dirty work runs `git add -A` on a copy of the index and commits that tree on `refs/heads/rescue/...`. The worktree and the real index stay as they were. The commit contains the worktree bytes of tracked and untracked files, including files that match HEAD. It does not contain ignored files. A staged blob that differs from the worktree is not the rescued blob: the worktree wins.

The thin bundle is still written, and `git bundle verify` still runs in the clone. That bundle omits objects reachable from origin. On the shelf, `git bundle list-heads` can name the rescue commit, and `git bundle verify` cannot rebuild the tree. Measured on a five-file fixture: the thin bundle was 583 bytes and `git bundle verify` in an empty repository failed for a missing prerequisite. `git archive` of the same commit was 10240 bytes. A full bundle of only the rescue ref was 802 bytes and fetched, and it also carried ancestor history. This path does not store that history.

The disposable rescue fixture in the agentd tests writes six regular files. Its tree tar was 7168 bytes and the JSON response was 1024 bytes. The rescue itself took about 50 ms. Both are far under the existing 8 MiB response cap. The thin bundle for that fixture was about 1 KiB; its exact size follows the worktree path recorded in the rescue commit message.

During rescue, after the thin bundle is verified, agentd writes a tar of each rescue-branch tree beside the bundle. The tar is built from `git ls-tree -r -z` and `git cat-file --batch`, with hooks disabled, not from `git archive`. Restore ignores the tar. A tree that is not only regular files (`100644` or `100755`) sets `treeRefused` to `not-regular` and omits the tar. The thin bundle stays valid, and a byte read of that rescue is refused.

## Who can read

`GET /v1/rescues` stays metadata. Being allowed to list a rescue is not permission to read its bytes.

The caller class is the class that may restore: human, client, or a session that can still create. A coordinator cannot restore, so a coordinator cannot read bytes. A canary or pilot session cannot create, so it cannot read bytes. A session deeper than the create limit cannot read bytes.

Byte egress is narrower than restore. Restore lets that caller class create a session from any complete rescue. A byte read also requires the existing `spec.parent` identity:

- a human or client may read only a session whose `spec.parent` is that caller
- a session may read its own rescue, or a rescue whose `spec.parent` is that session

A sibling session is refused. The other parent's name is not returned. This uses the parent field sessions already store. It does not add a role, a grant, or an allowlist.

The session object must still exist. Its rescue record must be `Verified`, not superseded, and bound to the requested session UID, pod UID, stamp, repository, and manifest path. A project query is required when the session has a project snapshot, and it must name that snapshot. A missing session cannot prove those bindings, so a reaped session is refused here even though restore can still fetch its bundle.

The read does not call `hold-rescue` and does not change the rescue directory.

## Limits already in force

The shelf exec and `agent-run` both stop at 8 MiB of response (`maxOutput` and `maxResponse`). This path does not raise that cap and does not add another one. A tree whose JSON exceeds it fails closed. A dev-env worktree measured about 13 MiB without `.git`, so a rescue of this repository is larger than the existing response cap. The installed drill uses a small tree.

The read refuses a path that is absolute, escapes the rescue directory, contains `..` or `.git`, or is a symlink. It refuses a tar entry that is not a regular file. `--out` must be a new directory, must not exist, and must not pass through a symlink. The command prints paths and SHA-256 hashes, not file contents. A failed read leaves no `--out` directory.

## Proof classes

| Class | Route |
| --- | --- |
| Public handoff excerpt | published context, separate from this path |
| Rescue one-tree bytes | `agent-run rescue read` |
| Private native history | history archive, not this path |

A commit id, a bundle hash, or a rescue list entry is not acceptance of the file bytes. #101 is not accepted by this source path. The installed drill is a later stage.
