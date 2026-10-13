# H7: unmounted history archive PVC, 2026-10-13

**Installed PVC unit verified at 17:24 UTC; archive activation remains pending.**
[haynes-ops #3898](https://github.com/thaynes43/haynes-ops/pull/3898) merged at
17:22 UTC as `a287a4f6ac8455076af0defee9383dcacb61a649`, after required
`Flux Local - Success`, `Diff Scope - Success`, native independent review of
`04139d9d2a5f9f9c70d89deeb092f0e6f97708ed` and an advisory review with no findings.

The source adds one PVC in the existing storage-only Kustomization and corrects
its comment. Flux source and `dev-env-system/dev-env-shared` applied the merge;
the storage Kustomization is Ready and Healthy.

| Check | Result |
|---|---|
| Claim | `dev-agents/dev-env-history-archive`, Bound, 20Gi, `ceph-block`, ReadWriteOnce |
| Pruning | Claim annotation disabled; existing storage Kustomization remains `prune: false` |
| Consumers | No reference among 481 readable Pods at the verification time |
| Bound volume | `pvc-ec80a337-740f-49d3-8b27-171bb6eed5af`; claim UID matches |
| Reclaim policy | **Delete; owner-set Retain remains pending before archive activation** |
| Shelf | Same UID, image 2.9.1, RollingUpdate and shared claim only |
| Retained runtime | Same selected fields for 2 operator Pods, 1 shelf Pod, 3 canary Pods, 5 AgentSessions and 2 failed Jobs; the 2 previously absent canary Pods remain absent |
| Expiry flags | Terminal record on; stop, archive, history reads and delete off |

The unit creates no archive mount or archive data. Next is the archive guard,
before the shelf mount. F4, both cold resource measurements, the bound PV's
owner-set Retain and the remaining H7 policies and flag gates still apply.
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
