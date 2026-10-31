# DESIGN-040: A truthful Cursor/Grok actor on the platform inbox

- **Status:** Proposed, 2026-10-31. Design only. It adds no code, CRD value,
  credential, ACL change, flag or activation, and it runs nothing.
- **Tracking:** OpenProject [#321](https://work.haynesnetwork.com/work_packages/321),
  Epic [#91](https://work.haynesnetwork.com/work_packages/91), Saga #85;
  journal 4016 and its correction, journal 4018.
- **Work order:** OVERNIGHT-GROKACTOR-321-DESIGN, from the #321 lead
  `dev-env-1029-132039`.
- **Source examined:** dev-env `9a8fac11578375116742c531b185e7fd5cc707c6`;
  haynes-ops `.agents/runbooks/cursor-v1-worker.md` at its last change
  `76a53ac91471b36e7bd929c3868de6d7615c5c1b`, read only. Cursor's public CLI
  pages were read on 2026-10-31 (section 11). No Cursor or Grok process was
  started, no pane was read and the v1 pod was not contacted.
- **Related:** [DESIGN-014, Ask and reply](014-durable-offline-inbox.md#ask-and-reply-2026-10-31-openproject-321),
  [DESIGN-001](001-dev-env-v2.md) 3.3, 6.2, 6.3 (D-78) and 6.13 (D-33),
  [DESIGN-025](025-private-home-expiry.md),
  [DESIGN-026](026-offline-provider-preparation.md),
  [the Cursor ACP client](../../../../docs/cursor-acp-session.md) (#314, #318),
  saga [decisions 74 and 79](../README.md#decision-log).

## 1. Outcome and why it is blocked today

#321 asks for "dedicated authorized Codex, Claude and Cursor/Grok actors" that
each send a real ordinary question, get a correlated coordinator reply and
acknowledge it through the platform inbox. These proofs are installed and
lead-verified: Claude to Claude (journal 3999), Codex to Claude (4013), and
Codex asking its recorded parent without `--to` (4031). No Cursor/Grok actor
can do the same under today's authority. The reason is not a missing command.
The inbox records who sent a message from the authenticated Session, and no
Session can truthfully be a Cursor actor.

This design compares three ways to change that, recommends one, and frames
the one decision Tom must make. It authorizes none of them.

## 2. Facts this design rests on

| Fact | Evidence |
| --- | --- |
| Once a request passes validation, only an authenticated, live, project-bound Session may publish. Any other caller class, the v1 pod included, gets the uniform 404. | `internal/apiserver/inbox.go:160-172` (validation, then `c.kind != kindSession`), `inboxSenderCaller` at `:81`, the project snapshot check after it |
| A recipient must be a Session whose accepted project snapshot matches the project. | `newInboxRecipient`, `inbox.go:140` |
| The server derives provider and requested model from the sender Session's `spec.agent` and `spec.model`. The request body cannot set them. | `inbox.go:183`; strict decode in `boundedInboxJSON`; DESIGN-014 "Do not accept caller-supplied provenance" |
| The stored provider is a schema enum, `claude`, `codex` or `opencode`. The schema accepts only `actor.kind == 'session'` for a sender and for an acknowledger. | `api/coordination/v1alpha1/inbox_types.go:40`, `:23`, `:89` |
| The AgentSession agent kinds are `claude`, `codex` and `opencode`. `opencode` is in the enum but the API refuses to create it. | `api/v1alpha1/agentsession_types.go:31`; `internal/apiserver/validate.go:156` |
| The v1 pod is a trusted Client, not a Session. DESIGN-014 leaves publication by Session-less hosts as separate work and gives them no synthetic Session UID. | `internal/apiserver/caller.go:28`, `:128`; DESIGN-014 "Authorization and acknowledgements" |
| A Session's token reaches more than the inbox: it can create child Sessions, request grants, post Asks to Tom, make task claims and published contexts. A canary Session is already refused child creation, a precedent for narrowing one kind of caller. | `internal/apiserver/sessions.go:51`, `grants.go:45`, `asks.go:74`, `taskclaims.go:75`, `publishedcontexts.go:79` |
| No image-provided or keeper-owned Cursor path exists in v2. A user-installed `cursor-agent` exists only on one workbench HOME (`dev-env-1027-010058`, installed 2026-10-29) with its own pod-owned login, which was not read. The OP314/318 Grok ACP runs used it. It is not a supported fleet route, and that Session's inbox identity is its own Claude or Codex Session, not a Cursor actor. | #321 journal 4018 (correction to 4016) |
| v1's Cursor login is `~/.config/cursor/auth.json`, a vendor browser login owned by the v1 pod. It must never be copied or printed. | haynes-ops `cursor-v1-worker.md`, "Persistent paths" |
| Tom ruled on 2026-10-20: "Let them go offline." The v1 Cursor workers stop with v1 and are not moved to v2. Cursor on v2, if ever wanted, is its own story. | saga README decision 79 |
| The ACP client sends only `reject_once` for a permission request and refuses `fs/*` and `terminal/*` callbacks. Its child runs as the caller and "does not become an authenticated dev-env session or gain inbox permissions". Existing Cursor workspace settings may still load. | `scripts/cursor-acp-session.py:406`; `docs/cursor-acp-session.md` |
| Cursor's authentication page documents a browser login (`agent login`; `NO_OPEN_BROWSER=1` prints the link) and a user API key (`CURSOR_API_KEY` or `--api-key`). It does not describe refresh, rotation or lifetime. Cursor's ACP page also lists `--auth-token` / `CURSOR_AUTH_TOKEN` as a way to authenticate before startup, but does not say what kind of token it takes or who issues it. | Cursor CLI authentication and ACP pages, read 2026-10-31 |
| ACP mode documents no model selection and no report of the model in use. Its permission options are spelled `allow-once` and `reject-once`; the repository's ACP client requires the underscore kinds and fails closed on any other spelling. | Cursor ACP page; `docs/cursor-acp-session.md` "UNVERIFIED" list |
| Headless `stream-json` emits one `system`/`init` event at the start of a session, with a model **display name**. No later event names a model. | Cursor output-format page, read 2026-10-31 |
| Cursor runs the model loop on Cursor's backend. URL-based MCP servers on Cursor My Machines also run from there, which is why v1 uses a local relay. | haynes-ops `cursor-v1-worker.md`, first section and "Rules and MCP adapters" |
| HOME expiry exports four fixed roots: the Claude config directory, `CODEX_HOME`, agentd's state directory and the Session's log. `unknown_entry` is raised only inside those roots, so a Cursor directory elsewhere in HOME is never listed. Today a `cursor` Session's archive would be refused by the provider check instead. | `internal/agentd/historyexport.go:248-290`, `:486`; `internal/agentd/historyarchive.go:155` |
| Common-rule loading supports Claude and Codex tasks only. | `internal/agentd/common_rules.go:63` |

## 3. What "truthful" means for this actor

Every field below must come from the platform or from a supported provider
observation. None may come from the request body or from model text.

| Field | Truthful source | Never |
| --- | --- | --- |
| Actor | The caller's projected token, bound to its Session UID and Pod UID | A name in the message text, or a configured host speaking for "Cursor" |
| Provider (the harness) | The Session's `spec.agent`, set at create and immutable | A field the sender supplies |
| Requested model | The Session's `spec.model`: a full Cursor model id from a pinned allowlist, for example `grok-4.7-high` | An alias such as `auto`, or a display name |
| Session model reported by Cursor | The `init` event's display name, kept verbatim and labeled as reported at session start | Used as the served model, mapped to an id, or written into the requested-model field |
| Served model | `Unknown`. No documented Cursor event reports the model that served a turn | Inferred from the requested model or the `init` name |
| Model vendor (xAI, behind Cursor) | Not observable by the platform | Claimed |

DESIGN-026 already says Cursor requested metadata must never carry the
`ProviderReported` served-model label. That stays true.

## 4. Options

### 4.1 Option (a): a v2 agent kind `cursor`

A Cursor Session is an ordinary v2 session pod whose agent is the pinned
Cursor CLI. It calls `agent-run inbox` from its own shell tool with its pod's
token. The inbox code that checks senders, recipients and acks does not change.

**What it needs.**

1. **Schema.** Add `cursor` to the AgentSession agent enum and to the
   InboxMessage provider enum. Leave the usage status enums at `claude|codex`,
   so Cursor usage reads as absent or `Unknown`; decide the SessionDirectory
   provider value (`Unknown` or a new `cursor`) in the source story. haynes-ops
   installs each CRD copy before the operator pin that writes the value.
2. **API.** Refuse `agent: cursor` unless a new default-off operator flag is
   set, as `opencode` is refused today. Accept task mode first. Check the
   model against a pinned allowlist, like `ValidateCodexModel`
   (`internal/agentd/protocol/session.go`). Cursor ids carry their effort,
   so refuse a non-empty `effort`.
3. **Caller rights.** A `cursor` Session's token can do everything any
   Session's token can (section 2). Because Cursor's backend chooses the
   commands the pod runs, decide in the source story what a `cursor` caller
   may do beyond the inbox. The default proposed here: inbox ask, reply,
   read and ack only; no child Sessions, grants, Asks, claims, published
   contexts or reminders, refused the way canary Sessions are refused child
   creation.
4. **Images.** Pin the Cursor CLI by version and SHA-256, with any
   self-updater disabled, in the agent image and in a login-helper image for
   the keeper (as `images/codex-login/` serves Codex). Renovate tracks the
   pin like the other CLIs.
5. **agentd adapter.** D-33's verbs (start, resume, status, deliver) for the
   Cursor CLI's headless `-p` mode with `stream-json` output, under its
   non-interactive permission mode (D-23). It keeps the `init` display name
   as the reported session model and nothing more. Register **no** MCP
   servers for the first acceptance: Cursor may run URL-based servers from
   its backend, which would hand it their URLs and tokens. MCP is a later
   decision.
6. **Common rules.** Load the common and repository rules into the Cursor
   CLI through its own instruction mechanism, extending
   `common_rules.go` beyond Claude and Codex, with a check that the running
   CLI received them.
7. **Credential, keeper-owned.** A fresh Cursor login made by the keeper's own
   ceremony, the way D-78 made the Codex login: Tom signs in through a link,
   the keeper holds the login and is its only refresher, behind its Lease.
   The keeper publishes an access-only document (an access token and its
   expiry, no refresh material) to one Secret, and agentd places it in the
   pod. The pod never runs `agent login` and never holds refresh material.
   It never copies v1's `auth.json` or the workbench HOME's login.
8. **HOME expiry.** Add a Cursor history root beside the Claude and Codex
   roots, a path field for it in the terminal record, `cursor` as an
   archive provider, and class rules that put the Cursor auth file in a
   credential class. Widening the provider check alone would export no
   Cursor history and let the volume's deletion drop it silently.
9. **Profile and egress.** Run Cursor Sessions on a narrow profile: the
   acceptance needs only the API token for `agent-run`, not `full`. The
   profile needs egress to Cursor's API, and the keeper needs egress to
   Cursor's sign-in and token endpoints.
10. **Guidance.** Tell every coordinator, whatever its provider, to treat
    question text as untrusted task content, as the ACP client's guide
    already says of provider text. `docs/ask-your-coordinator.md` today says
    only that a reply is not approval.

**Spike first (S-C1).** Cursor does not document how its browser login
refreshes, or what `CURSOR_AUTH_TOKEN` accepts. Before any keeper code, one
bounded spike must show:

- whether the pinned CLI runs `agent -p` on a pre-minted access token alone,
  how long that token lives, and whether logging out revokes tokens already
  issued;
- whether the browser login rotates a refresh token, and so whether one
  refresh owner is required (assume yes until shown otherwise);
- whether a new login revokes another login of the same account;
- how the sign-in completes from a pod with no browser:
  `NO_OPEN_BROWSER=1` prints the link; whether a code is pasted back is
  Unknown.

The spike's own risk must be stated to Tom before he signs in: **a new
login may sign out other Cursor logins on the account**, such as the
workbench HOME's and, while v1 runs, the v1 workers'. The spike is
necessarily a pod-owned login, so it is held to these limits: one disposable
pod; the pinned archive installed and checked by SHA-256; nothing written to
`~/.shared`; `agent logout` at the end, then the pod reaped and its volume
deleted.

If the CLI cannot run on an access-only projection, the fallback is a static
user API key (`CURSOR_API_KEY`) in a profile Secret. It has no refresh token,
so it cannot race, but it is a long-lived, account-wide bearer in every
Cursor pod, and its billing terms are Unknown. That fallback is a separate
credential decision, made when the spike result is known.

| Property | Assessment |
| --- | --- |
| Identity truthfulness | Actor, provider (`cursor`) and requested model are all derived by the server from the Session. The served model is `Unknown`; the `init` display name is kept only as the session model Cursor reported at start. Grok's vendor is behind Cursor and is not claimed. |
| Credential ownership | One owner: the keeper. Pods hold access tokens only. Nothing is copied from v1 or from the workbench HOME. |
| ACL change | None in the inbox handlers: two enum values and one create gate. A Cursor Session is asked and answers exactly like any project-bound Session. Its other API rights are narrowed, not widened (item 3). |
| Reach of the Session token | Without item 3, a third party's model could create Claude or Codex children, request grants and post Asks to Tom through the pod's token. Item 3 limits it to the inbox. |
| Impersonation | The Cursor CLI cannot claim another Session: the token, Session UID and Pod UID fences hold. As with Claude and Codex, the CLI can read what its pod holds (the API token, the projected Cursor token and any profile secrets), so the narrow profile matters. |
| Replay | Sender UID plus key, unchanged: an identical retry returns the stored record, a changed intent gets 409, a replacement Session cannot adopt the old inbox. A stolen projected Cursor token is usable at Cursor until it expires; no refresh token leaves the keeper. |
| Prompt-injected questions | A Grok asker steered by injected repository or web content can send a question meant to extract secrets or get an action approved. A reply is information only (DESIGN-014): no grant, held action or Ask changes, and a tool-permission request stays off the question route. Item 10 adds the coordinator guidance. Only the question's recipient can reply, so a third party cannot inject a reply. |
| Data leaving the cluster | Everything the Cursor agent reads, inbox replies included, goes to Cursor's backend and on to the model vendor. This is a new processor beside Anthropic and OpenAI. Private catalog projects need their own decision before a Cursor Session is bound to one. |
| Operational cost | Highest of the three: six stories after the spike (section 7), a third CLI to pin and bump in two images, a login that lapses on a schedule not yet known, Cursor plan usage that the usage report cannot yet see, two CRD copies with a rollback floor. |
| Reversibility | Good, with two limits. The flag off stops new Cursor Sessions; removing the pin removes the CLI. Deleting the Secret and logging the keeper out stops new tokens, but tokens already in pods stay valid until they expire unless logout revokes them (a spike question). Once a `cursor` record exists, neither CRD may be reverted below the enum that holds it, as with ask and reply. |

### 4.2 Option (b): a v1 sender class

The v1 pod's existing Client token gets a new caller class that may publish,
read and acknowledge, with an actor of `cursor` and a provider of `grok`.

**What it needs.** A third `actor.kind`; a sender that is not a
`SessionIdentity` (v1 has no Session UID); a recipient and acknowledger that
are not Sessions, so a coordinator's reply can reach the v1 asker; changes to
the two CEL rules that now allow only Session senders and acknowledgers; and
a way to tell the server the actor and model. That last part is the problem.

| Property | Assessment |
| --- | --- |
| Identity truthfulness | Claim only. The token proves "the v1 pod", not "Cursor". Claude, Codex and Cursor all run in v1 as the same user under the same ServiceAccount, so `actor=cursor` and the model would have to come from the request. That breaks DESIGN-014's rule: "Do not accept caller-supplied provenance." |
| Credential ownership | The one strength: v1's Cursor login stays where it is, with its single owner, and nothing is copied. |
| ACL change | Large: a new publishing class, a non-Session recipient and acknowledger, and two schema rules relaxed. DESIGN-014 left Session-less publication as separate work; this would be that work, built on a claimed identity. |
| Impersonation | Easy. Any process in v1 can publish as Cursor/Grok. |
| Replay | Idempotency keys would share one v1 namespace across every v1 process, or need a synthetic sender identity, which DESIGN-014 does not assign. |
| Prompt-injected questions | As in (a), with a wider blast radius: a Grok session in v1 runs beside v1's full credential set. |
| Data leaving the cluster | As in (a), from a pod that holds more: whatever the Cursor agent reads in v1 goes to Cursor's backend. |
| Operational cost | Less code than (a), all of it thrown away when v1 retires. Decision 79 already takes the v1 Cursor workers offline. Any change under haynes-ops `apps/dev/dev-env/app/resources/**` restarts v1 and is a held draft for Tom. |
| Reversibility | The class can be switched off, but its immutable records keep claim-only provenance for good, and the relaxed schema gets its own rollback floor. |

### 4.3 Option (c): a v2 Session that hosts the ACP client

A project-bound Claude or Codex Session runs the Cursor CLI through the ACP
client as a child and relays Grok's questions to the inbox.

It reduces to (a), and is worse on identity:

- **The credential is the same problem.** The Cursor CLI needs a Cursor
  credential in a v2 pod. That is (a)'s keeper ceremony, projection and image
  pin, or a pod-owned login that does not scale past one HOME.
- **The record names the wrong provider.** The server writes the host's
  `spec.agent` and `spec.model`, so a Grok question is stored as `claude` or
  `codex` with the host's model. Making it say `cursor` needs the Session's
  own spec to say `cursor`, which is (a). A sender-supplied sub-provider is
  forbidden.
- **The host, not Grok, is the sender.** The ACP client rejects every
  permission request, so the Grok model cannot run `agent-run` through an
  approved permission. Whether Cursor runs tools unprompted under loaded
  workspace settings is Unknown; if it can, it acts with the host's token,
  which is still the host's identity. In the supported path the host model
  reads `question-required` events and relays them, and can rewrite or
  invent what it relays.
- **The model is unprovable.** ACP documents no model selection and no
  model report, so even the requested model is the host's assertion.
- **The client may not even run.** Cursor's ACP page spells permission
  options `allow-once` and `reject-once`; the client requires the underscore
  kinds and fails closed. That is a source fix, but it shows the route is
  unverified end to end.
- The workbench HOME in journal 4018 is an unsupported instance of this
  option. Its records name that Session's own provider.

| Property | Assessment |
| --- | --- |
| Identity truthfulness | Fails: records name the host's provider and model. |
| Credential ownership | Same as (a), or a pod-owned login per HOME. |
| ACL change | None in the handlers, but the truthful variant needs (a)'s schema anyway. |
| Impersonation | The host can rewrite or invent the relayed question. The child shares the host's user, so it can read the host's files (the ACP guide says so). |
| Replay | Bound to the host's UID; the Grok turn has no identity of its own. |
| Prompt-injected questions | Two models in the path, and the relay can turn injected text into the host's own question. |
| Data leaving the cluster | The child can read the host's API token, `/creds/gh_token` and HOME, and anything it reads can reach Cursor's backend. The ACP client limits the child's environment, not its files. |
| Operational cost | (a)'s credential work plus bridge code. |
| Reversibility | The bridge can be removed, but mislabeled records are immutable. |

### 4.4 Variants rejected outright

- **Copy a login.** Copying v1's `auth.json` or the workbench HOME's login
  into a v2 pod or the keeper gives one token family two owners. Refused, as
  for Claude (6.2) and Codex (D-78).
- **A caller-supplied `provider: cursor`** on a Claude or Codex Session's
  message. Forbidden by DESIGN-014.
- **A per-pod `agent login` in every Cursor Session.** One owner per HOME
  holds, but Tom would sign in for each pod and each login would sit in a
  HOME that rescue and expiry must exclude. Decision 74 likewise replaced a
  pod-owned Claude login slice with the keeper-owned login. The spike's one
  disposable login is the only exception, under the limits in 4.1.

## 5. Comparison

| | (a) v2 kind `cursor` | (b) v1 sender class | (c) v2 host + ACP |
| --- | --- | --- | --- |
| Actor and provider truthful | Yes, server-derived | No, claimed | No, the host's |
| Served model | `Unknown`; Cursor's `init` name kept as reported | Claimed | Unprovable |
| Refresh owner | Keeper only | v1 pod, unchanged | Keeper (as a) or per HOME |
| Inbox ACL change | None (enum values only); other rights narrowed | Large | None, but untruthful |
| Survives v1 retirement | Yes | No | Yes |
| Cost | Highest | Throwaway | (a) plus a bridge |
| Records after rollback | Truthful; enum floor | Claim-only forever | Mislabeled forever |

## 6. Recommendation

**Choose (a), built behind gates.** #321 asks for a Cursor/Grok actor, and
(a) is the only option in which the platform, not the sender, says who sent
a message. It keeps one refresh owner, it survives v1's retirement, and it
fits decision 79: Cursor comes to v2 as its own story, not by moving v1's
workers. It is also the most work, it adds a credential and a new data
processor, its spike may sign out existing Cursor logins, and the spike can
fail.

**Fallback: no Cursor/Grok actor for now.** If Tom does not want a Cursor
credential in v2, record #321's Cursor/Grok actor as not supported in v2,
keep the Claude and Codex proofs, and stop. That is a valid answer, not a
failure of the story.

**Do not choose (b) or (c).**

## 7. Plan if (a) is chosen

Each story is bounded and serial, and records source, tested, installed and
usable states separately.

| Step | Scope | Gate before it |
| --- | --- | --- |
| G0 | Tom answers Q-321-01. | This design |
| S1 | Source: enum values, the default-off create flag, the model allowlist and effort refusal, the narrowed caller rights, the coordinator guidance, and every refusal test in section 8 that needs no CLI. No image, no credential. | G0 |
| S-C1 | The spike in section 4.1, in one disposable pod. Tom's own sign-in is the approval for this one login. | G0; Tom told the revocation risk and present to sign in |
| S2 | Pin the Cursor CLI in the agent image and in a keeper login-helper image. | S-C1 result |
| S3 | Keeper ceremony, refresh and access-only projection, default off, modeled on D-78. Or the API-key fallback if S-C1 failed and Tom approves it. | S2 |
| S4 | agentd adapter for task mode, common-rule loading, the Cursor history root and classes. | S1, S2, S3 |
| S5 | haynes-ops: the two CRD copies, then the operator pin, then keeper RBAC for the one Secret and its egress to Cursor's sign-in endpoints, then the narrow profile with Cursor API egress, then the flag. GitOps only. | S1 to S4 merged and released |
| S6 | The acceptance in section 8, by a fresh owned worker. | S5 installed |

## 8. Acceptance for (a)

**Live proof.** A fresh project-bound Cursor task Session (agent `cursor`, a
Grok model id on the allowlist that the account lists) asks a fresh
project-bound Claude coordinator one ordinary question with
`agent-run inbox ask --to`. The coordinator replies once. The Cursor Session
reads the reply and acks it. Where the topology gives the Cursor Session a
project-bound parent, the same run also proves `ask` without `--to`.
Evidence, from command output only:

- both Session names and UIDs, `agent-run show` for each (agent, model,
  parent);
- the question, reply and read-ack names and UIDs; the question's stored
  `provider: cursor` and `requestedModel`; the reply's `inReplyTo`;
- the model fields as section 3 defines them: served model `Unknown`, and
  Cursor's `init` display name verbatim, labeled as reported at session
  start;
- proof that the running Cursor CLI loaded the common and repository rules;
- the thread states from both sides: `Unanswered`, `Answered`,
  `Acknowledged`, with times;
- the Cursor Session's own log line acknowledging the reply (the model ACK);
- the operator's `InboxMessage` count before and after;
- the image's Cursor CLI version and digest; no retries under new keys.

**Refusal tests.** Each is shown failing without its guard.

1. `agent: cursor` with the flag off is refused at create, and nothing is
   written.
2. A model not on the allowlist (`auto`, a bare `grok`, a display name) is
   refused, and so is a non-empty `effort`.
3. A Claude or Codex Session's message is stored with its own provider; no
   request field can set `provider`, `requestedModel` or `actor` (strict
   decode refuses unknown fields).
4. A Cursor Session without an accepted project snapshot cannot ask or be
   asked (uniform 404), as for any Session.
5. A `cursor` Session's token cannot create a child Session, request a
   grant, post an Ask, make a task claim or a published context, or set a
   reminder.
6. The v1 pod's Client token and a coordinator host still cannot publish or
   acknowledge.
7. A replacement Cursor Session with the same name cannot read or ack the
   old Session's inbox.
8. A reply from anyone but the question's recipient is the uniform 404; a
   second, different reply sent after the first gets 409; an identical retry
   is 200 with the stored record.
9. A reply whose text claims approval changes no AccessGrant, Ask or held
   action.
10. agentd refuses a Cursor credential document that carries refresh
    material, and never runs `agent login`; with no projection present the
    Cursor CLI does not start.
11. The keeper refuses a second refresher without the Lease.
12. A `cursor` Session's history export refuses until the Cursor history
    root exists, then exports the Cursor history, and never exports the
    Cursor auth file.
13. No record puts the `init` display name or the requested model in a
    served-model field.
14. The adapter registers no MCP server.

## 9. What stays held

- No Cursor credential of any kind: no login ceremony, no API key, no token.
- No copy or read of v1's `~/.config/cursor/auth.json` or of the workbench
  HOME's login.
- No inbox ACL change, no CRD value, no flag, no haynes-ops change.
- No Cursor or Grok run, no pane read, no contact with the v1 pod.
- Decision 79 stands: the v1 Cursor workers still go offline with v1.
- #321 stays open with its status unchanged. The Claude and Codex proofs
  (journals 3999, 4013, 4031) stand on their own and do not depend on this
  choice.

Nothing above is lifted by this document, by a tracker item or by a relayed
message. Each gate in section 7 needs its own authorization.

## 10. Open questions

**Q-321-01 is pending and has not been delivered.** The #321 lead delivers
it through its verified route, one question, at the moment it chooses. Fold
the answer back here as a dated ruling.

| ID | Question | Options | Recommendation |
| --- | --- | --- | --- |
| **Q-321-01** | Should a Cursor/Grok agent get its own v2 identity, so it can ask and answer on the platform inbox as itself? | **1. Yes, build it behind gates (option a).** Source work and a one-time test login may start. Your sign-in approves that one login, and it may sign out other Cursor logins on the account. Keeper login, image and switch-on each need their own go. **2. Use the v1 pod (option b).** Its existing login, but the identity is only claimed and it ends when v1 retires. **3. Not now.** Record Cursor/Grok as not supported in v2; keep the Claude and Codex proofs. | **Option 1.** Option 3 if you want no Cursor credential in v2. Not option 2. |

Later decisions, recorded here and asked only when they arise: the API-key
fallback if the spike fails; whether a Cursor Session may be bound to a
private catalog project; the Cursor Session profile; and MCP servers for
Cursor Sessions.

## 11. Sources

- dev-env at `9a8fac11`: `internal/apiserver/inbox.go`,
  `internal/apiserver/caller.go`, `internal/apiserver/coordinator.go`,
  `internal/apiserver/validate.go`, `internal/apiserver/sessions.go`,
  `internal/apiserver/asks.go`, `internal/apiserver/grants.go`,
  `api/v1alpha1/agentsession_types.go`,
  `api/coordination/v1alpha1/inbox_types.go`,
  `internal/agentd/historyexport.go`, `internal/agentd/historyexport_layout.go`,
  `internal/agentd/historyarchive.go`, `internal/agentd/common_rules.go`,
  `internal/agentd/protocol/session.go`, `scripts/cursor-acp-session.py`,
  `docs/cursor-acp-session.md`, `docs/ask-your-coordinator.md`.
- haynes-ops [`cursor-v1-worker.md`](https://github.com/thaynes43/haynes-ops/blob/main/.agents/runbooks/cursor-v1-worker.md).
- Cursor: [CLI authentication](https://cursor.com/docs/cli/reference/authentication),
  [ACP](https://cursor.com/docs/cli/acp),
  [output format](https://cursor.com/docs/cli/reference/output-format), all
  read 2026-10-31. These are vendor statements, not observations of the
  pinned CLI; the spike confirms them.
