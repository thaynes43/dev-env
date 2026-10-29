# DESIGN-035: Session temporary storage

Date: 2026-10-29. Source-only work orders DEVENV251-TMP-SIZING-20261029-01
and DEVENV251-TMP-SIZING-20261029-02,
[OpenProject #251](https://work.haynesnetwork.com/work_packages/251), Epic #103.
Merge and rollout remain held; this PR stops when ready with CI started.

## Problem and observed facts

The size-S coordinator session was evicted on October 29 with `Usage of
EmptyDir volume tmp exceeds the limit 8Gi`. The work order initially reported
about 16:41Z; #251 journal 2503 corrects the observed PodReady/PodEnded transition
to 17:09:09Z. These are reported incident facts, not a new runtime measurement.

The controller currently gives every session an 8Gi disk-backed `/tmp` EmptyDir.
HOME is a separate persistent volume (20–40Gi in the incident context). Go's
build and module caches already default to HOME (`~/.cache/go-build` and
`~/go/pkg/mod`); pip, npm and Playwright also normally cache there. Go build
work directories default to `/tmp` without GOTMPDIR, alongside test temporary
directories and other tools' scratch files. The incident did not establish a
breakdown of which files consumed the 8Gi.

## Changes and defaults

1. Each template size class accepts optional `tmpSizeLimit`, a Kubernetes
   `resource.Quantity`, inclusively between 1Gi and 64Gi. For example, a later
   templates change could select S=8Gi, M=16Gi and L=32Gi. This source change
   selects none of those new values. Missing or null means exactly the existing
   8Gi EmptyDir. Hold pods use their existing size-S selection.
   Keep the field a pointer with `omitempty`; do not materialize its default in
   parsed templates. Existing serialized templates, revision hashes and generated
   pods remain identical when it is absent. An explicit value participates in the
   existing global template revision and therefore the existing Outdated/drain
   logic; this does not introduce per-size revisions or mutate existing pods.
2. The agent image sets `GOTMPDIR=/home/dev/.cache/go-tmp`. Agentd creates the
   directory during boot rendering after HOME is mounted, before repository
   preparation or agent launch. Rendering honors an explicit GOTMPDIR override
   and is idempotent. After creating the default directory, boot removes only
   its `go-build*` entries left by interrupted Go builds/tests. Cleanup skips
   symlink entries and a GOTMPDIR with any symlinked path component, stays
   confined to that directory, and never cleans a user-selected override.
   Nested symlinks are not followed.
   This is safe only at boot: HOME has one writer and no Go process has started.
   Persistent HOME avoids `/tmp` eviction but retains scratch after a killed
   build; boot cleanup bounds accumulation across restarts, not within one long
   session. Other contents and Go caches are kept. No global TMPDIR change or
   cache migration is included. Tests using os.TempDir can still use `/tmp`.
   HOME capacity still needs monitoring at adoption. If an agent clears
   `~/.cache`, recreate the work directory with `mkdir -p "$GOTMPDIR"` before
   running Go again; otherwise Go fails until the next boot with
   `creating work dir: stat /home/dev/.cache/go-tmp: no such file or directory`.
3. Agentd samples `statfs(/tmp)` at most once every five minutes and logs one
   warning line on crossing above 75%, including used bytes and filesystem
   capacity and available bytes. Use the fragment size (falling back to block
   size) and `Bavail` for available space; the percentage is used divided by
   used plus available, as in df, so reserved blocks are not available space.
   It suppresses repeats while high, and rearms after recovery. A
   failed measurement is not a zero-usage observation. No status protocol,
   API or CRD field is added. **This observes backing-filesystem pressure:**
   disk-backed EmptyDir sizeLimit is a kubelet eviction threshold, not a
   filesystem quota visible to statfs. The warning cannot predict the incident's
   per-directory 8Gi eviction or attribute other pods' filesystem use to `/tmp`.
   Measuring EmptyDir-specific consumption and transporting its configured
   threshold is separate work; do not label filesystem capacity as that limit.

No dependency or third-party component changes, so THIRD_PARTY.md needs no change.

## Rollout and acceptance boundaries

This work changes dev-env source only. No local image build, merge, release,
haynes-ops edit, dev-env-templates edit or cluster write is authorized here.
After Tom approves adoption, deliver the operator and signed agent image through
the normal GitOps path, then explicitly select any per-size limits in templates.
Template/image changes drain eligible sessions at idle under existing rules;
never restart busy sessions or manually delete volumes or temporary contents to
adopt it.
Roll back desired image/template values through GitOps under the same hold.

Source/test acceptance is distinct from image build/publication, deployment and
usable acceptance. A later approved fresh session must show GOTMPDIR exists on
HOME, a Go build places work there, and `/tmp` has the selected sizeLimit. Check
HOME headroom and node ephemeral-storage capacity before increasing limits;
an EmptyDir sizeLimit alone reserves no node disk.

## Verification

- Template parsing: omission, null, valid values and inclusive bounds; reject
  invalid quantities and out-of-range values. Pin the pre-change example
  revision and confirm an explicit value changes it.
- Controller: compare the pre-change pod bytes for omitted values; exercise
  S/M/L/default-M and size-S hold pods with overrides. Existing envtest tests
  cover Outdated marking and safe idle drains; run that suite too.
- Agentd: fresh HOME, override and repeated directory creation; stale
  `go-build*` cleanup with unrelated entries and symlink targets preserved;
  fake statfs and clock for fragment/block sizes, reserved blocks, threshold,
  rate limiting, warning suppression/recovery and errors.
  No capacity-filling test or load loop.
- Go 1.27.2; focused templates, controller (including envtest) and agentd suites
  serially under `nice -n 19`, `GOMAXPROCS=2`, `-p 1`, with GOCACHE and GOTMPDIR
  under HOME. For local Unix socket fixtures, use a short HOME path for both
  GOTMPDIR and TMPDIR so paths fit and socket renames stay on one filesystem.
  This is a test-process setting, not an image TMPDIR change. Then `make lint`
  and `make check-layout`. No generated files are
  intended; run `make check-generated` if that changes. CI builds/smokes the image.

The PR and #251 handoff record actual checks and independent native review.
