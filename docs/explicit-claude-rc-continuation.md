# Explicit Claude Remote Control continuation

This source adds a pod-local supervisor for one explicit continuation of a
retained private Claude task. It preserves the original Session, HOME,
conversation, launch and task outcome. It does not change Session mode or
permit ordinary automatic task resume.

The saga decision and adoption plan are in
[DESIGN-047](../.agents/sagas/distributed-dev-env/designs/047-explicit-claude-rc-continuation.md).

The caller must already have authorized control of the target pod. Request
provenance and cooperative-freeze descriptions are optional audit text. They
are not credentials, and a locally written freeze file grants no authority.
Runtime transition also requires the original owner's cooperative checkpoint
and confirmed exit through the supported control path.

## Pod-local source contract

`agentd ctl explicit-claude-rc-continuation` accepts a bounded version-1 JSON
request naming its stable request ID, Session UID, native conversation,
original HOME and title. The control command must be the same executable as
the live daemon recorded for this boot. An older daemon without that binding,
a side binary, another pod, a different HOME or an uncertain owner is refused.

The original Claude launch must bind the same Session UID, pod UID, worktree
and conversation. Its completed result must bind that launch's conversation,
boot and times. State reads use bounded private regular files without symlink
traversal. Transcript inspection reads only metadata at the exact worktree's
Claude project key; it does not scan conversation contents.

The continuation uses the existing usable Claude login. Its child environment
removes static OAuth token overrides, as an ordinary Remote Control launch
does. This operation never logs in, refreshes credentials or copies them.
Accepted project rules remain present, with the interactive worktree guard.
The command resumes the original conversation with one `--resume` and one
`--remote-control` title. Print, clear, fork and new-session-ID arguments are
refused.

## One durable intent

Before changing `resume.json` or starting tmux, the supervisor fsyncs a journal
with the immutable request, original-state hashes, resume hash, boot and one
writer generation. A live process becomes supervised only after its PID and
start time are observed. A conflicting request preserves the previous resume.
An ambiguous start retains its intent and cannot start another writer.

A daemon restart reconciles the recorded process or terminal exit. It never
replays the spawn. A matching live process stays visible across daemon boot
changes. A missing or inconsistent process/exit record reports uncertainty,
which is not terminal-task reclaim proof. A bound intentional TUI exit ends
the continuation and is never automatically relaunched. The original task
outcome remains retained, and no permanent reclaim exemption is introduced.

This pod-local generation uses the original pod. While its RC process is live
or uncertain, status is not exited, so the existing terminal-task compute
reclaim proof refuses it. HOME expiry still requires the whole original pod
to have terminated, including all its containers, on a live node. An earlier
task result alone cannot satisfy that proof. An explicit suspend/reap retains
the existing guarded whole-pod stop path; the journal adds no stop authority.

Remote Control status reports a URL only from the live CLI's matching process
record and bridge ID. Synthetic registration fixtures test that projection;
they do not demonstrate a real registered account entry or phone connection.
Persistence means keeping the owned conversation through turns, network
reconnection and controller/daemon observation. An intentional terminal exit
ends it.

## Installation and remaining integration

The old task daemon cannot safely consume this capability through a standalone
binary. Both start and heartbeat must run the reviewed daemon. Replacing a pod
also changes its pod UID, so an image pin alone is insufficient: the original
executor's stop and HOME identity must be bound to the new owned generation.

The required operator integration is default off and Claude specific. Its
configuration must select one exact Session name/UID and a signed released
agent digest. An authenticated owner request must bind its old executor,
original task outcome, HOME claim UID and conversation to the replacement
pod/generation. It must preserve every unrelated Session and image. A Codex
selector, a mode patch, a manually replaced daemon and a global worker image
change are not installation paths for this capability.

That operator adoption integration is still being implemented. Before runtime
use it needs focused failure/race tests, independent exact-source review,
required CI, signed publication, a reviewed installation plan and the target
owner's cooperative stop evidence. Source or build success is not a usable
Remote Control result. Acceptance requires the actual URL/title/account entry,
a live native acknowledgement and persistence/reconnection evidence.
