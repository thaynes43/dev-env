# M6 source evidence — no runtime activation

Canonical Story 178. This file distinguishes the implementation, disposable tests
and the remaining live gates. No runtime was built, installed or accepted here.

| Head / evidence | Observed result |
| --- | --- |
| Initial `328f5ae`, CI 40024701791 | Failed actual SQL because inet text included a netmask; lint also required `Time.Equal`. Corrected, not waived. Advisory findings4364266515/4364266736 accepted and answered. |
| Corrected `ca1fc8a97598b83fcf5ccedcd7f1b39e0aad97eb`, [PostgreSQL job 120660648186](https://github.com/thaynes43/dev-env/actions/runs/40025341968/job/120660648186) | PASS under `-race`, 42 top-level tests / 229 nodes, all 13 actual SQL subtests run. One explicit API-RBAC skip in this dedicated job; required make-test job supplies API assets separately. Lint/build/generated also passed on this head. |
| Same disposable PostgreSQL job | Two acknowledged DELETEs, two resurrected rows after physical asynchronous promotion, unchanged witness, two promoted runtime pools deny old session and callback before any observer; old-primary pinned connection denies new admission. |
| Same job, actual TLS wire response suppression | Backend COMMIT CommandComplete dropped after real commit: one durable insert with unavailable caller result; successful read payload discarded; no automatic write retry. |
| Same job, legitimate login | Begin + callback 16.458586ms, nine fresh witness GETs, one admitted start. |
| Same job, protected Attention read | Seven actual store reads / 21 fresh witness GETs / two current UserInfo calls, 19.54753ms end to end. Three serial fresh GET samples average 551.538 microseconds. Loopback synthetic measurements only. |
| Later fixture correction | Isolate pending rows before 256-row cleanup fixture (advisory4364300492), assert observed login GET count, close commit-loss listener before waiting for its worker. Fresh final-head CI required. |
| Local Go 1.27.2, nice 19 / GOMAXPROCS=2 / -p 1 | Focused portal tests/vet pass, targeted transaction/current-membership race pass. Actual local PostgreSQL/API tests explicitly skip without facilities. |

Final actual head, required aggregate CI and independent native review must be
recorded in the PR and canonical story after the final correction. Root retains
that review/merge custody; advisory review is additional evidence, not native
acceptance. A cancelled/superseded run does not certify a later head.

The witness reader has no polling loop/cache/application retry and caps in-flight
GETs at two per shared fence. SQL pools cap at two connections. Automatic browser
polling remains disabled pending measured live API latency/load, replica/client
limits and an approved request budget. These loopback samples establish request
count/protocol behavior, not a live throughput/SLO or control-plane capacity.

Still unavailable: live witness/RBAC and effective runtime role, DB/auth/provider,
shared Authentik secret-safe logging, proxy/isolation/metadata boundary and Gate A.
No production failover, recovery, witness creation, grant, role/key provisioning,
image build/deployment, V1/session lifecycle or UI action was performed. Logical
restore into an unchanged server and witness/control-plane restore require CLOSED
admission and independent fresh key/generation; identity fields alone cannot
identify those operations. No runtime acceptance is inferred from these tests.
