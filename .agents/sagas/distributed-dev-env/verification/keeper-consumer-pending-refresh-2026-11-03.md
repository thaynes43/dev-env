# Consumer view of a pending keeper refresh, 2026-11-03

## Observation (generic, relative timing only)

One live interactive session saw an OAuth revocation. The keeper's next login
generation arrived about 59 seconds later and an automatic retry about 1 second
after that succeeded. The login had been valid earlier the same day. Retry
recovery does not prove prevention.

## What the consumer does today (public source, synthetic tests)

agentd reads the keeper's projection from local files every 5 seconds and calls
no API, so the consumer adds at most one or two polls to any gap. It already
sends one `continue` per newly visible generation, and a stale generation does
not spend that recovery. Synthetic injected-clock tests (revocation then a
generation at +59 s, publish delays of 30 s, 119 s and 10 min, a stale
generation, four concurrent readers, a restart inside the gap) pin that existing
behavior; they pass on current main and are characterization, not a found defect.
What accounts for the roughly 59 s is a hypothesis, not an established cause: it may be the keeper's cadence plus the Secret volume update delay, but neither is visible from this repository and nothing here proves it.

## The defect this change fixes (display, plus one deliberate rc behavior change)

An applied access token that has just expired, while the keeper's next
generation is not yet visible, was shown as `NeedsLogin` by `agent-run auth
status` and as `login-required` in agentd's status text, as if a person had to
sign in. Now an access token that expired less than 3 minutes ago is shown as
`Refreshing` (and `refreshing:` in the status text) with the time since expiry.
Past the window, when the token has under a second left, or when `login.json`
shows the whole login lapsed (both `auth status` and agentd read it), the state is
`NeedsLogin` / `login-required` as before. Exit codes, the wire state word and
every `continue` fence are unchanged; a script still sees exit 1.

**This is not purely a display change.** `agent-run rc start --wait` recognized the
old `login-required: waiting for the keeper's Claude projection` text and turned
30 seconds of it into `action_required` (ask the owner to act). The new
`refreshing:` text does not match, so `rc start` now keeps waiting while the
status is `refreshing:` instead of asking for action while the keeper is already
refreshing (asking a person to act during a pending refresh is the false-login
symptom). Two bounds apply, and they are not the same as the 3-minute window:

- If the status returns to the old `login-required` text, the 30-second
  owner-action rule runs from that moment, within the rest of the 90-second
  agent-start budget.
- If a `refreshing:` status lasts until the 90-second agent-start budget ends
  (the window is longer than the budget), the launch ends as `timed_out` with the
  specific code `auth_refresh_pending`, session handle retained, and no sign-in
  requested. It is not reported as `action_required`.

It is covered by four rc tests (recovers while refreshing; owner action after a
return to login-required; a refresh outlasting the budget times out with its own
reason; login-required alone still ends in owner action) and by a mutation that
restores the old matching and one that drops the specific reason.

The 3-minute window is a heuristic: the keeper refreshes ahead of expiry and the
Secret volume is assumed (unproven) to trail the keeper by about a minute. It is two constants,
`claudePendingWindow` and `authPendingWindow`.

## What this does NOT fix (limits to preserve)

- The original revoked-but-not-yet-expired case. A token the provider revoked
  while its local expiry is still in the future reads as `Ready` everywhere,
  because the consumer persists no rejection signal. An honest "rejected, next
  generation pending" state needs a persisted marker, a status reader and
  probably a new wire state word (touching `internal/standby` metrics and the
  protocol enum). That is an open design decision, not done here.
- The wire state for an unstarted remote session still reads `login-required`;
  only its text changes.
- Reading a refresh marker published by the keeper would need the held keeper
  candidate's files (PR 885, candidate 5d6640c), which this change does not
  touch, copy or publish.
- This change does not claim reliable refresh or prevention of false status. It
  narrows one false display and one dependent wait. The original reliable-refresh
  gap stays open. Installed behavior and any real session are unverified.

## Evidence

On current main the three Pending rows in `TestAuthStatusPendingRefreshIsNotNeedsLogin`
and the two agentd tests (`TestProjectionStatusSeparatesPendingRefreshFromLapse`,
`TestPendingRefreshReachesTheStatusAndClearsOnTheNextGeneration`) fail; they pass
with the change. One older codex row (1 minute expired) was moved to 10 minutes
because 1 minute is now inside the window.
