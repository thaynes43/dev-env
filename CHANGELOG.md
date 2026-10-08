# Changelog

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
