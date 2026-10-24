# Rescue byte retrieval

`agent-run rescue read` verifies the existing thin bundle in the caller's clone and writes that rescue commit's files. It does not start a model, create an AgentSession, check out a repository, or run hooks. The bundle stays the artifact on the shared volume. No plaintext tree is added beside it.

```
agent-run rescue read <session>/<stamp> \
  --repo <name> --commit <oid> \
  --session-uid <uid> --pod-uid <uid> \
  [--project <name>] --out <new-directory>
```

The API is `GET /v1/rescues/{session}/{stamp}/tree`. A caller that passes the binding checks is exec'd in its own pod, which already has the clone. The command verifies the thin bundle there and returns the files. The shelf is not exec'd: it has no base clone. There is no new service, credential, ServiceAccount, Role, or network policy.

## What is stored

A rescue of dirty work runs `git add -A` on a copy of the index and commits that tree on `refs/heads/rescue/...`. The worktree and the real index stay as they were. The commit contains the Git blob of tracked and untracked files, including files that match HEAD. It does not contain ignored files. A staged blob that differs from the worktree is not the rescued blob: the worktree wins. Those blobs are Git objects after clean, end-of-line, and ident filters. They are not a claim that a worktree byte was served.

The thin bundle is still written, and `git bundle verify` still runs in the clone. That bundle omits objects reachable from origin. On the shelf, `git bundle list-heads` can name the rescue commit, and `git bundle verify` cannot rebuild the tree without the base objects. Measured on a five-file fixture: the thin bundle was 583 bytes and `git bundle verify` in an empty repository failed for a missing prerequisite. A full bundle of only the rescue ref also carried ancestor history. This path does not store that history and does not write a plaintext tree tar.

After the thin bundle is verified, agentd classifies each rescue-branch tree from `git ls-tree -r -l -z` only. It does not read blob bodies and it does not write a tar. A failure of that listing sets `treeRefused` to `capture-failed`. A symlink, a gitlink, or a path that is not a regular file sets `not-regular` and does not record the link target. A tree whose base64 JSON would exceed the existing 8 MiB response cap sets `too-large`. Any other tree sets `withheld`. None of those codes fails the thin bundle or a verified stop.

Object reads set `GIT_NO_REPLACE_OBJECTS=1`, `GIT_NO_LAZY_FETCH=1`, `GIT_CONFIG_NOSYSTEM=1`, and `GIT_CONFIG_GLOBAL=/dev/null`, and they unset the variables that would point Git at another object store. A replace ref does not choose the bytes. The manifest hash of a planted tar is not proof of the commit. A read that has no clone returns no file bytes. A read in the recipient clone returns the files only after `git bundle verify` requires the recorded base commit.

## Who may ask

`GET /v1/rescues` stays metadata. Being allowed to list a rescue is not permission to read its bytes.

D-10 and D-48 keep rescue bundles inside the cluster. A human and a client are refused. A coordinator is refused. The only caller that reaches the binding checks is an in-cluster session:

- the session itself, when the session UID matches the caller
- its direct child, when `spec.parent` is that session and `spec.usageCaptureParentUID` is that session's UID

A parent name without that stored UID is refused. A sibling is refused. A canary, a pilot, and a session past the create-depth limit are refused. The other parent's name is not returned. This uses fields sessions already store. It does not add a role, a grant, or an allowlist.

The session object must still exist. Its rescue record must be `Verified`, not superseded, and bound to the requested session UID, pod UID, stamp, repository, and manifest path. The requested commit must be a `refs/heads/rescue/...` entry in `status.rescue.unpushedRefs`, with the same repository base name and the same bundle directory as `lastBundle`. `omittedRefs` greater than zero refuses the request, because the canonical list is then incomplete. A project query is required when the session has a project snapshot, and it must name that snapshot. A missing session cannot prove those bindings, so a reaped session is refused here even though restore can still fetch its bundle.

Passing those checks runs the verification in the caller's pod. The recorded base commit must be present in that clone. `git bundle verify` must require that commit, and `list-heads` must name the rescue ref at the requested commit. The objects are fetched into a private `refs/rescue-read/` ref, with hooks disabled and replace refs ignored, and the ref is deleted afterward. There is no checkout and no fetch from a remote. A missing base, a forged bundle, or a tree whose encoded size would exceed 8 MiB refuses. The read does not call `hold-rescue` and does not change the rescue directory or the worktree.

This is the approved narrow use of the existing thin bundle. DESIGN-001 is unchanged. A human or client download, and a plaintext copy of the tree on the shared volume, are not part of it.

## Limits already in force

The shelf exec and `agent-run` both stop at 8 MiB of response (`maxOutput` and `maxResponse`, `8 << 20`). This path does not raise that cap and does not add another deadline. Classification estimates the base64 size before it would allocate blob bodies, and it does not allocate them. A dev-env worktree measured about 12 MiB of tracked files, so a whole-tree JSON of this repository is larger than the existing response cap. The shelf container memory limit is 256 MiB. The installed shelf image does not contain this route.

`agent-run rescue read` still refuses to write a response that fails its own checks. `--out` must be a new directory. The command creates it with `mkdir` so a directory that appears in between is not replaced. `--out` must not pass through a symlink, so a path whose ancestor is a symlink (macOS `/tmp` and `/var`) is refused. A path component that folds to `.git`, `.git~1`, or `git~1`, or that contains `:`, is refused. The command prints hashes, not file contents, and only when a response matches the request. The server returns those files only after the thin bundle verifies.

## Proof classes

| Class | Route |
| --- | --- |
| Public handoff excerpt | published context, separate from this path |
| Rescue one-tree bytes | thin bundle verified in the UID-bound session's existing clone |
| Private native history | history archive, not this path |

A commit id, a bundle hash, a header label, or a rescue list entry is not acceptance of file bytes. #101 is not accepted by this source path. The installed drill is a later stage. It waits on operator, CRD, template, shelf, and agent-run pins that contain this source, and on a disposable session that is not the live PEAR pod. A whole-tree read of this repository does not fit the 8 MiB cap.
