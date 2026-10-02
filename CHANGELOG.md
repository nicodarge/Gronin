# [0.2.0](https://github.com/nicodarge/Gronin/compare/v0.1.0...v0.2.0) (2026-10-02)


### Bug Fixes

* a playbook is claimed across processes, not only within one ([#20](https://github.com/nicodarge/Gronin/issues/20)) ([535c6f1](https://github.com/nicodarge/Gronin/commit/535c6f15e9762ec51d13157915618e3ea91474ca))
* **bintest:** a started gronin dies with the test binary that started it ([#58](https://github.com/nicodarge/Gronin/issues/58)) ([2aba506](https://github.com/nicodarge/Gronin/commit/2aba506339524a4438078476b39359acc93fa6f1))
* **deps:** bump grpc to 1.83.2 ([#57](https://github.com/nicodarge/Gronin/issues/57)) ([9af5a31](https://github.com/nicodarge/Gronin/commit/9af5a31360d067b45b4d0a80d8cabb6773f0511a))
* **deps:** update OpenTelemetry-Go past the exporter endpoint logging advisory ([#68](https://github.com/nicodarge/Gronin/issues/68)) ([60914ae](https://github.com/nicodarge/Gronin/commit/60914aee147ebd47eff73b55f8f5fd7ee547cd6e))
* **fakeagent:** remove the built stub when cmd/gronin's tests exit, and guard every package that builds one ([#45](https://github.com/nicodarge/Gronin/issues/45)) ([82110e9](https://github.com/nicodarge/Gronin/commit/82110e9acb8ca3fd6928b1072d0c9b3bd1412e48))
* **guard:** holder tests advance the clock only once the loop's own timer is set ([#44](https://github.com/nicodarge/Gronin/issues/44)) ([99ce66f](https://github.com/nicodarge/Gronin/commit/99ce66f7783bcce488b6118c1cded11ebbd07f1b))
* **guard:** judge a C2/C3 claim loss on the backend's clock, so a host freeze is not the backend's fault ([#53](https://github.com/nicodarge/Gronin/issues/53)) ([c801d04](https://github.com/nicodarge/Gronin/commit/c801d04e360204f915efc3e5e9b9294d34908132))
* hold TestTwoServesRunOneTickOnce's claim on the refusal, not a duration ([#64](https://github.com/nicodarge/Gronin/issues/64)) ([dcc4876](https://github.com/nicodarge/Gronin/commit/dcc4876b4b935eb6450a47b5bce63acd97146d2c)), closes [#56](https://github.com/nicodarge/Gronin/issues/56) [#56](https://github.com/nicodarge/Gronin/issues/56)
* **mutation:** copy the repository's tracked files, so no test skips under the harness ([#43](https://github.com/nicodarge/Gronin/issues/43)) ([ce95748](https://github.com/nicodarge/Gronin/commit/ce957487ff3b845c1987809913324c58667425db))
* **mutation:** keep the Go build cache off tmpfs, and clean it up ([#38](https://github.com/nicodarge/Gronin/issues/38)) ([36c637b](https://github.com/nicodarge/Gronin/commit/36c637b217583be9dfb592f6631c9c567fd8e0c2))
* **mutation:** refuse a mutant whose go test baseline ran no test ([#47](https://github.com/nicodarge/Gronin/issues/47)) ([01f00db](https://github.com/nicodarge/Gronin/commit/01f00db3554ac5f88c02c91be5be944ea945b31c))
* pin the delayed-host test to two ticks and wait on the claim itself ([#56](https://github.com/nicodarge/Gronin/issues/56)) ([157b83b](https://github.com/nicodarge/Gronin/commit/157b83b09ccf236ad9f79ceb6716e11054fd759e))
* read a config value from standard input, never from the command line ([#27](https://github.com/nicodarge/Gronin/issues/27)) ([6e82bd5](https://github.com/nicodarge/Gronin/commit/6e82bd58bb260d4725985e6eda7d2f3f09d37914))
* serve starts on the credential the runtime already runs with ([#15](https://github.com/nicodarge/Gronin/issues/15)) ([5234575](https://github.com/nicodarge/Gronin/commit/5234575ee6ba8bafb1156c6a3288cec4c6440c4a))
* the local gate costs less than the push it guards, and the harness owns its cache ([#17](https://github.com/nicodarge/Gronin/issues/17)) ([e635819](https://github.com/nicodarge/Gronin/commit/e635819570f6ea2df47705201d6da34da827dfee)), closes [#16](https://github.com/nicodarge/Gronin/issues/16)
* turn the terminal's echo off before the prompt asks for a value ([#33](https://github.com/nicodarge/Gronin/issues/33)) ([3abd837](https://github.com/nicodarge/Gronin/commit/3abd8379eb9fdab12f2276f0b2227fb9ed8bc428))
* wait for the expiry timer before moving the clock in the wait-expiry test ([#50](https://github.com/nicodarge/Gronin/issues/50)) ([ad4a510](https://github.com/nicodarge/Gronin/commit/ad4a51034e32d47c80f81e84b33805cbbb4e0c51)), closes [#44](https://github.com/nicodarge/Gronin/issues/44)


### Features

* **guard:** the coordination contract, its three implementations and the record it writes ([#34](https://github.com/nicodarge/Gronin/issues/34)) ([38207b9](https://github.com/nicodarge/Gronin/commit/38207b971b432d57a88bee295390aa10580702e0))
* **guard:** user story 1 — one playbook, two hosts, one run ([#36](https://github.com/nicodarge/Gronin/issues/36)) ([6c06f86](https://github.com/nicodarge/Gronin/commit/6c06f860711923e2153de1030059905313c9a92b))
* **guard:** user story 2 — a refused trigger is not lost ([#42](https://github.com/nicodarge/Gronin/issues/42)) ([587a221](https://github.com/nicodarge/Gronin/commit/587a22142192c8aeaef7e9c3855479750e73e6cb))
* **guard:** user story 3 — a playbook that fires too often is held back ([#49](https://github.com/nicodarge/Gronin/issues/49)) ([0abcbdc](https://github.com/nicodarge/Gronin/commit/0abcbdc199fec74fda1c6cdb9998ed32e9117f51)), closes [#48](https://github.com/nicodarge/Gronin/issues/48)
* **retrieve:** the declaration, its refusals and the record it writes ([#37](https://github.com/nicodarge/Gronin/issues/37)) ([cf728cb](https://github.com/nicodarge/Gronin/commit/cf728cb12911a8248429e33d60bee00d9ddb7821))
* **retrieve:** user story 1 — a playbook reads what the deployment's documents say ([#46](https://github.com/nicodarge/Gronin/issues/46)) ([d767731](https://github.com/nicodarge/Gronin/commit/d767731d5712b81207cdee2bfad554ed7c236e28))
* **retrieve:** user story 2 — a playbook reads what earlier runs concluded ([#62](https://github.com/nicodarge/Gronin/issues/62)) ([1bb776e](https://github.com/nicodarge/Gronin/commit/1bb776ed44b3d470f80e53a69171a75f167c4403))
* specify the guard stage ([#23](https://github.com/nicodarge/Gronin/issues/23)) ([e0c1806](https://github.com/nicodarge/Gronin/commit/e0c1806a0ab17c09d861fa73a089c9ea6e164aa4)), closes [#16](https://github.com/nicodarge/Gronin/issues/16)
* specify the retrieve stage ([#24](https://github.com/nicodarge/Gronin/issues/24)) ([0dd0238](https://github.com/nicodarge/Gronin/commit/0dd0238b582ccdc2a49b10d08fd7dc3ef2593a28)), closes [#16](https://github.com/nicodarge/Gronin/issues/16) [#16](https://github.com/nicodarge/Gronin/issues/16)
* specify the webhook trigger ([#25](https://github.com/nicodarge/Gronin/issues/25)) ([53badea](https://github.com/nicodarge/Gronin/commit/53badeaefc71f3381e0306e0b075830595aaf6f5))
* the deployment's MCP server catalogue ([#19](https://github.com/nicodarge/Gronin/issues/19)) ([8a5e70b](https://github.com/nicodarge/Gronin/commit/8a5e70b3f6c4a51849a4c56dae0d725a078c2729))
* **webhook:** phase 2 — the delivery record, the source catalogue and a held trigger ([#48](https://github.com/nicodarge/Gronin/issues/48)) ([a284c1e](https://github.com/nicodarge/Gronin/commit/a284c1e4cef4f3038f3dc94876cac2a10fe0c8c8)), closes [#42](https://github.com/nicodarge/Gronin/issues/42) [#46](https://github.com/nicodarge/Gronin/issues/46)
* **webhook:** the ingress and sources packages, kept apart from the operator API ([#40](https://github.com/nicodarge/Gronin/issues/40)) ([bf79e64](https://github.com/nicodarge/Gronin/commit/bf79e647a5e4ee3b10b7c12ae6300649d6c4d896))
* **webhook:** user story 1, slice (a) — the acceptance core ([#63](https://github.com/nicodarge/Gronin/issues/63)) ([bcd0cd0](https://github.com/nicodarge/Gronin/commit/bcd0cd075f2dee694cf80e7beddd4dd6111fca00))
* **webhook:** user story 3 — a payload cannot steer the run ([#51](https://github.com/nicodarge/Gronin/issues/51)) ([50ea36d](https://github.com/nicodarge/Gronin/commit/50ea36d37467b99d2ca10dc3ec7ce30f5a872b0f))
