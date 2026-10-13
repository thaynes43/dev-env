# H7 personal identity mapping checkpoint, 2026-10-13

**Status: conditional expected mapping; current personal authentication and
installed owner-policy acceptance remain pending.** No username or email is
published here. Q-33 preserves Tom's personal Kubernetes account permanently;
Headlamp retires only after full v2 adoption. Q-34's workstation request remains
withdrawn. No command is requested from the owner.

At 22:58 UTC, one current running Omni Identity matched the exact historical
personal username from the July 29 authenticated request. Its current role was
Admin, last active 20:09:16 UTC on October 13. No same-user alias appeared.
`SysVersions.system.sidero.dev/current` reported backend v1.12.4/API 3. Its
published annotated release tag resolves to
`f9d1743a50acf90f2337a7012e8f19fb283dd9d1`. This is reported server metadata,
not attestation of the running SaaS binary or its full configuration.

Pinned primary source carries the authenticated identity unchanged through
[request authentication](https://github.com/siderolabs/omni/blob/f9d1743a50acf90f2337a7012e8f19fb283dd9d1/internal/backend/oidc/internal/storage/authrequest/storage.go#L119-L132)
and the [JWT subject](https://github.com/siderolabs/omni/blob/f9d1743a50acf90f2337a7012e8f19fb283dd9d1/internal/backend/oidc/internal/models/auth_request.go#L103-L106).
The proxy removes caller-provided impersonation headers, then sets
[`Impersonate-User` to that subject](https://github.com/siderolabs/omni/blob/f9d1743a50acf90f2337a7012e8f19fb283dd9d1/internal/backend/k8sproxy/middleware.go#L103-L133).
Admin includes the [default access group](https://github.com/siderolabs/omni/blob/f9d1743a50acf90f2337a7012e8f19fb283dd9d1/internal/backend/oidc/internal/storage/token/storage.go#L274-L363),
[`system:masters`](https://github.com/siderolabs/omni/blob/f9d1743a50acf90f2337a7012e8f19fb283dd9d1/client/pkg/constants/constants.go#L53-L56).
The one AccessPolicy read exited 1 without retained raw error text; denial
versus absence is unclassified, and additional policy-derived groups are not
established. No retry followed.

All five current Kubernetes v1.35.5 API-server Pod command observations had no
OIDC username/group flags or structured authentication configuration. No
observable local prefix change was identified. A kubeconfig context label is
not authenticated identity evidence. Headlamp's shared administrator backend
remains distinct; the actual current phone path was not observed.

Root accepts these findings only as preparation of the exact expected private
username parameter. They do not establish a current personally authenticated
Kubernetes request, verified complete groups, installed field acceptance, or
an owner-policy activation gate. Preserve the private value outside public
manifests and do not substitute Headlamp or a system identity for the owner.

Private receipts under `/home/dev/work/orders/dev-env-h7-personal-lookup-1013/`:

| Receipt | SHA-256 |
|---|---|
| `personal-identity-mapping-r2.json` (mode 0600) | `9c1d686256aeae460c8c70ef9a1833523e9b10bea2760ee649c717e605da0352` |
| `personal-identity-mapping-redacted-r2.json` | `129a035ce3f1d4fde0b0ac5242ca74f6e1f648d513ac85f73c1e3ee8cf12b478` |

The audit read metadata and pinned source only. It made no credential reads,
impersonated requests, pod execs, runtime writes, policy changes or activation.
