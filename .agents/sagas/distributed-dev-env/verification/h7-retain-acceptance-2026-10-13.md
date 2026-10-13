# H7 history PV Retain acceptance, 2026-10-13

**Root accepts the PV Retain gate only. The single attempt is consumed.**
The reviewed client returned `AppliedVerified`, exit 0. Its own postcondition
GET observed the same bound PV and claim with `Retain` at 19:40:32 EDT
(23:40:32.254177 UTC). Independent review of the saved evidence found no issues.
Root made no additional PV request or executable invocation.

## Owner ruling and execution ownership

Q-37/D-91 is one exception to DESIGN-025 section 7's owner-only PV step, for
this exact history volume. At 19:34:06 EDT (23:34:06 UTC), Tom answered "Yes"
to the Mac conversation's explicit one-time Delete-to-Retain question naming
`dev-agents/dev-env-history-archive` and its bound PV. The source Mac preserved
and corroborated the original transcript and approval receipt. Root verified
the saved relay's exact scope and receipt bindings; the original owner turn
was not exposed in Root's thread view. This is source-corroborated relay
evidence, not a direct response observed in Root's asynchronous prompt.

The authorization was fixed before launch: verified main source
`789f2b499930dadf2cf02a440b56a20d224ae9b8`, binary SHA-256
`22867b8b277df64f6899f75ee6b58c0705b8a98b1f4cf6c1f4b03aed62dc0694`
and seven-test CAS patch SHA-256
`70d829eb6049d81a3c52be451c3b4b9502d8e34bbb9e43239f47772f19e9fd47`.
The [execution contract](h7-retain-action-contract-2026-10-13.md) records its
source review, build provenance, limits and refusal behavior.

The sole executor was the existing Mac session. It exclusively created the
start receipt, launched the verified client once with the actual approval
receipt digest, and preserved the original sanitized result. Root and its
workers did not launch a client or PATCH. The reservation, ACK, start receipt,
approval, output and execution provenance remain immutable. No second launch
or PATCH is authorized, including after a session restart or duplicate reply.

## Accepted postcondition

| Field | Preflight | Postcondition |
|---|---|---|
| PV | `pvc-ec80a337-740f-49d3-8b27-171bb6eed5af` | Same |
| PV UID | `92143b2b-e065-4511-9310-2a5d8c069192` | Same |
| Claim | `dev-agents/dev-env-history-archive` | Same |
| Claim UID | `ec80a337-740f-49d3-8b27-171bb6eed5af` | Same |
| ResourceVersion | `828181553` | `828767216` |
| Phase | `Bound` | `Bound` |
| Reclaim policy | `Delete` | `Retain` |

The client made exactly GET, PATCH, GET, all HTTP 200, with one PATCH attempt
and 8,653 response bytes total. The operation finished in about one second;
the outer execution receipt records exit 0, no timeout, no launch error, no
retry and zero extra PV requests. The report's `ProceduralReceiptOnly` field
does not claim cryptographic consent or cross-process enforcement.

This narrow client operation changed only the PV reclaim policy. It added no
permanent permission, changed no v1 or reader configuration, mounted no
archive, and activated no expiry flag. It is not a fresh observation of every
other workload: the complete guard baseline and actual admission behavior
remain separate, as do personal authenticated owner-path acceptance, F4,
both cold measurements, restore, archived reads and HOME deletion.

## Preserved evidence

Under `/home/dev/work/orders/dev-env-h7-pv-retain-execution-1013/`:

| Receipt | SHA-256 |
|---|---|
| `mac-client-result-r1.json` (unchanged, 1,671 bytes) | `036b610abb308feb2aaf270f89ad09b597034c6fc1c3df846605b83b6cbac250` |
| `mac-execution-provenance-r1.json` | `b65b597b162e5b0af9f474f2dfc727ef9f9f45bae69cc81b163de1689a9acc80` |
| `independent-q37-execution-evidence-audit-r1.json` | `2538e8bedd21d503bfe8e12d815d13d6497a3d6074efddeef9c56f057ecde398` |

The actual owner receipt is
`/home/dev/work/orders/dev-env-h7-archive-guard-1013/q37-owner-approval-mac-r1.json`,
SHA-256 `c86616cb671435d375317451d3032c8c371d8d2e0100c67181d01151624b62e1`.
The original approval evidence remains alongside it. The Root ratification is
`attempt-1-acceptance-r1.json` in the execution directory, SHA-256
`92dcdc855d026b266f699a5d615078eea89e5bb04932123a01908b03a484f087`.
No raw API body,
credential or private personal username is published here.
