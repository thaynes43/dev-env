# Changelog

## [2.24.1](https://github.com/thaynes43/dev-env/compare/v2.24.0...v2.24.1) (2026-10-12)


### Bug Fixes

* **agentd:** recover an interrupted private project-root advance ([#220](https://github.com/thaynes43/dev-env/issues/220)) ([81ba6f6](https://github.com/thaynes43/dev-env/commit/81ba6f6af54deae79ef1e89b136cad8f3eb2efea))
* **agentd:** task source record, explicit refmap, resume after identity drift, staged task clone ([#225](https://github.com/thaynes43/dev-env/issues/225)) ([6e0d934](https://github.com/thaynes43/dev-env/commit/6e0d93480eca5059fcb463b8f329418f5f3a8b66))
* **apiserver:** coordination fences, GitHub case, directory Unknown and object caps ([#223](https://github.com/thaynes43/dev-env/issues/223)) ([6644d8f](https://github.com/thaynes43/dev-env/commit/6644d8f8cb9b6f6dd757d8cc3928eff61dfad8ef))
* **canary-result:** keep retained canary results readable; once-only client fences ([#219](https://github.com/thaynes43/dev-env/issues/219)) ([3753b13](https://github.com/thaynes43/dev-env/commit/3753b1396a161253e35b76c04ef917262daae4b9))
* refuse canary prompts that repeat bound rules; widen the guard scan ([#229](https://github.com/thaynes43/dev-env/issues/229)) ([31fb105](https://github.com/thaynes43/dev-env/commit/31fb10593e28d139b01e627184f40ab405bab921))
* **taskbudget:** confirm accepted ledger writes, bound history, tolerate skew, close unlaunched children ([#221](https://github.com/thaynes43/dev-env/issues/221)) ([0a63581](https://github.com/thaynes43/dev-env/commit/0a6358133de1bcdb93b245783d0392be8d3d284b))
* truthful canary task result, resumable known non-attempt, capture gap freeze ([#222](https://github.com/thaynes43/dev-env/issues/222)) ([538c4f6](https://github.com/thaynes43/dev-env/commit/538c4f6e771a4fdeeb09586b560a5d2f4efe647c))

## [2.24.0](https://github.com/thaynes43/dev-env/compare/v2.23.0...v2.24.0) (2026-10-12)


### Features

* **controller:** prove original private worker ownership without admission ([#213](https://github.com/thaynes43/dev-env/issues/213)) ([b3aa4d9](https://github.com/thaynes43/dev-env/commit/b3aa4d93aa72afdec72ddd97a2303eb5860c368a))

## [2.23.0](https://github.com/thaynes43/dev-env/compare/v2.22.0...v2.23.0) (2026-10-12)


### Features

* **agent-run:** add bounded named session reads ([#210](https://github.com/thaynes43/dev-env/issues/210)) ([4c1e0c3](https://github.com/thaynes43/dev-env/commit/4c1e0c37cb228d54556237276b09b502914c027b))

## [2.22.0](https://github.com/thaynes43/dev-env/compare/v2.21.0...v2.22.0) (2026-10-12)


### Features

* **canary:** read retained probe results for initiating clients ([#208](https://github.com/thaynes43/dev-env/issues/208)) ([cd966d1](https://github.com/thaynes43/dev-env/commit/cd966d17c8d1a2bca82c27cc01be4d9a11ae5256))

## [2.21.0](https://github.com/thaynes43/dev-env/compare/v2.20.0...v2.21.0) (2026-10-11)


### Features

* **cli:** add single-request ID-only canary creation ([#206](https://github.com/thaynes43/dev-env/issues/206)) ([562d7df](https://github.com/thaynes43/dev-env/commit/562d7df2a059eb268a879edbb7c32fdda4065d47))
* **cli:** add single-request session directory reads ([#204](https://github.com/thaynes43/dev-env/issues/204)) ([c9493db](https://github.com/thaynes43/dev-env/commit/c9493db0ab9f5b899e78e5617b650f0bd7077d9e))

## [2.20.0](https://github.com/thaynes43/dev-env/compare/v2.19.0...v2.20.0) (2026-10-11)


### Features

* retain bounded private session directory metadata ([#199](https://github.com/thaynes43/dev-env/issues/199)) ([7fa1705](https://github.com/thaynes43/dev-env/commit/7fa1705e4981632f15580996c303848930913dbd))
* retain truthful initial invocation usage provenance ([#200](https://github.com/thaynes43/dev-env/issues/200)) ([a0ba2ee](https://github.com/thaynes43/dev-env/commit/a0ba2ee413d38e8368e99f6bb95809d85b214aff))


### Bug Fixes

* **controller:** retain protected private executor pods ([#202](https://github.com/thaynes43/dev-env/issues/202)) ([a44b5b1](https://github.com/thaynes43/dev-env/commit/a44b5b1e0b92b4c1a146254e03202617a58b7a01))

## [2.19.0](https://github.com/thaynes43/dev-env/compare/v2.18.0...v2.19.0) (2026-10-11)


### Features

* **agentd:** retain bounded partial managed output context ([#198](https://github.com/thaynes43/dev-env/issues/198)) ([24960dc](https://github.com/thaynes43/dev-env/commit/24960dc128b219c48f4a32ba9f25e778f20e9a66))
* **agentd:** retain common rules and private canary instruction inputs ([#195](https://github.com/thaynes43/dev-env/issues/195)) ([f1bb6ca](https://github.com/thaynes43/dev-env/commit/f1bb6ca26937557f6c823fc74b0ba385661f360a))

## [2.18.0](https://github.com/thaynes43/dev-env/compare/v2.17.0...v2.18.0) (2026-10-11)


### Features

* **coordination:** retain durable offline inbox and read acknowledgements ([#194](https://github.com/thaynes43/dev-env/issues/194)) ([f02d0ad](https://github.com/thaynes43/dev-env/commit/f02d0ad276d007105795dd8611d0725c5176869b))
* **operator:** admit one-attempt private managed rule canaries ([#191](https://github.com/thaynes43/dev-env/issues/191)) ([c062e98](https://github.com/thaynes43/dev-env/commit/c062e9897d95aa5da7643250a5b9811c4ecc8cae))

## [2.17.0](https://github.com/thaynes43/dev-env/compare/v2.16.0...v2.17.0) (2026-10-11)


### Features

* **coordination:** publish immutable context with scoped stopped reads ([#190](https://github.com/thaynes43/dev-env/issues/190)) ([5cfcf0e](https://github.com/thaynes43/dev-env/commit/5cfcf0e02f9d16d11b3a9d92bd9f56fde748d3d2))

## [2.16.0](https://github.com/thaynes43/dev-env/compare/v2.15.1...v2.16.0) (2026-10-11)


### Features

* **agentd:** guard private native Codex host preparation ([#185](https://github.com/thaynes43/dev-env/issues/185)) ([284e697](https://github.com/thaynes43/dev-env/commit/284e69762c48edc1370b12273d91ebbc758e436f))
* **coordination:** retain atomic explicit task ownership ([#187](https://github.com/thaynes43/dev-env/issues/187)) ([d95e8fb](https://github.com/thaynes43/dev-env/commit/d95e8fbe7b3e1c07eed75959819cd35f90a155c4))

## [2.15.1](https://github.com/thaynes43/dev-env/compare/v2.15.0...v2.15.1) (2026-10-11)


### Bug Fixes

* **agentd:** preserve owned wrapper modes under private umask ([#186](https://github.com/thaynes43/dev-env/issues/186)) ([9c5cd87](https://github.com/thaynes43/dev-env/commit/9c5cd87fadacee98fd79edc97c6a74def76b198b))

## [2.15.0](https://github.com/thaynes43/dev-env/compare/v2.14.0...v2.15.0) (2026-10-11)


### Features

* **controller:** retain private provider homes and session identity ([#183](https://github.com/thaynes43/dev-env/issues/183)) ([af80cfc](https://github.com/thaynes43/dev-env/commit/af80cfc8bc2bcb4cb28290ac110eff924308be76))

## [2.14.0](https://github.com/thaynes43/dev-env/compare/v2.13.0...v2.14.0) (2026-10-11)


### Features

* **agentd:** prepare owned private project roots and pinned anchors ([#181](https://github.com/thaynes43/dev-env/issues/181)) ([e34ad2c](https://github.com/thaynes43/dev-env/commit/e34ad2c07ab2c93b77e14f882adb058251ff8f90))

## [2.13.0](https://github.com/thaynes43/dev-env/compare/v2.12.0...v2.13.0) (2026-10-11)


### Features

* **agentd:** bootstrap trusted private project catalogs ([#179](https://github.com/thaynes43/dev-env/issues/179)) ([951eeaf](https://github.com/thaynes43/dev-env/commit/951eeafa43871825e6eb00ac2240fa31970b86ee))

## [2.12.0](https://github.com/thaynes43/dev-env/compare/v2.11.0...v2.12.0) (2026-10-10)


### Features

* prepare declared projects in private task worktrees ([#178](https://github.com/thaynes43/dev-env/issues/178)) ([7a634e5](https://github.com/thaynes43/dev-env/commit/7a634e5ec7ff8ffa91e504a83defe258dacca85c))


### Bug Fixes

* **taskbudget:** pin fixture Codex state to its private home ([#170](https://github.com/thaynes43/dev-env/issues/170)) ([3e16e2f](https://github.com/thaynes43/dev-env/commit/3e16e2ff96744d0b3ae3ce366b3a0f7bffb84cab))

## [2.11.0](https://github.com/thaynes43/dev-env/compare/v2.10.0...v2.11.0) (2026-10-10)


### Features

* **agentd:** add disabled owned foreground Codex executor ([#162](https://github.com/thaynes43/dev-env/issues/162)) ([a196280](https://github.com/thaynes43/dev-env/commit/a196280dba966ffa9ec84828bd775a7578c2ce31))
* **agentd:** wire owned host HTTPS gate and authenticated readiness ([#166](https://github.com/thaynes43/dev-env/issues/166)) ([d47bd0d](https://github.com/thaynes43/dev-env/commit/d47bd0d0d1e0be010ddf564a6f399c5c12c0145b))
* enforce finite budgets for managed executors ([#167](https://github.com/thaynes43/dev-env/issues/167)) ([079c0a2](https://github.com/thaynes43/dev-env/commit/079c0a227431b8ed7d15ea75c311e1e21c84a4ae))
* **operator:** admit isolated native lifecycle fixture ([#168](https://github.com/thaynes43/dev-env/issues/168)) ([cc8ba3c](https://github.com/thaynes43/dev-env/commit/cc8ba3cdace348befced9a039ffd188086b7dca4))
* retain logical task budgets and gate assigned host work ([#163](https://github.com/thaynes43/dev-env/issues/163)) ([53d7dea](https://github.com/thaynes43/dev-env/commit/53d7dea7531e1640a90333562af3a80c605cbead))

## [2.10.0](https://github.com/thaynes43/dev-env/compare/v2.9.1...v2.10.0) (2026-10-10)


### Features

* add disabled shared project catalog core ([#145](https://github.com/thaynes43/dev-env/issues/145)) ([084b66a](https://github.com/thaynes43/dev-env/commit/084b66ac064e337e361713ae6dd7d73bd72290d4))
* add retained Codex host lifecycle and recovery status ([#157](https://github.com/thaynes43/dev-env/issues/157)) ([ed64941](https://github.com/thaynes43/dev-env/commit/ed64941d6d91c0e32727f9f2f50eb5ebc256f4e2))
* guard opt-in shared workspace ownership ([#135](https://github.com/thaynes43/dev-env/issues/135)) ([316c03c](https://github.com/thaynes43/dev-env/commit/316c03c08a4ef174aa60d53d86ca7f509eb3a566))
* prepare bounded Git wait observations ([#146](https://github.com/thaynes43/dev-env/issues/146)) ([a5ae4a0](https://github.com/thaynes43/dev-env/commit/a5ae4a09cf89c49c185b35e11d3284fe8c7e698d))
* record authoritative child answers and resume exact Codex threads ([#155](https://github.com/thaynes43/dev-env/issues/155)) ([43972bd](https://github.com/thaynes43/dev-env/commit/43972bd8a725aaa1918fb2e938ee6bf550e84999))
* rescue shared tasks after verified executor stop ([#140](https://github.com/thaynes43/dev-env/issues/140)) ([7b3ce4c](https://github.com/thaynes43/dev-env/commit/7b3ce4cd4fbf314fd8633764d914401961dabfab))


### Bug Fixes

* **deps:** update module golang.org/x/crypto to v0.58.0 ([#122](https://github.com/thaynes43/dev-env/issues/122)) ([41e2497](https://github.com/thaynes43/dev-env/commit/41e2497f70808604a6f881d2239eaf77c1dea63f))
* publish Git wait fixture from reviewed main inputs ([#151](https://github.com/thaynes43/dev-env/issues/151)) ([0e0931a](https://github.com/thaynes43/dev-env/commit/0e0931ad3e4a733f3a4064251b9b548c164abcab))
* **trials:** retain diagnostics on helper interruption ([#139](https://github.com/thaynes43/dev-env/issues/139)) ([a222a69](https://github.com/thaynes43/dev-env/commit/a222a69311940bc7efd3b8b1f58352e7ddc40cdc))

## [2.9.1](https://github.com/thaynes43/dev-env/compare/v2.9.0...v2.9.1) (2026-10-09)


### Bug Fixes

* **deps:** update module github.com/prometheus/client_golang to v1.25.0 ([#121](https://github.com/thaynes43/dev-env/issues/121)) ([4a55a5d](https://github.com/thaynes43/dev-env/commit/4a55a5dc4002519340655efa1e81b5fe467fd43f))
* require current source for new tasks and document dev-env workflows ([#124](https://github.com/thaynes43/dev-env/issues/124)) ([759f084](https://github.com/thaynes43/dev-env/commit/759f084f52099cda258b5cd9f1a0eebecce5d284))

## [2.9.0](https://github.com/thaynes43/dev-env/compare/v2.8.0...v2.9.0) (2026-10-08)


### Features

* add disabled keeper-owned Proxmox credential backend ([#111](https://github.com/thaynes43/dev-env/issues/111)) ([eeb15e3](https://github.com/thaynes43/dev-env/commit/eeb15e37ffdcc990f8ae780b7c73e7ce4b166e25))


### Bug Fixes

* omit agent credential mounts from rescue holds ([#108](https://github.com/thaynes43/dev-env/issues/108)) ([d4c48bc](https://github.com/thaynes43/dev-env/commit/d4c48bca5cbc0f0f114d14bf9a8b3194c67f09f3))

## [2.8.0](https://github.com/thaynes43/dev-env/compare/v2.7.1...v2.8.0) (2026-10-08)


### Features

* **cli:** connect from laptop kubeconfig through verified TLS ([#103](https://github.com/thaynes43/dev-env/issues/103)) ([12a97c4](https://github.com/thaynes43/dev-env/commit/12a97c402917d2fbd01ff78f114498411fb63a5d))

## [2.7.1](https://github.com/thaynes43/dev-env/compare/v2.7.0...v2.7.1) (2026-10-08)


### Bug Fixes

* **deps:** update module github.com/prometheus/client_golang to v1.24.1 ([#99](https://github.com/thaynes43/dev-env/issues/99)) ([7e3b3ef](https://github.com/thaynes43/dev-env/commit/7e3b3ef66812de044a0d8dbeaaac40e62abc0a90))

## [2.7.0](https://github.com/thaynes43/dev-env/compare/v2.6.0...v2.7.0) (2026-10-08)


### Features

* rescues on a shelf pod: list, restore and pruning (D-67) ([#92](https://github.com/thaynes43/dev-env/issues/92)) ([035dfaa](https://github.com/thaynes43/dev-env/commit/035dfaa3b34777a04f2d643863bd7b0845ab132c))

## [2.6.0](https://github.com/thaynes43/dev-env/compare/v2.5.0...v2.6.0) (2026-10-07)


### Features

* **api,agentd,agent-run:** messages and logs (D-65) ([#85](https://github.com/thaynes43/dev-env/issues/85)) ([953f786](https://github.com/thaynes43/dev-env/commit/953f78686f81880c447b580853b7122b1e2eb45f))
* **api,operator,agent-run:** declare-activity on the API (D-66) ([#87](https://github.com/thaynes43/dev-env/issues/87)) ([44fff0f](https://github.com/thaynes43/dev-env/commit/44fff0feb2750794ad2f37db401f0318eea7b83d))

## [2.5.0](https://github.com/thaynes43/dev-env/compare/v2.4.0...v2.5.0) (2026-10-07)


### Features

* **api,agent-run:** a session's own timers at create (D-60) ([#82](https://github.com/thaynes43/dev-env/issues/82)) ([1d6cc53](https://github.com/thaynes43/dev-env/commit/1d6cc5395f82b9ef44a1a090c201d174cbeedd0e))
* **broker,agentd:** install kube grants in session tmpfs (D-63) ([0af613d](https://github.com/thaynes43/dev-env/commit/0af613d0660ba6a338905736bd47bd7c5c2a0504))
* **broker,operator:** egress grants and expiry backstop (D-64) ([763fe77](https://github.com/thaynes43/dev-env/commit/763fe77880b066e6d79cc2460832f1a017712812))
* **broker:** the access broker mode, kube and break-glass grants (plan 07 step 3) ([#74](https://github.com/thaynes43/dev-env/issues/74)) ([2ed524f](https://github.com/thaynes43/dev-env/commit/2ed524fdacff0deb31540fface56e005c0a0fa23))
* **operator:** the archive timer of a suspended session (D-62) ([#78](https://github.com/thaynes43/dev-env/issues/78)) ([aaa9c93](https://github.com/thaynes43/dev-env/commit/aaa9c93bab8555cb7bfa6a8237377b01e6c8db57))

## [2.4.0](https://github.com/thaynes43/dev-env/compare/v2.3.0...v2.4.0) (2026-10-07)


### Features

* **agentd:** idle detection, Claude's status and the newest activity (D-59) ([#75](https://github.com/thaynes43/dev-env/issues/75)) ([ecb977d](https://github.com/thaynes43/dev-env/commit/ecb977dde43b526e34dd20ec82b7eedb0addbb5e))
* **operator,api,agent-run:** suspend, resume and the idle timer (D-60) ([#76](https://github.com/thaynes43/dev-env/issues/76)) ([3e103b4](https://github.com/thaynes43/dev-env/commit/3e103b46f26940ee6879b4a2598ff8e0f288fc83))

## [2.3.0](https://github.com/thaynes43/dev-env/compare/v2.2.0...v2.3.0) (2026-10-07)


### Features

* **agentd,api,agent-run:** local sessions, resume on boot, attach (D-58) ([#73](https://github.com/thaynes43/dev-env/issues/73)) ([dbf32dc](https://github.com/thaynes43/dev-env/commit/dbf32dc235ddbb453337c30f8c721a9b1b4cc535))
* **api:** /v1/grants, request, list, show and release (plan 07 step 2) ([#72](https://github.com/thaynes43/dev-env/issues/72)) ([06841c8](https://github.com/thaynes43/dev-env/commit/06841c83ac5a3bfcf22377cb3bf4c786e6007abc))
* **api:** AccessGrant and GrantPolicy CRDs (plan 07 step 1) ([#66](https://github.com/thaynes43/dev-env/issues/66)) ([e588f29](https://github.com/thaynes43/dev-env/commit/e588f294d1aae983c3ce7354f6f52bcd354db46c))
* **operator:** serve the RescueFailed metric for the page (D-57) ([#69](https://github.com/thaynes43/dev-env/issues/69)) ([6cd7adc](https://github.com/thaynes43/dev-env/commit/6cd7adcfae04297d33f91591825b6295f918103b))

## [2.2.0](https://github.com/thaynes43/dev-env/compare/v2.1.0...v2.2.0) (2026-10-07)


### Features

* **operator,agentd:** a hold pod rescues a volume that has no pod (D-55) ([#68](https://github.com/thaynes43/dev-env/issues/68)) ([ef6660b](https://github.com/thaynes43/dev-env/commit/ef6660b93771df4217d9e72409e73a400709f703))

## [2.1.0](https://github.com/thaynes43/dev-env/compare/v2.0.1...v2.1.0) (2026-10-07)


### Features

* **images:** carry upstream license texts and THIRD_PARTY.md ([#64](https://github.com/thaynes43/dev-env/issues/64)) ([fac4751](https://github.com/thaynes43/dev-env/commit/fac47516763718465558a1b8fd191f67a3f5ac6f))

## [2.0.1](https://github.com/thaynes43/dev-env/compare/v2.0.0...v2.0.1) (2026-10-07)


### Bug Fixes

* **deps:** update k8s.io/utils digest to cf1189d ([#47](https://github.com/thaynes43/dev-env/issues/47)) ([51bfdbf](https://github.com/thaynes43/dev-env/commit/51bfdbfcc670510a3be15bc358dd43fbc0cd103b))
* **deps:** update module github.com/go-logr/logr to v1.4.4 ([#50](https://github.com/thaynes43/dev-env/issues/50)) ([7d74713](https://github.com/thaynes43/dev-env/commit/7d747131c188d1d87c80c0d07d72e2f54b95931c))
* **deps:** update sigs.k8s.io/json digest to 11ed52e ([#48](https://github.com/thaynes43/dev-env/issues/48)) ([195a5b3](https://github.com/thaynes43/dev-env/commit/195a5b334f867611c74fff846b9c663751f2b9d1))

## 2.0.0 (2026-10-07)


### Features

* **agent-run:** -p, list, show, reap and fleet against the /v1 API (plan 01 step 7) ([#34](https://github.com/thaynes43/dev-env/issues/34)) ([d25e453](https://github.com/thaynes43/dev-env/commit/d25e4537da2ac21b1dd53cdaa28df8b20be0ed37))
* **agentd:** clone, task in tmux, heartbeat, ctl status (plan 01 step 4, part 2) ([#27](https://github.com/thaynes43/dev-env/issues/27)) ([7a5446a](https://github.com/thaynes43/dev-env/commit/7a5446aaa8457a71208733d72d45afac4fcd7c91))
* **agentd:** config rendering, the port of dev-init.sh (plan 01 step 4, part 1) ([#25](https://github.com/thaynes43/dev-env/issues/25)) ([f424372](https://github.com/thaynes43/dev-env/commit/f42437264e28d4ce6185e88e2daeeec7623e13c2))
* **agentd:** ctl rescue (plan 01 step 4, part 3) ([#28](https://github.com/thaynes43/dev-env/issues/28)) ([02e8ab2](https://github.com/thaynes43/dev-env/commit/02e8ab2f46896f0faa34f69c68c53a3d2eb6e6f7))
* **agentd:** ctl rescue writes the bundle to the shared volume (plan 01 step 5, part 1) ([#32](https://github.com/thaynes43/dev-env/issues/32)) ([3c9b401](https://github.com/thaynes43/dev-env/commit/3c9b4016ab9e8447169436cbe8c044980bb4e76d))
* **api:** AgentSession schema rules and an envtest suite (plan 01 step 1) ([#24](https://github.com/thaynes43/dev-env/issues/24)) ([879e818](https://github.com/thaynes43/dev-env/commit/879e81830f226c76dd6ca11c2a8ccaa0630c1123))
* **api:** the /v1 API with TokenReview auth (plan 01 step 3) ([#31](https://github.com/thaynes43/dev-env/issues/31)) ([4112e6a](https://github.com/thaynes43/dev-env/commit/4112e6a67e6be1b12e0b82f125467905e6db0f3e))
* Go skeleton for dev-env v2 (KICKOFF B1) ([#14](https://github.com/thaynes43/dev-env/issues/14)) ([d9ace8e](https://github.com/thaynes43/dev-env/commit/d9ace8e53da2cd8d84258f398dc46d3a80e53120))
* **image:** the v2 agent image, built and smoke-tested in CI, published from a release tag (KICKOFF B5, D-53) ([#40](https://github.com/thaynes43/dev-env/issues/40)) ([4af8f81](https://github.com/thaynes43/dev-env/commit/4af8f81392e7fa29907c2d4373b2ce7cf1d848e9))
* **keeper:** mint the gh token every 40 minutes (plan 01 step 6) ([#39](https://github.com/thaynes43/dev-env/issues/39)) ([34e782e](https://github.com/thaynes43/dev-env/commit/34e782e8f6bfcc7899649688a7b67ad2690ad6b6))
* **operator:** deleting a session is a reap; one guarded pod delete (plan 01 step 2, part 2) ([#30](https://github.com/thaynes43/dev-env/issues/30)) ([7313a21](https://github.com/thaynes43/dev-env/commit/7313a210022f33d677500220e062e415e55859bc))
* **operator:** rescue before a suspend deletes the pod, archive after a verified rescue (plan 01 step 5, part 2) ([#36](https://github.com/thaynes43/dev-env/issues/36)) ([2149163](https://github.com/thaynes43/dev-env/commit/2149163456d029aa14ce1516e81dd8727a5f2aa3))
* **operator:** session pods and volumes from dev-env-templates (plan 01 step 2, part 1) ([#29](https://github.com/thaynes43/dev-env/issues/29)) ([9e3dc78](https://github.com/thaynes43/dev-env/commit/9e3dc78b3406d2a928bb9bfde4145b5668b1b9fe))
