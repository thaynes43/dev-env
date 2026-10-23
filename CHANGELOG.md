# Changelog

## [2.43.0](https://github.com/thaynes43/dev-env/compare/v2.42.0...v2.43.0) (2026-10-23)


### Features

* **maintenance:** add offline upgrade-shepherd correlation check ([#520](https://github.com/thaynes43/dev-env/issues/520)) ([b972bcc](https://github.com/thaynes43/dev-env/commit/b972bccfb48fa1c10a30ee6785a3f5ef161a56bc))

## [2.42.0](https://github.com/thaynes43/dev-env/compare/v2.41.0...v2.42.0) (2026-10-23)


### Features

* **usage:** capture direct-child receipts on the parent ([#517](https://github.com/thaynes43/dev-env/issues/517)) ([09ab4ee](https://github.com/thaynes43/dev-env/commit/09ab4ee9510dff21c7964a2807062da60d613962))

## [2.41.0](https://github.com/thaynes43/dev-env/compare/v2.40.0...v2.41.0) (2026-10-23)


### Features

* **fleet:** report bound Pod resource specs on opt-in ([#514](https://github.com/thaynes43/dev-env/issues/514)) ([5437434](https://github.com/thaynes43/dev-env/commit/5437434bdbd9d2a574e936d901d3b3b6ddd3067d))

## [2.40.0](https://github.com/thaynes43/dev-env/compare/v2.39.1...v2.40.0) (2026-10-22)


### Features

* expose read-only standby observation metrics ([#512](https://github.com/thaynes43/dev-env/issues/512)) ([ebc5111](https://github.com/thaynes43/dev-env/commit/ebc51111f0d5c10bcf32f94c74963661c2ce0a45))

## [2.39.1](https://github.com/thaynes43/dev-env/compare/v2.39.0...v2.39.1) (2026-10-22)


### Bug Fixes

* **keeper:** a bad notice flag disables notices instead of stopping the keeper ([#505](https://github.com/thaynes43/dev-env/issues/505)) ([4ebf457](https://github.com/thaynes43/dev-env/commit/4ebf457764e9ccdf7d1651671aa70a221d245936))

## [2.39.0](https://github.com/thaynes43/dev-env/compare/v2.38.3...v2.39.0) (2026-10-20)


### Features

* **keeper:** login lapse gauges, lapse date in auth status, and keeper Pushover notices ([#495](https://github.com/thaynes43/dev-env/issues/495)) ([0d2691d](https://github.com/thaynes43/dev-env/commit/0d2691d58bdb6684c174714957448bc8d2432595))

## [2.38.3](https://github.com/thaynes43/dev-env/compare/v2.38.2...v2.38.3) (2026-10-20)


### Bug Fixes

* **standby:** an agent report made while agentd boots is not an ended agent ([#489](https://github.com/thaynes43/dev-env/issues/489)) ([9a70ff9](https://github.com/thaynes43/dev-env/commit/9a70ff96a40203c718daa326f47b7ed5ee07abbe))

## [2.38.2](https://github.com/thaynes43/dev-env/compare/v2.38.1...v2.38.2) (2026-10-20)


### Bug Fixes

* **agentd:** one login lock per pod so a second ceremony cannot start ([#443](https://github.com/thaynes43/dev-env/issues/443) D1) ([#485](https://github.com/thaynes43/dev-env/issues/485)) ([73d050f](https://github.com/thaynes43/dev-env/commit/73d050f9eb7359eeb1c7db891847d83d67918cdd))

## [2.38.1](https://github.com/thaynes43/dev-env/compare/v2.38.0...v2.38.1) (2026-10-20)


### Bug Fixes

* **apiserver:** cancel a Claude keeper-login ceremony with a fixed abandon command (agentd claude-login-abandon) ([#483](https://github.com/thaynes43/dev-env/issues/483)) ([ed156f0](https://github.com/thaynes43/dev-env/commit/ed156f0195854956f004cd883e01d7d4061f0998))
* **keeper:** accept PVE's string-typed create response and log ambiguous mints ([#481](https://github.com/thaynes43/dev-env/issues/481)) ([a04bcdf](https://github.com/thaynes43/dev-env/commit/a04bcdfe0e9ddaa943726e028c173da621c18bd6))
* **standby:** no second standby while the first drains for a revision move ([#480](https://github.com/thaynes43/dev-env/issues/480)) ([080e7b5](https://github.com/thaynes43/dev-env/commit/080e7b553c3f46508551ccacc00f2817a451e0be))

## [2.38.0](https://github.com/thaynes43/dev-env/compare/v2.37.0...v2.38.0) (2026-10-20)


### Features

* **apiserver:** renew the keeper login from a workbench session (agent-run keeper-login) ([#475](https://github.com/thaynes43/dev-env/issues/475)) ([fe48aa1](https://github.com/thaynes43/dev-env/commit/fe48aa1e95353e3a79da815d1c8dbbf86ada8fe7))


### Bug Fixes

* **agentd:** abandon a stalled clone so the retry runs instead of leaving staging ([#478](https://github.com/thaynes43/dev-env/issues/478)) ([33fb2ec](https://github.com/thaynes43/dev-env/commit/33fb2ec20cfe0b886ed8a3aa1623e0856356aea4))
* **agentd:** recognise the real Claude CLI 401 in the continue classifier ([#473](https://github.com/thaynes43/dev-env/issues/473)) ([ed9f15e](https://github.com/thaynes43/dev-env/commit/ed9f15ebcb4dcfcfe1839f4cc5c87ee9bbadce48))
* **apiserver:** accept a workbench session on the coordination routes ([#472](https://github.com/thaynes43/dev-env/issues/472)) ([b820538](https://github.com/thaynes43/dev-env/commit/b8205387ac50611c6b424cb9ec48c27ab5ab5a3c))
* **keeper:** offer only the pinned SSH host-key algorithms per target ([#474](https://github.com/thaynes43/dev-env/issues/474)) ([ebe2da6](https://github.com/thaynes43/dev-env/commit/ebe2da6b4b20ca8186c9dbeb6f3321d0ad633130))

## [2.37.0](https://github.com/thaynes43/dev-env/compare/v2.36.0...v2.37.0) (2026-10-19)


### Features

* **agentd:** report the served model for Codex tasks ([#465](https://github.com/thaynes43/dev-env/issues/465)) ([dd6d289](https://github.com/thaynes43/dev-env/commit/dd6d2895227d2c2d4391330fe6d54f34aa6e9ebc))
* **operator:** keep one standby remote Claude session ready (default off) ([#466](https://github.com/thaynes43/dev-env/issues/466)) ([123bedc](https://github.com/thaynes43/dev-env/commit/123bedc22e213bddce406108c6c3a120d53b8ee8))


### Bug Fixes

* **apiserver:** let a workbench session create a Codex host ([#467](https://github.com/thaynes43/dev-env/issues/467)) ([f908dd6](https://github.com/thaynes43/dev-env/commit/f908dd643e999ec99a6af6aa34f93cdf290ed606))

## [2.36.0](https://github.com/thaynes43/dev-env/compare/v2.35.1...v2.36.0) (2026-10-19)


### Features

* **agent-run:** auth status for the shared Claude and Codex logins ([#461](https://github.com/thaynes43/dev-env/issues/461)) ([b56f45d](https://github.com/thaynes43/dev-env/commit/b56f45d413602d4038863e6651b59ba51552eb0d))
* v2 workbench identity for coordinator sessions ([#462](https://github.com/thaynes43/dev-env/issues/462)) ([aed438d](https://github.com/thaynes43/dev-env/commit/aed438da78b0db8f4399b94fa55f6fea9034aa57))

## [2.35.1](https://github.com/thaynes43/dev-env/compare/v2.35.0...v2.35.1) (2026-10-19)


### Bug Fixes

* let idle remote sessions drain onto a new agent image ([#459](https://github.com/thaynes43/dev-env/issues/459)) ([dadd77e](https://github.com/thaynes43/dev-env/commit/dadd77ebe1b8b9c1ce0ad80411728020c035d7a9))

## [2.35.0](https://github.com/thaynes43/dev-env/compare/v2.34.0...v2.35.0) (2026-10-19)


### Features

* Codex remote sessions on v2 with phone pairing ([#457](https://github.com/thaynes43/dev-env/issues/457)) ([76ade7a](https://github.com/thaynes43/dev-env/commit/76ade7a4194f2495cbf31efd1c63c34fafb42b63))

## [2.34.0](https://github.com/thaynes43/dev-env/compare/v2.33.0...v2.34.0) (2026-10-19)


### Features

* **keeper:** adopt a fresh Claude login into the keeper journal ([#455](https://github.com/thaynes43/dev-env/issues/455)) ([3aa45d1](https://github.com/thaynes43/dev-env/commit/3aa45d104bf65ab5c79dab7fb36afb5f0994e3ee))

## [2.33.0](https://github.com/thaynes43/dev-env/compare/v2.32.0...v2.33.0) (2026-10-19)


### Features

* **agent:** bake the opwork OpenProject backlog CLI into the agent image ([#454](https://github.com/thaynes43/dev-env/issues/454)) ([a72ec00](https://github.com/thaynes43/dev-env/commit/a72ec00b1a9058123a70d03e91248d15d6bf9ea3))
* **agentd:** send one continue after a revoked-token turn once a new Claude generation lands ([#449](https://github.com/thaynes43/dev-env/issues/449)) ([5dfbbb9](https://github.com/thaynes43/dev-env/commit/5dfbbb9dab35e98f3330371644e517254b408395))
* **keeper:** Claude login transport, journal and access-only publisher ([#450](https://github.com/thaynes43/dev-env/issues/450)) ([0be3eea](https://github.com/thaynes43/dev-env/commit/0be3eea5315523b3a43c3f78ebaad4359e47c21a))
* **keeper:** keep the shared Claude login fresh behind --enable-claude-auth ([#452](https://github.com/thaynes43/dev-env/issues/452)) ([7936a4d](https://github.com/thaynes43/dev-env/commit/7936a4dff8b65a25e551132fcb37ea95031380a1))


### Bug Fixes

* **controller:** only task sessions get private-history retention so interactive sessions can suspend ([#453](https://github.com/thaynes43/dev-env/issues/453)) ([467f6bb](https://github.com/thaynes43/dev-env/commit/467f6bbabf9c4366f9afffb0fc4fb5dfca0b46ed))

## [2.32.0](https://github.com/thaynes43/dev-env/compare/v2.31.0...v2.32.0) (2026-10-19)


### Features

* **agentd:** consume the keeper's Claude live-credential projection ([#446](https://github.com/thaynes43/dev-env/issues/446)) ([1d8691f](https://github.com/thaynes43/dev-env/commit/1d8691f7bc9b5499df30145034861f004547fc54))


### Bug Fixes

* treat claude-haiku-5-5 as the latest Haiku with effort support ([#447](https://github.com/thaynes43/dev-env/issues/447)) ([957c400](https://github.com/thaynes43/dev-env/commit/957c400dd37bfde2d710bdb425eff199b18be78a))

## [2.31.0](https://github.com/thaynes43/dev-env/compare/v2.30.7...v2.31.0) (2026-10-19)


### Features

* v2 remote mode on a pod-owned Claude login (interim) ([#443](https://github.com/thaynes43/dev-env/issues/443)) ([6adc588](https://github.com/thaynes43/dev-env/commit/6adc588d2e5ba4a11d2031aca4e8840bd064f0b5))

## [2.30.7](https://github.com/thaynes43/dev-env/compare/v2.30.6...v2.30.7) (2026-10-19)


### Bug Fixes

* preserve fresh owned authority during operator replacement ([#440](https://github.com/thaynes43/dev-env/issues/440)) ([4d81c4b](https://github.com/thaynes43/dev-env/commit/4d81c4b1d32e4930181dfa1666f4e4ca5b1cc0d6))

## [2.30.6](https://github.com/thaynes43/dev-env/compare/v2.30.5...v2.30.6) (2026-10-19)


### Bug Fixes

* **agentd:** compose owned authority and native operation deadlines ([#438](https://github.com/thaynes43/dev-env/issues/438)) ([a4e623e](https://github.com/thaynes43/dev-env/commit/a4e623ebe63e05f6b363e57a28f82d7ebc5eaa12))

## [2.30.5](https://github.com/thaynes43/dev-env/compare/v2.30.4...v2.30.5) (2026-10-19)


### Bug Fixes

* **native:** allow bounded cold-catalog owner acknowledgement ([#436](https://github.com/thaynes43/dev-env/issues/436)) ([65b2b16](https://github.com/thaynes43/dev-env/commit/65b2b1627905549fccbde9f9d1fe08d26e549297))

## [2.30.4](https://github.com/thaynes43/dev-env/compare/v2.30.3...v2.30.4) (2026-10-19)


### Bug Fixes

* **agentd:** consume complete evidence requests before reply ([#434](https://github.com/thaynes43/dev-env/issues/434)) ([239d968](https://github.com/thaynes43/dev-env/commit/239d96800f14a49ab7cd0776add07be2f257da2a))

## [2.30.3](https://github.com/thaynes43/dev-env/compare/v2.30.2...v2.30.3) (2026-10-19)


### Bug Fixes

* honor existing owned admission request budget ([#432](https://github.com/thaynes43/dev-env/issues/432)) ([84baa90](https://github.com/thaynes43/dev-env/commit/84baa90ad7f84e75afd7b56f628c5c28f0eb4860))

## [2.30.2](https://github.com/thaynes43/dev-env/compare/v2.30.1...v2.30.2) (2026-10-19)


### Bug Fixes

* **operator:** wait for initial owned Pod readiness before admission ([#428](https://github.com/thaynes43/dev-env/issues/428)) ([bfacc9b](https://github.com/thaynes43/dev-env/commit/bfacc9b34b09467696827c1786e9baffbe75f84b))
* retain owned-root refusal stages without weakening proof ([#431](https://github.com/thaynes43/dev-env/issues/431)) ([7628f42](https://github.com/thaynes43/dev-env/commit/7628f4298db3e9d1bde621da8c2e04c5f262a621))

## [2.30.1](https://github.com/thaynes43/dev-env/compare/v2.30.0...v2.30.1) (2026-10-19)


### Bug Fixes

* enable trusted owner control for guarded native sessions ([#426](https://github.com/thaynes43/dev-env/issues/426)) ([513cd3b](https://github.com/thaynes43/dev-env/commit/513cd3bc63c29e3ad5e8a233059200aeedf4d548))

## [2.30.0](https://github.com/thaynes43/dev-env/compare/v2.29.5...v2.30.0) (2026-10-18)


### Features

* **budget:** authenticate owned Codex root lifecycle ([#423](https://github.com/thaynes43/dev-env/issues/423)) ([4802b3e](https://github.com/thaynes43/dev-env/commit/4802b3ee1fd2d09d91e26563da024dd416154d3a))
* **budget:** prepare owned HOME and one owner continuation ([#425](https://github.com/thaynes43/dev-env/issues/425)) ([598f60b](https://github.com/thaynes43/dev-env/commit/598f60bece90117ae394d79a17b0a5559de35ead))
* **budget:** run finite private tasks with retained stop proof ([#422](https://github.com/thaynes43/dev-env/issues/422)) ([2b24c79](https://github.com/thaynes43/dev-env/commit/2b24c7934918b5bb61d1509c61f44ea9c2848e44))
* **cli:** create and inspect assigned task budgets ([#421](https://github.com/thaynes43/dev-env/issues/421)) ([e6035b2](https://github.com/thaynes43/dev-env/commit/e6035b2a3da58b60529bd931d73a01015e259b52))


### Bug Fixes

* **budget:** authenticate owned root through CRI exit status ([#424](https://github.com/thaynes43/dev-env/issues/424)) ([82c9416](https://github.com/thaynes43/dev-env/commit/82c94167fedbc8eb8dfc945a61a44fc9221a42c9))
* repair retention and grant integration boundaries ([#419](https://github.com/thaynes43/dev-env/issues/419)) ([a353373](https://github.com/thaynes43/dev-env/commit/a3533739bdee22870612dbd2be1bedd2a352981c))
* **trials:** retain pilot log capture after executor stop ([#370](https://github.com/thaynes43/dev-env/issues/370)) ([60c4e38](https://github.com/thaynes43/dev-env/commit/60c4e38c507c21d5fce64c51d0decc4b56362996))

## [2.29.5](https://github.com/thaynes43/dev-env/compare/v2.29.4...v2.29.5) (2026-10-17)


### Bug Fixes

* **agentd:** expose configured model and effort in ctl status ([#367](https://github.com/thaynes43/dev-env/issues/367)) ([5d44d12](https://github.com/thaynes43/dev-env/commit/5d44d12a55e37211f29d094370efc03911ba5e29))
* **pilot:** accept direct task-claim stdout in the rendezvous gate ([#366](https://github.com/thaynes43/dev-env/issues/366)) ([b8a0873](https://github.com/thaynes43/dev-env/commit/b8a0873343d3e7d88666bba6d2133ee222522f48))
* **pilot:** fix claim receipt exit 125 offline path ([#362](https://github.com/thaynes43/dev-env/issues/362)) ([e2be494](https://github.com/thaynes43/dev-env/commit/e2be49403871d742c82cf6404e2e139b9728e6d2))

## [2.29.4](https://github.com/thaynes43/dev-env/compare/v2.29.3...v2.29.4) (2026-10-17)


### Bug Fixes

* **pilot:** accept the installed heartbeat cadence in the rendezvous gate ([#358](https://github.com/thaynes43/dev-env/issues/358)) ([a7c8e60](https://github.com/thaynes43/dev-env/commit/a7c8e6096433d5464aa20ddb50b4ad45e2b091c6))
* **pilot:** run sealed coordination tests on gpt-6-luna ([#360](https://github.com/thaynes43/dev-env/issues/360)) ([e4350bc](https://github.com/thaynes43/dev-env/commit/e4350bc5eb7bf474a2e6673441fe33675532be69))

## [2.29.3](https://github.com/thaynes43/dev-env/compare/v2.29.2...v2.29.3) (2026-10-16)


### Bug Fixes

* **pilot:** bound rendezvous and dispatch peer without a model handoff ([#354](https://github.com/thaynes43/dev-env/issues/354)) ([df32cab](https://github.com/thaynes43/dev-env/commit/df32cabbad2c9afd1b95f3c8037d44140690b5d3))

## [2.29.2](https://github.com/thaynes43/dev-env/compare/v2.29.1...v2.29.2) (2026-10-16)


### Bug Fixes

* retain independent sealed pilot cohort identities ([#351](https://github.com/thaynes43/dev-env/issues/351)) ([6910dec](https://github.com/thaynes43/dev-env/commit/6910decc5279cb3d320e749418cac25fd13f85f1))

## [2.29.1](https://github.com/thaynes43/dev-env/compare/v2.29.0...v2.29.1) (2026-10-16)


### Bug Fixes

* validate sealed pilots against schema before launch ([#348](https://github.com/thaynes43/dev-env/issues/348)) ([5d17250](https://github.com/thaynes43/dev-env/commit/5d172507bceba1d1b115b1584926c16e94901fe5))

## [2.29.0](https://github.com/thaynes43/dev-env/compare/v2.28.0...v2.29.0) (2026-10-16)


### Features

* add bounded in-session task-claim client ([#340](https://github.com/thaynes43/dev-env/issues/340)) ([f7025ea](https://github.com/thaynes43/dev-env/commit/f7025eacc49465925bf78955f6455f0ab91cf459))
* add coordination archive shelf client ([#299](https://github.com/thaynes43/dev-env/issues/299)) ([b1fc8ca](https://github.com/thaynes43/dev-env/commit/b1fc8caf044778a6e71406d9a3a419635362b628))
* add coordination retention bulk lookup ([#301](https://github.com/thaynes43/dev-env/issues/301)) ([baa95b2](https://github.com/thaynes43/dev-env/commit/baa95b2025d6a93cfbf304c8e0a9801bd094422d))
* add coordination retention rule summaries ([#300](https://github.com/thaynes43/dev-env/issues/300)) ([7763470](https://github.com/thaynes43/dev-env/commit/7763470738dca53a489b24d4ec6aa63a7ca6dd4b))
* add published context archive fallback ([#302](https://github.com/thaynes43/dev-env/issues/302)) ([b8c81aa](https://github.com/thaynes43/dev-env/commit/b8c81aa9b25c19b638bd66d5bd88dbdcfab43bea))
* **agentd:** add coordination archive CLI round-trip ([#286](https://github.com/thaynes43/dev-env/issues/286)) ([ae82d95](https://github.com/thaynes43/dev-env/commit/ae82d9531463bd80ed947cea9b3577ef2d15e029))
* **agentd:** add paged coordination archive list ([#297](https://github.com/thaynes43/dev-env/issues/297)) ([f2f49f8](https://github.com/thaynes43/dev-env/commit/f2f49f85ecb15dfa4c3743d78649462f40387060))
* **agentd:** add verified UID-first archive item lookup ([#324](https://github.com/thaynes43/dev-env/issues/324)) ([1b8cc27](https://github.com/thaynes43/dev-env/commit/1b8cc27997932d4474dcca21fcb1bdd3ba076a67))
* **agentd:** clean stale coordination staging ([#296](https://github.com/thaynes43/dev-env/issues/296)) ([d6c5777](https://github.com/thaynes43/dev-env/commit/d6c5777cd65a217e936a9aab1cd6f257d7f879a9))
* **agentd:** discover unmarked coordination archive revisions ([#287](https://github.com/thaynes43/dev-env/issues/287)) ([42cd82d](https://github.com/thaynes43/dev-env/commit/42cd82da46646c26eee6aa0d57008fa71c09abd5))
* **agentd:** guard coordination archive shelf startup ([#298](https://github.com/thaynes43/dev-env/issues/298)) ([272e9be](https://github.com/thaynes43/dev-env/commit/272e9be3e6178f1eead410f537866f0ff84b166c))
* **agentd:** journal coordination archive grace transitions ([#288](https://github.com/thaynes43/dev-env/issues/288)) ([a2d2f2c](https://github.com/thaynes43/dev-env/commit/a2d2f2ca384d026a0c9cb6a3580688624b93a6cb))
* **agentd:** publish and repair immutable coordination indices ([#285](https://github.com/thaynes43/dev-env/issues/285)) ([9f5e71f](https://github.com/thaynes43/dev-env/commit/9f5e71fb17ddb2d6ee6f1d91a194b45aebc70589))
* **agentd:** select verified coordination archive revisions by recorded time ([#289](https://github.com/thaynes43/dev-env/issues/289)) ([8bba7b6](https://github.com/thaynes43/dev-env/commit/8bba7b66fe5635434e11837cbb843f1c9e025492))
* **agentd:** store verified coordination archive revisions ([#283](https://github.com/thaynes43/dev-env/issues/283)) ([cf91ef4](https://github.com/thaynes43/dev-env/commit/cf91ef4e94f8de5da7e78c42a335a228209e5b7f))
* **agentd:** verify bounded coordination archive objects ([#281](https://github.com/thaynes43/dev-env/issues/281)) ([4e67b87](https://github.com/thaynes43/dev-env/commit/4e67b87f03db48fb60948937c5273f6f03b57e92))
* **apiserver:** list archived TaskClaim metadata ([#312](https://github.com/thaynes43/dev-env/issues/312)) ([3d0a18e](https://github.com/thaynes43/dev-env/commit/3d0a18e85a39540c9c689abb253a4974cb6be39f))
* **apiserver:** list scoped archived inbox metadata ([#314](https://github.com/thaynes43/dev-env/issues/314)) ([f362fad](https://github.com/thaynes43/dev-env/commit/f362fad1af31b43e5182c1a3bf40917e83dae8f0))
* **apiserver:** read archived inbox messages after live miss ([#316](https://github.com/thaynes43/dev-env/issues/316)) ([51f8e39](https://github.com/thaynes43/dev-env/commit/51f8e3912ecef646980e429557a2eb7ce875227d))
* **apiserver:** read archived TaskClaims by optional UID ([#330](https://github.com/thaynes43/dev-env/issues/330)) ([47d7c9b](https://github.com/thaynes43/dev-env/commit/47d7c9b709fee696e11b63117d7a34cdba89680d))
* **apiserver:** read verified archived inbox acknowledgements ([#322](https://github.com/thaynes43/dev-env/issues/322)) ([862f7a4](https://github.com/thaynes43/dev-env/commit/862f7a4930f0d2d0274ffc5ac96750a8f311bac6))
* **coordination:** expose archive item reads through CLI and shelf ([#328](https://github.com/thaynes43/dev-env/issues/328)) ([504b1c5](https://github.com/thaynes43/dev-env/commit/504b1c5b12b8195347053667a61f0a38d52d597e))
* define offline Cursor control contracts ([#308](https://github.com/thaynes43/dev-env/issues/308)) ([a70c45c](https://github.com/thaynes43/dev-env/commit/a70c45c4f0d1aebeddbb6f238427e77bd3555c24))
* define offline Ollama candidate preflight ([#307](https://github.com/thaynes43/dev-env/issues/307)) ([19615e9](https://github.com/thaynes43/dev-env/commit/19615e97cd80c15be1ff94e2c88f7fa6f30264cf))
* list archived published contexts ([#306](https://github.com/thaynes43/dev-env/issues/306)) ([5957708](https://github.com/thaynes43/dev-env/commit/5957708fc0c719db09b6051fb4a3de5ce61f8910))
* **operator:** wire default-off coordination archive reads ([#334](https://github.com/thaynes43/dev-env/issues/334)) ([e25c8f3](https://github.com/thaynes43/dev-env/commit/e25c8f36e9a54f0390fe5ba0ec03c2f308cc0e46))
* retain truthful coordination eligibility observations ([#336](https://github.com/thaynes43/dev-env/issues/336)) ([a4f950e](https://github.com/thaynes43/dev-env/commit/a4f950e0b24425a8553045c67409e53f686b5e1d))
* seal and expire a two-session private pilot ([#345](https://github.com/thaynes43/dev-env/issues/345)) ([c1c84e5](https://github.com/thaynes43/dev-env/commit/c1c84e52d2da5b608ebd56086d1caf99cbbd86b3))


### Bug Fixes

* **apiserver:** confirm archived inbox acknowledgement retries ([#326](https://github.com/thaynes43/dev-env/issues/326)) ([3020772](https://github.com/thaynes43/dev-env/commit/30207728900ccf2b5cc713a6605d258b991bc729))
* **coordarchive:** use first time for default latest get ([#310](https://github.com/thaynes43/dev-env/issues/310)) ([900fc02](https://github.com/thaynes43/dev-env/commit/900fc02236d71b8f140c1bb9089bc63d6561f783))
* **coordination:** clarify lifetime and live project record bounds ([#318](https://github.com/thaynes43/dev-env/issues/318)) ([51c0256](https://github.com/thaynes43/dev-env/commit/51c0256fa4de9e9f02358374c905800196260a0f))

## [2.28.0](https://github.com/thaynes43/dev-env/compare/v2.27.0...v2.28.0) (2026-10-14)


### Features

* **h7:** isolate corrected Mac metadata checker ([#267](https://github.com/thaynes43/dev-env/issues/267)) ([dad78a3](https://github.com/thaynes43/dev-env/commit/dad78a3958867a4f7260624dc7932d402927a67a))
* **h7:** prepare fixed owner-gated history-PV Retain client ([#269](https://github.com/thaynes43/dev-env/issues/269)) ([789f2b4](https://github.com/thaynes43/dev-env/commit/789f2b499930dadf2cf02a440b56a20d224ae9b8))


### Bug Fixes

* prepare H7 resource measurements and preserve v1 access ([#262](https://github.com/thaynes43/dev-env/issues/262)) ([01c6bc9](https://github.com/thaynes43/dev-env/commit/01c6bc99bc377771c4c43223c311974c0804bf0e))
* share archived history with existing trusted agents ([#273](https://github.com/thaynes43/dev-env/issues/273)) ([1e17919](https://github.com/thaynes43/dev-env/commit/1e179198114b465dee81d4ab0ce69168bf0bea62))

## [2.27.0](https://github.com/thaynes43/dev-env/compare/v2.26.0...v2.27.0) (2026-10-13)


### Features

* archived-history reads and the guarded HOME delete, off (DESIGN-025 H6) ([#254](https://github.com/thaynes43/dev-env/issues/254)) ([2221b83](https://github.com/thaynes43/dev-env/commit/2221b834dab857abe251e228d24d4d85cec4391e))
* **operator:** HOME expiry up to Archived (DESIGN-025 H5) ([#251](https://github.com/thaynes43/dev-env/issues/251)) ([a3091af](https://github.com/thaynes43/dev-env/commit/a3091af279f9e495badac2b0a72aac99cc1690e2))

## [2.26.0](https://github.com/thaynes43/dev-env/compare/v2.25.0...v2.26.0) (2026-10-13)


### Features

* **agentd:** history export and archive (DESIGN-025 H4) ([#246](https://github.com/thaynes43/dev-env/issues/246)) ([20c710f](https://github.com/thaynes43/dev-env/commit/20c710fe21350db0ce0fde64accf12e40c5413d5))
* **operator:** stop request for reaped private executors (DESIGN-025 H3) ([#244](https://github.com/thaynes43/dev-env/issues/244)) ([944c1f5](https://github.com/thaynes43/dev-env/commit/944c1f54d6215a72c2f085d6cfe4e0a9c361503e))
* **usage:** provider-reported served model, usage and cost ([#176](https://github.com/thaynes43/dev-env/issues/176)) ([#245](https://github.com/thaynes43/dev-env/issues/245)) ([02a586b](https://github.com/thaynes43/dev-env/commit/02a586bae799fa2d3a480424e101562800884ec2))

## [2.25.0](https://github.com/thaynes43/dev-env/compare/v2.24.1...v2.25.0) (2026-10-12)


### Features

* **agentd:** closed-set reason codes for private catalog bootstrap ([#239](https://github.com/thaynes43/dev-env/issues/239)) ([388444d](https://github.com/thaynes43/dev-env/commit/388444db0b00a5bed43fe513613e2ec2f5f7dfa3))
* durable private executor terminal record (DESIGN-025 H1) ([#236](https://github.com/thaynes43/dev-env/issues/236)) ([ca58acd](https://github.com/thaynes43/dev-env/commit/ca58acdeec02f0203649beae669a979fdaff2586))


### Bug Fixes

* **agentd:** accept tracked symlinks in the clean-checkout proof ([#237](https://github.com/thaynes43/dev-env/issues/237)) ([7367fa1](https://github.com/thaynes43/dev-env/commit/7367fa1fb3631c2f46cedd8234f8d5cb6e803278))

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
