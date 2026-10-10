# Isolated native lifecycle fixture

Technical implementation contract for the bounded zero-task fixture. This is a
source candidate under D-84 and [design 002](002-task-budget-execution.md), not
production native-host enablement or phone acceptance.

`KubeNativeFixtureInspector` uses the operator's uncached reader. Its operator
configuration supplies one externally signature-verified published agent digest.
The immutable ledger must bind `owned-native-fixture` to the current UID of the
fixed `dev-agents/dev-env-owned-native-lifecycle` Pod and dedicated ServiceAccount
`dev-env-owned-native-fixture`. Only epoch 1, a 20-second overall, effort and
checkpoint budget, the literal zero-task success condition, and at most the exact
active native host worker are admitted. Extensions, history and managed children
refuse. Missing authority, changed identity, image, container capability, volume,
command, limit or policy refuses. The campaign deadline must also leave ten
seconds before both the actual container start plus its 100-second sleep and
the conservative Pod creation plus its 120-second lifetime. Native readiness is checked after launch by the
owned executor, avoiding a circular prelaunch readiness requirement.

The exported `NativeFixturePod` and `NativeFixturePolicy` are the exact GitOps
profile constructors. The Pod has a 120-second lifetime, no restart, one 250m/
256Mi container on a worker, bounded fresh home/tmp emptyDirs, no history or
enrollment, no injected init, no ambient credential environment and no automatic
ServiceAccount mount. Explicit operator-audience token, public API CA and the
keeper's access-only projection are the only projected inputs. Container and Pod
security contexts prohibit host access and privilege escalation. The shell creates
a fresh daemon-settings file with `updater.autoUpdateEnabled=false`, then a WIP
canary and sleeps; it never starts native work itself. Actual runtime digest
and zero container restarts are checked independently from the desired image.

The Cilium profile denies all network ingress and world, host and remote-node
egress; it adds cluster DNS and operator HTTPS only. The inspector checks live
policy **intent**. Before the sole launch, deployment acceptance must independently
verify all additive baseline/cluster policy selectors, effective datapath
enforcement, DNS/operator reachability and exclusive
trusted exec ownership. API policy presence alone does not prove enforcement.
Any unexpected admission default fails closed; it must be reviewed explicitly,
never silently normalized away. Only the scheduler's NodeName and the API's legacy
ServiceAccount alias are normalized when comparing the full Pod specification.

The dedicated factory is off by default. Explicit
`--enable-native-lifecycle-fixture` and `--native-fixture-image` select it only
with existing retained budget authority and the sole matching configured
coordinator identity/assigned campaign. Reviewed GitOps must supply retained
budget namespace/RBAC and a verified published digest. No local permit file, permissive
callback or caller-supplied `verified` boolean is authority.

The trusted acceptance orchestrator must provision and inspect the profile, create
one campaign through the real HTTPS operator API, take the exact returned binding,
and exec the signed `agentd owned-codex-host` once within the Pod's existing lifetime.
The native protocol is only initialize/initialized; no prompt, turn, enrollment,
pairing challenge or provider call is sent. The executor must observe native
0.160.1 readiness and stop/reap its exact owned process tree by the campaign latch.
Preserve the receipt and WIP canary until reviewed cleanup. No automatic retry,
deadline extension or resource increase is allowed.

This proves only local native startup and owned stop under finite authority.
Remote connectivity, account acceptance, phone pairing, actionable questions,
task turns, retained-home recovery, provider refresh and complete native usage
accounting remain separate acceptance. Unknown usage does not disable time or
attempt limits. EmptyDir preservation is process-stop evidence, not Pod-replacement
durability. The shared Flux maintenance hold must remain intact.
