# D-92 shared-history source, release and deployment — 2026-10-13 EDT

This records delivery of the [shared-history requirement](../requirements/2026-10-13-shared-agent-history.md).
The runtime reads remain off. Delivery of this correction is separate from
installed archived-read acceptance, F4, the cold measurements and H8.

## Source and release

[dev-env #273](https://github.com/thaynes43/dev-env/pull/273), merge
`1e179198114b465dee81d4ab0ce69168bf0bea62`, changes only the history read
scope. Existing exact configured API clients, live Sessions and configured
Coordinators may read valid histories across projects, including projectless
histories. Authentication, live Session checks and publication/write/delete
helpers are unchanged. An omitted CLI list project selects the existing
projectless bucket; it does not combine every project's index. Untrusted single
reads still return 404 before the shelf. Integrity failures still refuse.

The focused finite tests passed, then required hosted CI, native independent
review and the available advisory review passed at the final head. The
security-sensitive expansion was identified before implementation under D-92;
it grants no new identity, token, RBAC permission or runtime flag.

[Release #263](https://github.com/thaynes43/dev-env/pull/263) produced v2.28.0
at `364744c4c0b50c1a7acbc33d4a8e1a280eca3d3c`. Required CI and native
review passed; the release App's advisory job skipped and is not a review pass.
The agent publication run
[38493497971](https://github.com/thaynes43/dev-env/actions/runs/38493497971)
built, smoke-tested, signed and verified the exact release image:

```
ghcr.io/thaynes43/dev-env:2.28.0@sha256:d426ad1e17ccf4663e07ae333eaea9551342ff214beefa5acd1e26063cb2efd7
```

[Publication fix #274](https://github.com/thaynes43/dev-env/pull/274), merge
`cbf0700d07aa0de54dde89a16f733d3a412b8d00`, adds release-manifest and workflow
push paths. Manifest-changing commits take the checked-in release version,
avoiding the tag-creation race; other commits retain the Git-derived version.
Nine finite offline version-selection cases, required CI and both final reviews
passed. No publication permission or credential changed. The prior manual
dispatch returned 403; it was not retried or bypassed.

Automatic operator publication
[38495128673](https://github.com/thaynes43/dev-env/actions/runs/38495128673)
signed and verified the digest with the existing main-branch workflow identity
and GitHub Actions issuer. The Go source includes #273; the intervening fix
changes CI only. Its actual binary version is `v2.28.0-1-gcbf0700`:

```
ghcr.io/thaynes43/dev-env-operator:sha-cbf0700@sha256:e86be8a5876a159aff3fe9950ac98830b94d0f80f623466393247cddc037dced
```

## Deployment record

The operator and template are separate GitOps PRs. The template Kustomization
is an operator dependency; combining the pins would not establish operator-first
delivery. Neither PR changes the shelf or v1.

| Update | Merge and installed result |
|---|---|
| [Operator #3906](https://github.com/thaynes43/haynes-ops/pull/3906) | `2b80cadee26028302664af01b3232668c2c2b469`, merged 21:30:34 EDT; Flux Ready/Healthy, Deployment generation/observed generation 32/32, Ready 2/2 at the exact operator digest above |
| [Template #3907](https://github.com/thaynes43/haynes-ops/pull/3907) | `5c133617497fdd00cc63ff2aa4eca9dbabe9b949`, merged 21:38:37 EDT; Flux Ready/Healthy, exact agent digest above, loaded revision `2.28.0-f61d3ada0e` |

Both PRs passed required Flux checks, native independent review and the available
advisory review at their exact final heads before merging. Root reconciled the
normal Flux targets and independently checked the new operator and template
readiness. The operator-ready check preceded creation of the template PR.

Each manifest changes only its image semantically. The operator retains 500m CPU
and 256Mi memory limits, no init/ephemeral containers and no `GOMEMLIMIT` yet.
The new operator Pods were Running/Ready with zero restarts. CRD generation is
still 13; no schema update was needed. Existing auth arguments and feature flags
match the baseline: terminal recording is on; executor stop, history archive,
archived reads and HOME deletion remain off; global private admission remains off.

The final comparison preserves all five original Session identities, saved
revisions and phases, retained agent Pod identities/images/restarts, the five
protected shelf identities and its 2.9.1 image without an archive claim, the held
reader and failed Jobs, and v1's identity and images. Three existing Session
status conditions report the new available template revision; no retained Pod
is upgraded. These targeted comparisons are not a new complete guard capture
or an archived-read acceptance test.

The rollout used `act-013030-430969`, scoped to the two v2 namespaces and Flux
apps, and ended after verification. No other agent's activity was ended.

| Private receipt | SHA-256 |
|---|---|
| `dev-env-d92-deploy-1013/d92-operator-installed-verification-r1.json` | `52425ab46a35ed5a76e231e156c5e98be63cc6655b5a11149af530077c4ab5fe` |
| `dev-env-d92-template-1013/native-review-pr3907-r1.json` | `17d41e7141c664e4fdc42119d8d06fc024753fc5fe376f7cb0e293e78cad022e` |
| `dev-env-d92-template-1013/d92-template-installed-verification-r1.json` | `72485905c3f19e8f0fe2cb365720869c6c9b4aa638861a4917588f0672526197` |

Receipts remain private on the PVC. Do not infer runtime read acceptance from
source tests, publication or either successful image deployment.

## Remaining gates

The [accepted capture-r2](h7-mac-capture-r2-2026-10-13.md) remains its historical
bounded configuration/pre-mount metadata window. It is not actual admission or
lookup acceptance, a persistence/restore test, personal phone-path proof or an
archived-read proof. The held reader and failed Jobs remain preserved.

Continue H7 in the pickup brief's order. Before archive activation, retain F4's
observed-layout requirement and both bounded cold resource measurements. Use an
approved non-canary private sample only at the H8 owner gate, then prove positive
peer retrieval and a matching digest. No per-agent denied-read matrix is needed.
Stop, archive, reads and HOME deletion remain off; the shelf stays on 2.9.1
without an archive mount. The one-time PV Retain attempt remains consumed.

Private deployment evidence belongs under
`/home/dev/work/orders/dev-env-d92-deploy-1013/`, outside this public repository.
No Accepted ADR changes or new owner question IDs are part of this delivery.
