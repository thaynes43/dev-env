# H7: unmounted history archive PVC, 2026-10-13

**Installed PVC unit verified at 17:24 UTC; PV Retain accepted later under Q-37/D-91.**
Archive activation remains pending. The [Retain acceptance](h7-retain-acceptance-2026-10-13.md)
records the sole consumed operation and client postcondition at 19:40:32 EDT
(23:40:32.254177 UTC); no extra PV GET was made.
[haynes-ops #3898](https://github.com/thaynes43/haynes-ops/pull/3898) merged at
17:22 UTC as `a287a4f6ac8455076af0defee9383dcacb61a649`, after required
`Flux Local - Success`, `Diff Scope - Success`, native independent review of
`04139d9d2a5f9f9c70d89deeb092f0e6f97708ed` and an advisory review with no findings.

The source adds one PVC in the existing storage-only Kustomization and corrects
its comment. Flux source and `dev-env-system/dev-env-shared` applied the merge;
the storage Kustomization is Ready and Healthy.

| Check | Result at 17:24 UTC |
|---|---|
| Claim | `dev-agents/dev-env-history-archive`, Bound, 20Gi, `ceph-block`, ReadWriteOnce |
| Pruning | Claim annotation disabled; existing storage Kustomization remains `prune: false` |
| Consumers | No reference among 481 readable Pods at the verification time |
| Bound volume | `pvc-ec80a337-740f-49d3-8b27-171bb6eed5af`; claim UID matches |
| Reclaim policy | `Delete` at this snapshot; later `Retain` verified under Q-37/D-91 (above) |
| Shelf | Same UID, image 2.9.1, RollingUpdate and shared claim only |
| Retained runtime | Same selected fields for 2 operator Pods, 1 shelf Pod, 3 canary Pods, 5 AgentSessions and 2 failed Jobs; the 2 previously absent canary Pods remain absent |
| Expiry flags | Terminal record on; stop, archive, history reads and delete off |

The unit creates no archive mount or archive data. The next ordered unit is the
archive guard before the shelf mount. The later read-only finding raised
[Q-35](../designs/025-private-home-expiry.md#20-h7-archive-trust-boundary-q-35);
its relayed owner answer is now recorded in
[ADR-007](../adrs/007-preserve-v1-archive-trust-boundary.md). That ruling governs
the next compatible guard; it changes none of this installed PVC evidence.
F4, both cold resource measurements and the remaining H7 policies and flag
gates still apply. The [one-time action](h7-retain-action-contract-2026-10-13.md)
has completed and is consumed; it authorizes no repeat or other PV change.
This is not H8's installed history-read acceptance.

Activity `act-172203-248390` covered provisioning and reconciliation in
`dev-agents`, `dev-env-shared` and `rook-ceph`; it was ended after verification.
The other agent's media activity was outside that scope and left alone.

Evidence is retained under `/home/dev/work/orders/dev-env-h7-archive-pvc-1013/`:
the author baseline and narrow render/schema checks, native independent review,
safe pre-deploy baseline and `native-postdeploy-verification-r1.json`. The latter
ran from 17:24:32 to 17:24:34 UTC, SHA-256
`4fa10f8e3003b4e35b96eecdefb943ff8867f891fc0e411eeec705341ace77e3`.
The coordinator also checked the Bound claim, applied merge and shelf fields.
No credentials, private account identity or transcript contents were read or copied.
