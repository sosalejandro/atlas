# Changelog

## [0.15.0](https://github.com/sosalejandro/atlas/compare/v0.14.0...v0.15.0) (2026-09-08)


### Features

* 100% unit test coverage — all 41 features at target ([8c6eb47](https://github.com/sosalejandro/atlas/commit/8c6eb47851713bb3334bcb287bc4fe6a5d8ac76c))
* add Echo router auto-detection with group variable tracking ([7a445d2](https://github.com/sosalejandro/atlas/commit/7a445d2644ffe25b256de8675e3b28ffa10afbcf))
* add GraphQL resolver entry point tracing ([8f07be4](https://github.com/sosalejandro/atlas/commit/8f07be4656e0291007219ce1ba2d8a06a401d320))
* add layer_rules config for custom directory classification ([99aa319](https://github.com/sosalejandro/atlas/commit/99aa319ccbd3467635c7cf41d6b782a9998bba42))
* add Python/pytest support and document AI-assisted gap fixing ([2215841](https://github.com/sosalejandro/atlas/commit/2215841bc3606bced491ed227c81d70a0a0af3a6))
* add testreg serve web console (htmx + Go templates) ([51468cf](https://github.com/sosalejandro/atlas/commit/51468cfb72cb0dcd42a9492e1bd8ca1b56513a64))
* add Uber Fx/Dig DI resolver and stdlib net/http route parser ([340faec](https://github.com/sosalejandro/atlas/commit/340faec27b887815fbf0ded918a0ad9d7b0f3da7))
* **affected:** select the tests a diff can actually reach ([#90](https://github.com/sosalejandro/atlas/issues/90)) ([b2abae2](https://github.com/sosalejandro/atlas/commit/b2abae2f994dfe4d5eced105c925a6f2c618c326))
* annotation-based scanning, function extraction, and run command ([ff26d1c](https://github.com/sosalejandro/atlas/commit/ff26d1c68787330aac61cf292a095e0eb0bf11e0))
* **api:** atlas-serve — a local HTTP API, in a binary that cannot dial out ([#173](https://github.com/sosalejandro/atlas/issues/173)) ([7b8f3c1](https://github.com/sosalejandro/atlas/commit/7b8f3c186dbe0fb838b97e904bad0f8f6e9eaf1f)), closes [#101](https://github.com/sosalejandro/atlas/issues/101)
* **audit:** call-graph impl-surface coverage attribution ([#82](https://github.com/sosalejandro/atlas/issues/82)) ([6698066](https://github.com/sosalejandro/atlas/commit/669806662a1f2f9ad9bcc96e746589942c054b26))
* **audit:** line-weighted coverage scoring (Tier B) ([e312262](https://github.com/sosalejandro/atlas/commit/e312262bd8afbc5ee147f86893342c56956ba6dd))
* **audit:** package-anchor fallback for zero-call-edge test stubs ([#84](https://github.com/sosalejandro/atlas/issues/84)) ([cec617e](https://github.com/sosalejandro/atlas/commit/cec617ed88515caa151a8e928df46f98a409a66c))
* auto-map test files to features by directory proximity ([e0dd5e0](https://github.com/sosalejandro/atlas/commit/e0dd5e09aa3ef01158682b91e46fe86070251aae))
* **cli:** --stable, so two runs over the same code produce the same bytes ([#171](https://github.com/sosalejandro/atlas/issues/171)) ([e2a6bde](https://github.com/sosalejandro/atlas/commit/e2a6bdeea1fbcaf6ea6f445871e5b99a4251b104)), closes [#162](https://github.com/sosalejandro/atlas/issues/162)
* **cli/codebase:** add dead-code candidate subcommand surfacing zero-incoming-edge symbols ([#75](https://github.com/sosalejandro/atlas/issues/75)) ([ec3e585](https://github.com/sosalejandro/atlas/commit/ec3e58521eb8e387e1dc0ff153556f4982862f96))
* **cli/trace,codeindex/py:** recursive call-tree walk + cross-module python resolution ([#62](https://github.com/sosalejandro/atlas/issues/62)) ([5502ae4](https://github.com/sosalejandro/atlas/commit/5502ae424e5a1e120400a69d482ac0f45ae04689))
* **cli:** add codebase cycles subcommand for circular-import detection ([#73](https://github.com/sosalejandro/atlas/issues/73)) ([792de38](https://github.com/sosalejandro/atlas/commit/792de38193a572cfcb206e7dff991835ecfa88ba))
* **cli:** cov sync --framework go-cover (coverprofile ingest) ([24a002f](https://github.com/sosalejandro/atlas/commit/24a002f2985f0070e2e21509a688c38039f336ab))
* **cli:** wire run groups, the frontier and generated-code rules to the CLI ([e2f0514](https://github.com/sosalejandro/atlas/commit/e2f0514888a186c60a508f9301248fdcc3a90352))
* **codeindex/py:** add Python AST scanner mirroring TS scanner layout (closes [#46](https://github.com/sosalejandro/atlas/issues/46)) ([#47](https://github.com/sosalejandro/atlas/issues/47)) ([56e197b](https://github.com/sosalejandro/atlas/commit/56e197b2aed8e2833207cf85a05991ffe87cc96c))
* **codeindex/py:** parse [@atlas](https://github.com/atlas):feature comment + [@atlas](https://github.com/atlas).feature decorator annotations (closes [#53](https://github.com/sosalejandro/atlas/issues/53)) ([#55](https://github.com/sosalejandro/atlas/issues/55)) ([eff6368](https://github.com/sosalejandro/atlas/commit/eff636819bfc7f28d62fca632392adde35f3423d))
* **codeindex/py:** resolve intra-repo import edges to source files via canonical-name suffix index ([#69](https://github.com/sosalejandro/atlas/issues/69)) ([87e3e5d](https://github.com/sosalejandro/atlas/commit/87e3e5de2218640e3a90a07feac5ffb1a6ea4468))
* **codeindex:** detect generated Go code by header and glob, not by directory ([0dec0ba](https://github.com/sosalejandro/atlas/commit/0dec0ba5fcb13e565266994f6f1d97f8818ff50b)), closes [#96](https://github.com/sosalejandro/atlas/issues/96)
* contract Phase 2 — struct field extraction via go/types ([00fc8cc](https://github.com/sosalejandro/atlas/commit/00fc8cc3ecaae91791328a6f2909bba4e13b46d2))
* **contract:** add contract_exempt flag for intentionally untraceable features ([d46ecc5](https://github.com/sosalejandro/atlas/commit/d46ecc5962bf118c13d2292aa27f82ac69365283))
* **coverage:** derive a feature's impl surface from what its tests ran ([#104](https://github.com/sosalejandro/atlas/issues/104)) ([23b5300](https://github.com/sosalejandro/atlas/commit/23b53008a4e961cf2ddcf0fb0b49bbda2e1027f8))
* **coverage:** framework-parser integration tests (W2-E) ([#41](https://github.com/sosalejandro/atlas/issues/41)) ([1a849a5](https://github.com/sosalejandro/atlas/commit/1a849a5a7b54f74f9a05c448042345094457a93f))
* **coverage:** Go coverprofile parser (gocover) — real execution data ([b958188](https://github.com/sosalejandro/atlas/commit/b95818866d264b70d9c967219cb8bbd398ceaa42))
* **coverage:** istanbul FE statement-coverage ingester (Tier B) — v0.13.0 ([13c5c2b](https://github.com/sosalejandro/atlas/commit/13c5c2bec1d7f825e812e40593e4af67620c9f42))
* **coverage:** patch coverage and a CI gate that can actually fire ([#89](https://github.com/sosalejandro/atlas/issues/89)) ([8e8359d](https://github.com/sosalejandro/atlas/commit/8e8359de413a4fa757f1a66915f55a242713aed5))
* **coverage:** per-symbol statement counts in gocover ingest (Tier B) ([d1c8cf3](https://github.com/sosalejandro/atlas/commit/d1c8cf39148ee6d79893f91c722ce7f0760992bc))
* **coverage:** per-test collection shim, the producer [#104](https://github.com/sosalejandro/atlas/issues/104) was missing ([#128](https://github.com/sosalejandro/atlas/issues/128)) ([e6709dc](https://github.com/sosalejandro/atlas/commit/e6709dc90686e27f350fd24dfcf1dd1c432b316a))
* **coverage:** persist run attribution metadata and gaps ([#100](https://github.com/sosalejandro/atlas/issues/100)) ([b2bca70](https://github.com/sosalejandro/atlas/commit/b2bca70d596bb5cef8759dfb3cc5944be6a664f7))
* **coverage:** position-based coverprofile ingest (spans -&gt; impl symbols) ([7da00af](https://github.com/sosalejandro/atlas/commit/7da00afa93df9388d06ca5da628d5c071a47b9bf))
* **coverage:** report the attribution gap instead of absorbing it ([#85](https://github.com/sosalejandro/atlas/issues/85)) ([6bdee36](https://github.com/sosalejandro/atlas/commit/6bdee361f01371ea13cd99628821dbd7b5ccb6f5))
* **coverage:** run groups so a frontier can span frameworks ([#86](https://github.com/sosalejandro/atlas/issues/86)) ([2fc838e](https://github.com/sosalejandro/atlas/commit/2fc838e14399952b9663da4f698311591271c04c))
* **diagnose:** add confidence scoring, multi-match, and 17 new symptom rules ([173ae29](https://github.com/sosalejandro/atlas/commit/173ae29c660165cf8f639da0194c9f5dc2bb4001))
* **doctor:** one command that reports whether atlas's picture of the repo is still true ([#88](https://github.com/sosalejandro/atlas/issues/88)) ([ff3082b](https://github.com/sosalejandro/atlas/commit/ff3082bc93fdd77c0f8d577d7c48cb2a974d497b))
* embed ts-scanner.ts in binary — zero-install frontend scanning ([419f8fa](https://github.com/sosalejandro/atlas/commit/419f8fa75709fda1e2e9e87babbbb95a62486449))
* go/types TypedScanner + testreg contract command ([20ba20f](https://github.com/sosalejandro/atlas/commit/20ba20f9ab1b286d619bd06ca4825d5b4e5706d9))
* **indexfresh:** a shared guard for joining diff line numbers against stored spans ([58cfca6](https://github.com/sosalejandro/atlas/commit/58cfca6b55c342c904f94913dca9c3dd07448ebd))
* initial testreg CLI tool with hexagonal architecture ([d8330fb](https://github.com/sosalejandro/atlas/commit/d8330fba435d3493c8095ed561a15048ebef0647))
* **m3:** SQL intelligence, control flow, churn ranking, the exclusion ledger, and MCP ([485f606](https://github.com/sosalejandro/atlas/commit/485f6066b6515f077c76ec780d832bfd6d1750c1))
* **m4:** reproducible releases, property+acceptance tests, first run, security posture, carryforward ([43f0d56](https://github.com/sosalejandro/atlas/commit/43f0d56f0eb7e4edc3a6f4cfb2d93ad4aad6f42c))
* **m5:** edge provenance, type-checked Go indexing, decision coverage, Windows work ([616ae73](https://github.com/sosalejandro/atlas/commit/616ae73d07d255a1eced4cdaa5a4dce9a10ca677))
* **m6f:** an exit-code contract, and a release gate that runs the binary ([#165](https://github.com/sosalejandro/atlas/issues/165)) ([499a0a1](https://github.com/sosalejandro/atlas/commit/499a0a1ea7611689c10778913cef121950ed1b8f)), closes [#158](https://github.com/sosalejandro/atlas/issues/158) [#159](https://github.com/sosalejandro/atlas/issues/159)
* **m6:** the taxonomy renames, a real scan profile, and Windows path keying ([db1ef62](https://github.com/sosalejandro/atlas/commit/db1ef62fa82b33d09a727a62fd6c43cd09809dae))
* new CLI commands (sprint, gaps, diff, audit flags), self-tracking, and internals documentation ([eee3178](https://github.com/sosalejandro/atlas/commit/eee31780f85953f1851774400cc23cbcf1f94072))
* parallel scanning — Go AST and frontend run concurrently ([48f9be7](https://github.com/sosalejandro/atlas/commit/48f9be7052f46260ad0e51e4a719a474b671c617))
* **phase-1:** codeindex foundation — graph + go ast scanner + annotations parser ([84cf881](https://github.com/sosalejandro/atlas/commit/84cf8811aec3993222c0f2939878af78741eec89))
* **phase-1:** codeindex foundation — shared + graph + go ast scanner + annotations parser ([22de748](https://github.com/sosalejandro/atlas/commit/22de7481dcc53963d2f5fa39404791b26b983b09))
* **phase-2:** TS scanner — embedded scanner.ts + node orchestrator (RR/TanStack/Expo) ([b37f729](https://github.com/sosalejandro/atlas/commit/b37f729dc14cd561f34549d78f77bf23fa6b344d))
* **phase-2:** TS scanner — embedded ts-scanner.ts + node orchestrator + react-router/tanstack/expo coverage (semgrep-suppressed exec) ([a817a4f](https://github.com/sosalejandro/atlas/commit/a817a4f2c6ff55f805cdf815cac91865b6958cca))
* **phase-4:** sqlite store — 11 tables + embedded migrations + per-table adapters + Ingest ([c181527](https://github.com/sosalejandro/atlas/commit/c181527473f590d3ddb6a5bd49895d4fdad9e9e6))
* **phase-4:** sqlite store — 11 tables + embedded migrations + per-table adapters + Ingest from codeindex.Index ([361c025](https://github.com/sosalejandro/atlas/commit/361c0259161f78cbc2440aece3b660430050bab2))
* **phase-5:** coverage ingestion — 5 framework parsers + SQLite store wiring ([#8](https://github.com/sosalejandro/atlas/issues/8)) ([a4070f3](https://github.com/sosalejandro/atlas/commit/a4070f382913b9a6a5191d88166208dad9c42ad0))
* **phase-6a:** audit health-scoring + sprint planning with gap-weighted priority ([#20](https://github.com/sosalejandro/atlas/issues/20)) ([b6ca140](https://github.com/sosalejandro/atlas/commit/b6ca1409bfadbd99dc705ac02c08076ae5612cad))
* **phase-6b:** snapshot diff — structured delta across symbols/annotations/contracts/audit/coverage ([#19](https://github.com/sosalejandro/atlas/issues/19)) ([036e91c](https://github.com/sosalejandro/atlas/commit/036e91c21aa22e18707c1dafc8557e5d09d2ee53))
* **phase-6c:** contract extraction — Go funcs + TS funcs + HTTP routes (incl. Huma) + GraphQL ([#16](https://github.com/sosalejandro/atlas/issues/16)) ([1251393](https://github.com/sosalejandro/atlas/commit/1251393f211e49945d722ff25552f3d383bd12f0))
* **phase-6d:** diagnose — symptom → symbol matcher with graph-weighted scoring ([#7](https://github.com/sosalejandro/atlas/issues/7)) ([83e951e](https://github.com/sosalejandro/atlas/commit/83e951e4b155eb5c85a4fd1252f3cd562c88c2c7))
* **phase-6e:** EDA annotation kinds (bc/aggregate/saga/consumer/event-emit/outbox-publish) + store queries ([#14](https://github.com/sosalejandro/atlas/issues/14)) ([e43e8c0](https://github.com/sosalejandro/atlas/commit/e43e8c0e61733f1eded1f08c16bf3defe0ce6773))
* **phase-6f:** parser-based pattern recognizers — outbox.Append + EventRecorder embed + canonical-service shape ([#17](https://github.com/sosalejandro/atlas/issues/17)) ([8398724](https://github.com/sosalejandro/atlas/commit/83987245d72c1e6a4b5e6e9097384cfd6faa5d0e))
* **phase-7:** atlas CLI assembly — cobra dispatch for every library ([#22](https://github.com/sosalejandro/atlas/issues/22)) ([a89c168](https://github.com/sosalejandro/atlas/commit/a89c168490186e5189a420df5b8bc8e9dad32570))
* **report:** SARIF, GitHub annotations and a sticky PR comment ([#91](https://github.com/sosalejandro/atlas/issues/91)) ([cbff3d8](https://github.com/sosalejandro/atlas/commit/cbff3d8709d8270c7355914a2407df4e79d84c90))
* **scip:** read an index produced by somebody else's indexer ([#105](https://github.com/sosalejandro/atlas/issues/105) tier 3) ([#169](https://github.com/sosalejandro/atlas/issues/169)) ([fd51817](https://github.com/sosalejandro/atlas/commit/fd51817bc039dcb873c40eec4dbd73ceb7d41634))
* **store:** add coverage_results statement-fraction columns (Tier B) ([ee97139](https://github.com/sosalejandro/atlas/commit/ee97139a4370122f127015d9c83fa4da24758005))
* testreg init --discover auto-scaffolds from actual routes ([f32c77d](https://github.com/sosalejandro/atlas/commit/f32c77d3b40de39699688c1091e4a69b7d889fe9))
* **trend:** coverage and audit trends, and a gate on regression ([#92](https://github.com/sosalejandro/atlas/issues/92)) ([dc840b1](https://github.com/sosalejandro/atlas/commit/dc840b1acdb7950cc2626fc534bee24f61d2ba9c))
* **web:** align dashboard with Stitch design system + screenshots ([dec93e9](https://github.com/sosalejandro/atlas/commit/dec93e95b9696ed0a7f418d7d6eab44fe236674c))


### Bug Fixes

* **adapters:** make file locking portable, and cross-compile the whole module ([7666f93](https://github.com/sosalejandro/atlas/commit/7666f93d01acb78c52757e405301bde20a2379e4))
* **annotations:** allow dashes in strict-kind ids (closes [#15](https://github.com/sosalejandro/atlas/issues/15)) ([#18](https://github.com/sosalejandro/atlas/issues/18)) ([96dbde7](https://github.com/sosalejandro/atlas/commit/96dbde743536de40d1aa528e4910081139c3c2a9))
* audit health falls back to registry coverage when graph trace is empty ([45d5c8a](https://github.com/sosalejandro/atlas/commit/45d5c8acba33352db0837e46b8178d5ac5bd88cc))
* **audit:** add annotation_presence signal so trace-linked features score &gt; 0 ([#78](https://github.com/sosalejandro/atlas/issues/78)) ([5a34a1f](https://github.com/sosalejandro/atlas/commit/5a34a1f6dfb9927e2d74a7e413cb6c2f2f0cb9f5))
* **audit:** credit FE/mobile test-file annotations via impl-file fallback ([#79](https://github.com/sosalejandro/atlas/issues/79)) ([240cabf](https://github.com/sosalejandro/atlas/commit/240cabf8d1dd3f7a1c157703ed9ad579b28bd3cd))
* **audit:** credit features when their annotated test passes ([#82](https://github.com/sosalejandro/atlas/issues/82)) ([7f170fd](https://github.com/sosalejandro/atlas/commit/7f170fd712c4c13707ed64aea9d99830434cd372))
* **audit:** enforce dotted feature-id grammar at materialization choke point ([#77](https://github.com/sosalejandro/atlas/issues/77)) ([f2d1396](https://github.com/sosalejandro/atlas/commit/f2d1396f527ff70baf9655595e8fb3821a60d237))
* **audit:** exclude phantom/noise feature IDs from sprint backlog ([#80](https://github.com/sosalejandro/atlas/issues/80)) ([eab71c7](https://github.com/sosalejandro/atlas/commit/eab71c7257566b4576b96a69ef497622338a97e9))
* **audit:** recognize per-feature-folder api.ts convention in FE scanner ([#81](https://github.com/sosalejandro/atlas/issues/81)) ([c6660bd](https://github.com/sosalejandro/atlas/commit/c6660bd7c635efa2eaf16785bad47d41c1cba46c))
* **audit:** require dotted feature ids + presence floor (proper [#77](https://github.com/sosalejandro/atlas/issues/77)/[#78](https://github.com/sosalejandro/atlas/issues/78)) ([e402a2d](https://github.com/sosalejandro/atlas/commit/e402a2d2d1adb57ea472ec7cc1a525048ad53a16))
* **audit:** validate feature IDs in legacy annotation parser to reject stray tokens ([#77](https://github.com/sosalejandro/atlas/issues/77)) ([aa3695a](https://github.com/sosalejandro/atlas/commit/aa3695a37adb0a397d5001a59d76660c64a4db30))
* **ci:** correct the gitleaks module path, and make the planted-key test real ([91dba53](https://github.com/sosalejandro/atlas/commit/91dba5338e8882f80b0be2d52c4956542e8652c0))
* **ci:** drop the docs-claims job left behind when [#147](https://github.com/sosalejandro/atlas/issues/147) was backed out ([4252158](https://github.com/sosalejandro/atlas/commit/425215839bc639ad168a112171af94e069065705))
* **ci:** extract release version from PR title, not action output ([#43](https://github.com/sosalejandro/atlas/issues/43)) ([#45](https://github.com/sosalejandro/atlas/issues/45)) ([247c1b9](https://github.com/sosalejandro/atlas/commit/247c1b9be3950f57c08bfb81cd5ebfa90f2e1d85))
* **ci:** quote the matrix labels -- an unquoted "[#143](https://github.com/sosalejandro/atlas/issues/143)" truncated the job name ([2602b45](https://github.com/sosalejandro/atlas/commit/2602b45d512a3280fd68d9c7cf30d9652ebdb3f3))
* **ci:** re-stamp release PR even when prior stamps already in tree (closes [#49](https://github.com/sosalejandro/atlas/issues/49)) ([#54](https://github.com/sosalejandro/atlas/issues/54)) ([50ce307](https://github.com/sosalejandro/atlas/commit/50ce307292ccb48fc89d8795fb5dbea1c5439750))
* **cli/trace:** use cached store DB + accept feature-id input (closes [#28](https://github.com/sosalejandro/atlas/issues/28), [#29](https://github.com/sosalejandro/atlas/issues/29)) ([#31](https://github.com/sosalejandro/atlas/issues/31)) ([f7124f3](https://github.com/sosalejandro/atlas/commit/f7124f3126c048681fef231961ad00d2193f5554))
* **cli:** propagate --node-modules-path to init + scan + auto-detect from project root ([#24](https://github.com/sosalejandro/atlas/issues/24)) ([924d522](https://github.com/sosalejandro/atlas/commit/924d52291582e401b9a0819ac3be322044685577))
* **cli:** real version string from runtime/debug.ReadBuildInfo() ([#37](https://github.com/sosalejandro/atlas/issues/37)) ([5030af6](https://github.com/sosalejandro/atlas/commit/5030af676acd27e9b27d9bc249ec66bf591b5dbb))
* **codeindex/go:** index _test.go files by default (closes [#26](https://github.com/sosalejandro/atlas/issues/26)) ([#27](https://github.com/sosalejandro/atlas/issues/27)) ([ef802eb](https://github.com/sosalejandro/atlas/commit/ef802eb555ce23f305be9ed98a22becfbff80dee))
* **codeindex/go:** stop dropping symbols to short-name collisions ([#85](https://github.com/sosalejandro/atlas/issues/85)) ([48cccc7](https://github.com/sosalejandro/atlas/commit/48cccc736b4410d301510ede044295d36ccd70dd))
* **codeindex/py:** bound call-edge walk at nested scopes so caller identity survives ([#66](https://github.com/sosalejandro/atlas/issues/66)) ([be2efb6](https://github.com/sosalejandro/atlas/commit/be2efb6a8f4e2bd2994a28bbc15807943b248dfb))
* **codeindex/py:** emit deferred imports at any depth with scope tag ([#71](https://github.com/sosalejandro/atlas/issues/71)) ([c328de4](https://github.com/sosalejandro/atlas/commit/c328de4bc3f059c2b95d6cd3418901a31db2883a))
* **codeindex/py:** preserve edge kind + resolve in-module targets so python edges land in store ([#59](https://github.com/sosalejandro/atlas/issues/59)) ([805b2fe](https://github.com/sosalejandro/atlas/commit/805b2feb89e84e85729779fe7b8ba9494a5f1831))
* **codeindex/py:** preserve per-edge source line so python import edges report their actual lineno ([#68](https://github.com/sosalejandro/atlas/issues/68)) ([78efd17](https://github.com/sosalejandro/atlas/commit/78efd17c7b0ab922aa644beca21cc172dc2855a9))
* **codeindex/ts:** emit actionable warning when typescript module missing ([#64](https://github.com/sosalejandro/atlas/issues/64)) ([5bbc9cb](https://github.com/sosalejandro/atlas/commit/5bbc9cb933bbd4a458ae97d0ecb24d89f7fb01f0))
* **codeindex:** report the strongest generated-code signal, and stop deriving the rule twice ([f9eab04](https://github.com/sosalejandro/atlas/commit/f9eab047176174e4936727a5a4fe0225a1ad7eaf))
* contract command no longer double-builds the graph ([f285d9d](https://github.com/sosalejandro/atlas/commit/f285d9df3dcad8dce343b5252c8ef49a7ba0a3e2))
* **coverage:** commit the acceptance fixture's coverage profile ([e73653e](https://github.com/sosalejandro/atlas/commit/e73653e7b7a3f734314f46a3c56c9d888ee035e3))
* **coverage:** fold attributed statements per file, not as a scalar max ([f125245](https://github.com/sosalejandro/atlas/commit/f1252454fc99e997ab0fcd2052025d55befc5b03))
* **coverage:** merge duplicate coverprofile blocks before statement accounting ([362ef3b](https://github.com/sosalejandro/atlas/commit/362ef3b0484631ddceb69f072f9aa5c7739b1043))
* **coverage:** stop dropping production code from statement attribution ([#85](https://github.com/sosalejandro/atlas/issues/85), [#97](https://github.com/sosalejandro/atlas/issues/97), [#98](https://github.com/sosalejandro/atlas/issues/98)) ([964ede5](https://github.com/sosalejandro/atlas/commit/964ede5d87bda73a8ddf376479be746a072ca143))
* drop the sync-docs make target left behind with [#147](https://github.com/sosalejandro/atlas/issues/147) ([8983b3e](https://github.com/sosalejandro/atlas/commit/8983b3ee7638cf250d3eeccdb774660d78a4785c))
* embedded ts-scanner resolves typescript from monorepo workspaces ([61b9e95](https://github.com/sosalejandro/atlas/commit/61b9e95ba68e9303edbaf20598a66a26c3256a3f))
* **ingest:** materialize features from annotations + drop no-op --import-yaml (closes [#11](https://github.com/sosalejandro/atlas/issues/11)) ([#25](https://github.com/sosalejandro/atlas/issues/25)) ([3c199a3](https://github.com/sosalejandro/atlas/commit/3c199a3023d2cb9e44a2b8cc88227ea50c48db73))
* **m2:** the defects adversarial review found in all five CI-surface features ([3ea578b](https://github.com/sosalejandro/atlas/commit/3ea578b95c21eac1d29a1145a35ca4a68bc159dc))
* **m3:** the defects adversarial review found, and a gitignore trap that hid one ([7e9ccc2](https://github.com/sosalejandro/atlas/commit/7e9ccc29dd16c9c7627cb35660b73a19e9a76ed4))
* **m4:** the defects adversarial review found, and one DSN escape in three places ([cdfdde4](https://github.com/sosalejandro/atlas/commit/cdfdde41ae46f7e7852b9a55c3078accaf5af0df))
* **m5:** the defects adversarial review found in the typed core ([2435839](https://github.com/sosalejandro/atlas/commit/243583936acfc58ef26a6da95bb6e17978a5ba18))
* **m6d:** close the review findings, reconcile the versions, and unbreak the secret gate ([2815817](https://github.com/sosalejandro/atlas/commit/281581793447126c7033d688834a09940d398af9))
* performance analysis falls back to registry test files when graph is empty ([65fc966](https://github.com/sosalejandro/atlas/commit/65fc96682a8a43e026ec6e0cfe52b59f6db34cc9))
* **phase-2:** 3 blocking code-review findings (post-merge follow-up) ([7ecd034](https://github.com/sosalejandro/atlas/commit/7ecd034beab332a0a8e03018119e7115bd1e405e))
* probe typescript module reachability via the same candidate list ([5bbc9cb](https://github.com/sosalejandro/atlas/commit/5bbc9cb933bbd4a458ae97d0ecb24d89f7fb01f0))
* **scan:** a git repository nested in the tree is not part of it ([#167](https://github.com/sosalejandro/atlas/issues/167)) ([76b94ef](https://github.com/sosalejandro/atlas/commit/76b94ef4e19bb75dc76dedbdb118294331088391))
* **store:** correct symbol identity, spans and staleness on re-scan ([#97](https://github.com/sosalejandro/atlas/issues/97), [#98](https://github.com/sosalejandro/atlas/issues/98)) ([aaabbd8](https://github.com/sosalejandro/atlas/commit/aaabbd8f51366de74713c2c62a7e0f76cf58f232))
* TypedScanner supports Go workspace mode (go.work) ([131200a](https://github.com/sosalejandro/atlas/commit/131200a2e319c81ff31d9534f2e91eaaa38388ae))
* vitest scanner discovers client/frontend paths, fix test type heuristic ([53ff85e](https://github.com/sosalejandro/atlas/commit/53ff85e3d181706229e99935b293a66c0f7141d9))


### Performance Improvements

* build graph once in ExecuteAll — 7.9x speedup for audit/sprint ([91dd2f1](https://github.com/sosalejandro/atlas/commit/91dd2f16b9f8ad5231b7f86f0db839b80d412f42))
* fix the O(E²) adjacency rebuild and batch the ingest ([#150](https://github.com/sosalejandro/atlas/issues/150)) ([f5b5550](https://github.com/sosalejandro/atlas/commit/f5b55504af27571be3b364daeb773d4217614ad4))
* **m6e:** drop SSA for a go/types dispatch index, and read each file once ([#164](https://github.com/sosalejandro/atlas/issues/164)) ([eb61731](https://github.com/sosalejandro/atlas/commit/eb61731e6a84e04e71ede433fb5ba5853771b6e7)), closes [#155](https://github.com/sosalejandro/atlas/issues/155) [#156](https://github.com/sosalejandro/atlas/issues/156)
* **memory:** one file read per parse, a scan allocation gate, and two measured dead ends ([d1eb77e](https://github.com/sosalejandro/atlas/commit/d1eb77e5d51f68fdff8a72fe18d5f97098ca97f1))
* stop rebuilding the adjacency map per edge, and batch the ingest ([17b4867](https://github.com/sosalejandro/atlas/commit/17b4867ecfad71befbd13c2a530328eb02f545fb))


### Code Refactoring

* **audit:** decompose the two functions golangci-lint gates on ([29aeadc](https://github.com/sosalejandro/atlas/commit/29aeadc8f1c7b14fe913c9475a2fa8d9674c499f))
* **phase-2:** apply 7 advisory code-review findings (post-merge polish) ([#6](https://github.com/sosalejandro/atlas/issues/6)) ([ae2e4e1](https://github.com/sosalejandro/atlas/commit/ae2e4e1b51d231eae1346c811e101aca7b79f50e))
* **phase-4:** swap custom runner + adapters for golang-migrate + sqlc ([#5](https://github.com/sosalejandro/atlas/issues/5)) ([2bf5bd4](https://github.com/sosalejandro/atlas/commit/2bf5bd4524edab2bb1f178dd7119299df4a2c33c))


### Documentation

* add configuration field reference and layer recognition guide ([7bd446e](https://github.com/sosalejandro/atlas/commit/7bd446e3e0b2efb3fcf7f25df37611a473a20deb))
* add contract trace design — live API contracts from source code ([2940ed8](https://github.com/sosalejandro/atlas/commit/2940ed8ba41711f23b9b00f550129c7fa69fa4fd))
* add coverage enrichment design plan (--coverprofile) ([1965c22](https://github.com/sosalejandro/atlas/commit/1965c22d944319b4690edfe89fe3606db1614c87))
* add dogfooding case study — how testreg developed itself ([005af26](https://github.com/sosalejandro/atlas/commit/005af2696ae4b1c6c2718b55005bdbca452756d0))
* add go/types optional integration plan ([0d5ace4](https://github.com/sosalejandro/atlas/commit/0d5ace4e254d3b6de16d5c52bef100ba3fdb2c91))
* add performance benchmarks section to README ([59368a5](https://github.com/sosalejandro/atlas/commit/59368a5eb7904407c602ef9ea1cfcf1011ea93c3))
* add performance test plan for benchmarks and race coverage ([14173e9](https://github.com/sosalejandro/atlas/commit/14173e91eac84f01b8994d4d06e02652b1c97ca3))
* add prerequisites and assumptions section to README ([d1470c1](https://github.com/sosalejandro/atlas/commit/d1470c1f96e18e2414887645f636b39b6eaded08))
* add unified scan architecture design (parallel goroutines + per-dir TS) ([37f34d9](https://github.com/sosalejandro/atlas/commit/37f34d99f481014e82b504d3b272440dae6fefdc))
* add web dashboard GUI design (testreg serve) ([19df5db](https://github.com/sosalejandro/atlas/commit/19df5db6af2ab6af6b9b417b8e81311146ef6f7c))
* add web terminal concept draft ([be8027b](https://github.com/sosalejandro/atlas/commit/be8027b3c4373e5c8bb7cedd7b0b28657fdc5975))
* add wireframes and modular Google Stitch prompts for dashboard UI ([e91ac18](https://github.com/sosalejandro/atlas/commit/e91ac181e89554e1875aa22e20c7a67e62a72b01))
* **annotations:** grammar reference + legacy testreg compatibility + migration command (Phase 0) ([37c1401](https://github.com/sosalejandro/atlas/commit/37c14014cc9c0ffce5e6fbd4e34b8c8cd9dc47fe))
* **architecture:** authoritative package boundaries + dependency direction + CLI shape (Phase 0) ([cdc80fe](https://github.com/sosalejandro/atlas/commit/cdc80fea167b2a207fdc70bfc7ca42f68abcbb88))
* **design:** track the canvas artboard sources ([fecdc54](https://github.com/sosalejandro/atlas/commit/fecdc542ede8c5cda823f06ee2dbd1e6fc3ff597))
* expand coverage enrichment plan and framework support matrix ([91d3169](https://github.com/sosalejandro/atlas/commit/91d316978946debbc069caaf0fdf3d229aac0ebd))
* **graph:** clarify graph.Graph vs store.Edges consumer guidance (closes [#13](https://github.com/sosalejandro/atlas/issues/13)) ([#30](https://github.com/sosalejandro/atlas/issues/30)) ([196862d](https://github.com/sosalejandro/atlas/commit/196862d2dd3888cef978007cadec6531f43af747))
* mark type_checking as experimental — not production-ready ([75a020c](https://github.com/sosalejandro/atlas/commit/75a020cddae06ce5862dfe42fab82caa3d34a822))
* mark type_checking as experimental and not recommended ([99ed8a6](https://github.com/sosalejandro/atlas/commit/99ed8a65e781b15a9fb12f40f48721ac594b92fe))
* **readme:** drop stale 'Phase 7 ships' qualifier + add --version note ([#35](https://github.com/sosalejandro/atlas/issues/35)) ([05e500b](https://github.com/sosalejandro/atlas/commit/05e500b15468926ead0c5a9fa05d153566bfba1b))
* reconcile 3 post-phase-4 divergences across schema/architecture/annotations ([610976f](https://github.com/sosalejandro/atlas/commit/610976f50d881a0205d632c248628a85cb322385))
* reconcile migration-from-testreg + .atlas.yaml schema with actual Phase 7 binary ([#23](https://github.com/sosalejandro/atlas/issues/23)) ([39a3a8b](https://github.com/sosalejandro/atlas/commit/39a3a8b019e859654ba63abd3a58dbdfa30fc35b))
* redesign contract page for type_checking off (default) ([43e51a0](https://github.com/sosalejandro/atlas/commit/43e51a03a16b17cdd5db1e92c9dc794b937d47c2))
* refresh stale testreg-era command docs + add quickstart and per-language guides ([#51](https://github.com/sosalejandro/atlas/issues/51)) ([4833562](https://github.com/sosalejandro/atlas/commit/4833562e75bee40f7efdb7d316517390dd67f9a7))
* reorganize README and add per-command reference docs ([6db2c0e](https://github.com/sosalejandro/atlas/commit/6db2c0e13e1316229c8872fd8d846f4ad2ce7efc))
* replace fabricated examples with real nutrition-project-v2 output ([be53cd7](https://github.com/sosalejandro/atlas/commit/be53cd74ee0f0e20f9364f2edb4059bce60e486f))
* **schema:** sqlite v1 reference (Phase 0) ([80be588](https://github.com/sosalejandro/atlas/commit/80be588534ad5ee06e69f4d59a468881768fa195))
* sync GUI and Stitch designs with recent CLI changes ([f121ced](https://github.com/sosalejandro/atlas/commit/f121ced1e913e0f54a7c931e11ad8ddb8558e7b4))
* update GUI design and Stitch prompts with contract page + new features ([8e78917](https://github.com/sosalejandro/atlas/commit/8e7891790361569321a1cc0980c6ebcd0a277cbc))
* update README with contract, init --discover, go/types, Echo, Python ([b139cfd](https://github.com/sosalejandro/atlas/commit/b139cfd75947e9cb576ed22e2936b2a3c4ea4a06))

## [0.14.0](https://github.com/sosalejandro/atlas/compare/v0.11.0...v0.14.0) (2026-09-07)

Everything since `v0.11.0`, which is the last release that was actually
tagged. The numbering skips `0.12.0` and `0.13.0` on purpose: both were
declared in commit subjects and in `internal/cli.Version`, but neither was
ever tagged or published, so binaries built from `main` since 2026-06-02
report a version that names no release. Reusing either number for this
tree would make those reports actively wrong rather than merely
unresolvable. See [docs/releasing.md](./docs/releasing.md#why-0140).

Two top-level commands were renamed, which is breaking for anyone
scripting the CLI: `atlas audit` is now `atlas health`, and `atlas trace`
is now `atlas chain`. The store schema moves from 8 to 19; migrations run
automatically on first open.

### Features

* **store:** add coverage_results statement-fraction columns (Tier B) ([ee97139](https://github.com/sosalejandro/atlas/commit/ee97139a4370122f127015d9c83fa4da24758005))
* **coverage:** per-symbol statement counts in gocover ingest (Tier B) ([d1c8cf3](https://github.com/sosalejandro/atlas/commit/d1c8cf39148ee6d79893f91c722ce7f0760992bc))
* **audit:** line-weighted coverage scoring (Tier B) ([e312262](https://github.com/sosalejandro/atlas/commit/e312262bd8afbc5ee147f86893342c56956ba6dd))
* **coverage:** istanbul FE statement-coverage ingester (Tier B) — v0.13.0 ([13c5c2b](https://github.com/sosalejandro/atlas/commit/13c5c2bec1d7f825e812e40593e4af67620c9f42))
* **coverage:** report the attribution gap instead of absorbing it ([#85](https://github.com/sosalejandro/atlas/issues/85)) ([6bdee36](https://github.com/sosalejandro/atlas/commit/6bdee361f01371ea13cd99628821dbd7b5ccb6f5))
* **coverage:** derive a feature's impl surface from what its tests ran ([#104](https://github.com/sosalejandro/atlas/issues/104)) ([23b5300](https://github.com/sosalejandro/atlas/commit/23b53008a4e961cf2ddcf0fb0b49bbda2e1027f8))
* **coverage:** persist run attribution metadata and gaps ([#100](https://github.com/sosalejandro/atlas/issues/100)) ([b2bca70](https://github.com/sosalejandro/atlas/commit/b2bca70d596bb5cef8759dfb3cc5944be6a664f7))
* **coverage:** per-test collection shim, the producer #104 was missing ([#128](https://github.com/sosalejandro/atlas/issues/128)) ([e6709dc](https://github.com/sosalejandro/atlas/commit/e6709dc90686e27f350fd24dfcf1dd1c432b316a))
* **codeindex:** detect generated Go code by header and glob, not by directory ([0dec0ba](https://github.com/sosalejandro/atlas/commit/0dec0ba5fcb13e565266994f6f1d97f8818ff50b))
* **coverage:** run groups so a frontier can span frameworks ([#86](https://github.com/sosalejandro/atlas/issues/86)) ([2fc838e](https://github.com/sosalejandro/atlas/commit/2fc838e14399952b9663da4f698311591271c04c))
* **cli:** wire run groups, the frontier and generated-code rules to the CLI ([e2f0514](https://github.com/sosalejandro/atlas/commit/e2f0514888a186c60a508f9301248fdcc3a90352))
* **doctor:** one command that reports whether atlas's picture of the repo is still true ([#88](https://github.com/sosalejandro/atlas/issues/88)) ([ff3082b](https://github.com/sosalejandro/atlas/commit/ff3082bc93fdd77c0f8d577d7c48cb2a974d497b))
* **coverage:** patch coverage and a CI gate that can actually fire ([#89](https://github.com/sosalejandro/atlas/issues/89)) ([8e8359d](https://github.com/sosalejandro/atlas/commit/8e8359de413a4fa757f1a66915f55a242713aed5))
* **affected:** select the tests a diff can actually reach ([#90](https://github.com/sosalejandro/atlas/issues/90)) ([b2abae2](https://github.com/sosalejandro/atlas/commit/b2abae2f994dfe4d5eced105c925a6f2c618c326))
* **report:** SARIF, GitHub annotations and a sticky PR comment ([#91](https://github.com/sosalejandro/atlas/issues/91)) ([cbff3d8](https://github.com/sosalejandro/atlas/commit/cbff3d8709d8270c7355914a2407df4e79d84c90))
* **trend:** coverage and audit trends, and a gate on regression ([#92](https://github.com/sosalejandro/atlas/issues/92)) ([dc840b1](https://github.com/sosalejandro/atlas/commit/dc840b1acdb7950cc2626fc534bee24f61d2ba9c))
* **indexfresh:** a shared guard for joining diff line numbers against stored spans ([58cfca6](https://github.com/sosalejandro/atlas/commit/58cfca6b55c342c904f94913dca9c3dd07448ebd))
* **m3:** SQL intelligence, control flow, churn ranking, the exclusion ledger, and MCP ([485f606](https://github.com/sosalejandro/atlas/commit/485f6066b6515f077c76ec780d832bfd6d1750c1))
* **m4:** reproducible releases, property+acceptance tests, first run, security posture, carryforward ([43f0d56](https://github.com/sosalejandro/atlas/commit/43f0d56f0eb7e4edc3a6f4cfb2d93ad4aad6f42c))
* **m5:** edge provenance, type-checked Go indexing, decision coverage, Windows work ([616ae73](https://github.com/sosalejandro/atlas/commit/616ae73d07d255a1eced4cdaa5a4dce9a10ca677))
* **m6:** the taxonomy renames, a real scan profile, and Windows path keying ([db1ef62](https://github.com/sosalejandro/atlas/commit/db1ef62fa82b33d09a727a62fd6c43cd09809dae))

### Bug Fixes

* **coverage:** merge duplicate coverprofile blocks before statement accounting ([362ef3b](https://github.com/sosalejandro/atlas/commit/362ef3b0484631ddceb69f072f9aa5c7739b1043))
* **codeindex/go:** stop dropping symbols to short-name collisions ([#85](https://github.com/sosalejandro/atlas/issues/85)) ([48cccc7](https://github.com/sosalejandro/atlas/commit/48cccc736b4410d301510ede044295d36ccd70dd))
* **store:** correct symbol identity, spans and staleness on re-scan ([#97](https://github.com/sosalejandro/atlas/issues/97)) ([#98](https://github.com/sosalejandro/atlas/issues/98)) ([aaabbd8](https://github.com/sosalejandro/atlas/commit/aaabbd8f51366de74713c2c62a7e0f76cf58f232))
* **coverage:** commit the acceptance fixture's coverage profile ([e73653e](https://github.com/sosalejandro/atlas/commit/e73653e7b7a3f734314f46a3c56c9d888ee035e3))
* **coverage:** fold attributed statements per file, not as a scalar max ([f125245](https://github.com/sosalejandro/atlas/commit/f1252454fc99e997ab0fcd2052025d55befc5b03))
* **codeindex:** report the strongest generated-code signal, and stop deriving the rule twice ([f9eab04](https://github.com/sosalejandro/atlas/commit/f9eab047176174e4936727a5a4fe0225a1ad7eaf))
* **m2:** the defects adversarial review found in all five CI-surface features ([3ea578b](https://github.com/sosalejandro/atlas/commit/3ea578b95c21eac1d29a1145a35ca4a68bc159dc))
* **m3:** the defects adversarial review found, and a gitignore trap that hid one ([7e9ccc2](https://github.com/sosalejandro/atlas/commit/7e9ccc29dd16c9c7627cb35660b73a19e9a76ed4))
* **m4:** the defects adversarial review found, and one DSN escape in three places ([cdfdde4](https://github.com/sosalejandro/atlas/commit/cdfdde41ae46f7e7852b9a55c3078accaf5af0df))
* **adapters:** make file locking portable, and cross-compile the whole module ([7666f93](https://github.com/sosalejandro/atlas/commit/7666f93d01acb78c52757e405301bde20a2379e4))
* **ci:** quote the matrix labels -- an unquoted "#143" truncated the job name ([2602b45](https://github.com/sosalejandro/atlas/commit/2602b45d512a3280fd68d9c7cf30d9652ebdb3f3))
* **ci:** correct the gitleaks module path, and make the planted-key test real ([91dba53](https://github.com/sosalejandro/atlas/commit/91dba5338e8882f80b0be2d52c4956542e8652c0))
* **m5:** the defects adversarial review found in the typed core ([2435839](https://github.com/sosalejandro/atlas/commit/243583936acfc58ef26a6da95bb6e17978a5ba18))
* **ci:** drop the docs-claims job left behind when #147 was backed out ([4252158](https://github.com/sosalejandro/atlas/commit/425215839bc639ad168a112171af94e069065705))
* drop the sync-docs make target left behind with #147 ([8983b3e](https://github.com/sosalejandro/atlas/commit/8983b3ee7638cf250d3eeccdb774660d78a4785c))

### Performance Improvements

* stop rebuilding the adjacency map per edge, and batch the ingest ([17b4867](https://github.com/sosalejandro/atlas/commit/17b4867ecfad71befbd13c2a530328eb02f545fb))
* **memory:** one file read per parse, a scan allocation gate, and two measured dead ends ([d1eb77e](https://github.com/sosalejandro/atlas/commit/d1eb77e5d51f68fdff8a72fe18d5f97098ca97f1))

### Code Refactoring

* **audit:** decompose the two functions golangci-lint gates on ([29aeadc](https://github.com/sosalejandro/atlas/commit/29aeadc8f1c7b14fe913c9475a2fa8d9674c499f))

### Documentation

* **design:** track the canvas artboard sources ([fecdc54](https://github.com/sosalejandro/atlas/commit/fecdc542ede8c5cda823f06ee2dbd1e6fc3ff597))

## [0.11.0](https://github.com/sosalejandro/atlas/compare/v0.10.0...v0.11.0) (2026-06-01)

### Features

* **audit:** package-anchor fallback for zero-call-edge test stubs ([#84](https://github.com/sosalejandro/atlas/issues/84)) ([cec617e](https://github.com/sosalejandro/atlas/commit/cec617ed88515caa151a8e928df46f98a409a66c))

## [0.10.0](https://github.com/sosalejandro/atlas/compare/v0.9.0...v0.10.0) (2026-06-01)

### Features

* **coverage:** Go coverprofile parser (gocover) — real execution data ([b958188](https://github.com/sosalejandro/atlas/commit/b95818866d264b70d9c967219cb8bbd398ceaa42))
* **coverage:** position-based coverprofile ingest (spans -> impl symbols) ([7da00af](https://github.com/sosalejandro/atlas/commit/7da00afa93df9388d06ca5da628d5c071a47b9bf))
* **audit:** call-graph impl-surface coverage attribution ([#82](https://github.com/sosalejandro/atlas/issues/82)) ([6698066](https://github.com/sosalejandro/atlas/commit/669806662a1f2f9ad9bcc96e746589942c054b26))
* **cli:** cov sync --framework go-cover (coverprofile ingest) ([24a002f](https://github.com/sosalejandro/atlas/commit/24a002f2985f0070e2e21509a688c38039f336ab))

## [0.9.0](https://github.com/sosalejandro/atlas/compare/v0.8.0...v0.9.0) (2026-06-01)

### Bug Fixes

* **audit:** validate feature IDs in legacy annotation parser to reject stray tokens ([#77](https://github.com/sosalejandro/atlas/issues/77)) ([aa3695a](https://github.com/sosalejandro/atlas/commit/aa3695a37adb0a397d5001a59d76660c64a4db30))
* **audit:** add annotation_presence signal so trace-linked features score > 0 ([#78](https://github.com/sosalejandro/atlas/issues/78)) ([5a34a1f](https://github.com/sosalejandro/atlas/commit/5a34a1f6dfb9927e2d74a7e413cb6c2f2f0cb9f5))
* **audit:** credit FE/mobile test-file annotations via impl-file fallback ([#79](https://github.com/sosalejandro/atlas/issues/79)) ([240cabf](https://github.com/sosalejandro/atlas/commit/240cabf8d1dd3f7a1c157703ed9ad579b28bd3cd))
* **audit:** recognize per-feature-folder api.ts convention in FE scanner ([#81](https://github.com/sosalejandro/atlas/issues/81)) ([c6660bd](https://github.com/sosalejandro/atlas/commit/c6660bd7c635efa2eaf16785bad47d41c1cba46c))
* **audit:** exclude phantom/noise feature IDs from sprint backlog ([#80](https://github.com/sosalejandro/atlas/issues/80)) ([eab71c7](https://github.com/sosalejandro/atlas/commit/eab71c7257566b4576b96a69ef497622338a97e9))
* **audit:** require dotted feature ids + presence floor (proper #77/#78) ([e402a2d](https://github.com/sosalejandro/atlas/commit/e402a2d2d1adb57ea472ec7cc1a525048ad53a16))
* **audit:** enforce dotted feature-id grammar at materialization choke point ([#77](https://github.com/sosalejandro/atlas/issues/77)) ([f2d1396](https://github.com/sosalejandro/atlas/commit/f2d1396f527ff70baf9655595e8fb3821a60d237))
* **audit:** credit features when their annotated test passes ([#82](https://github.com/sosalejandro/atlas/issues/82)) ([7f170fd](https://github.com/sosalejandro/atlas/commit/7f170fd712c4c13707ed64aea9d99830434cd372))


## [0.8.0](https://github.com/sosalejandro/atlas/compare/v0.7.0...v0.8.0) (2026-05-24)


### Features

* **cli/codebase:** add dead-code candidate subcommand surfacing zero-incoming-edge symbols ([#75](https://github.com/sosalejandro/atlas/issues/75)) ([ec3e585](https://github.com/sosalejandro/atlas/commit/ec3e58521eb8e387e1dc0ff153556f4982862f96))

## [0.7.0](https://github.com/sosalejandro/atlas/compare/v0.6.1...v0.7.0) (2026-05-24)


### Features

* **cli:** add codebase cycles subcommand for circular-import detection ([#73](https://github.com/sosalejandro/atlas/issues/73)) ([792de38](https://github.com/sosalejandro/atlas/commit/792de38193a572cfcb206e7dff991835ecfa88ba))

## [0.6.1](https://github.com/sosalejandro/atlas/compare/v0.6.0...v0.6.1) (2026-05-24)


### Bug Fixes

* **codeindex/py:** emit deferred imports at any depth with scope tag ([#71](https://github.com/sosalejandro/atlas/issues/71)) ([c328de4](https://github.com/sosalejandro/atlas/commit/c328de4bc3f059c2b95d6cd3418901a31db2883a))

## [0.6.0](https://github.com/sosalejandro/atlas/compare/v0.5.2...v0.6.0) (2026-05-24)


### Features

* **codeindex/py:** resolve intra-repo import edges to source files via canonical-name suffix index ([#69](https://github.com/sosalejandro/atlas/issues/69)) ([87e3e5d](https://github.com/sosalejandro/atlas/commit/87e3e5de2218640e3a90a07feac5ffb1a6ea4468))

## [0.5.2](https://github.com/sosalejandro/atlas/compare/v0.5.1...v0.5.2) (2026-05-24)


### Bug Fixes

* **codeindex/py:** bound call-edge walk at nested scopes so caller identity survives ([#66](https://github.com/sosalejandro/atlas/issues/66)) ([be2efb6](https://github.com/sosalejandro/atlas/commit/be2efb6a8f4e2bd2994a28bbc15807943b248dfb))
* **codeindex/py:** preserve per-edge source line so python import edges report their actual lineno ([#68](https://github.com/sosalejandro/atlas/issues/68)) ([78efd17](https://github.com/sosalejandro/atlas/commit/78efd17c7b0ab922aa644beca21cc172dc2855a9))

## [0.5.1](https://github.com/sosalejandro/atlas/compare/v0.5.0...v0.5.1) (2026-05-23)


### Bug Fixes

* **codeindex/ts:** emit actionable warning when typescript module missing ([#64](https://github.com/sosalejandro/atlas/issues/64)) ([5bbc9cb](https://github.com/sosalejandro/atlas/commit/5bbc9cb933bbd4a458ae97d0ecb24d89f7fb01f0))
* probe typescript module reachability via the same candidate list ([5bbc9cb](https://github.com/sosalejandro/atlas/commit/5bbc9cb933bbd4a458ae97d0ecb24d89f7fb01f0))

## [0.5.0](https://github.com/sosalejandro/atlas/compare/v0.4.1...v0.5.0) (2026-05-23)


### Features

* **cli/trace,codeindex/py:** recursive call-tree walk + cross-module python resolution ([#62](https://github.com/sosalejandro/atlas/issues/62)) ([5502ae4](https://github.com/sosalejandro/atlas/commit/5502ae424e5a1e120400a69d482ac0f45ae04689))

## [0.4.1](https://github.com/sosalejandro/atlas/compare/v0.4.0...v0.4.1) (2026-05-23)


### Bug Fixes

* **codeindex/py:** preserve edge kind + resolve in-module targets so python edges land in store ([#59](https://github.com/sosalejandro/atlas/issues/59)) ([805b2fe](https://github.com/sosalejandro/atlas/commit/805b2feb89e84e85729779fe7b8ba9494a5f1831))

## [0.4.0](https://github.com/sosalejandro/atlas/compare/v0.3.1...v0.4.0) (2026-05-23)


### Features

* **codeindex/py:** parse [@atlas](https://github.com/atlas):feature comment + [@atlas](https://github.com/atlas).feature decorator annotations (closes [#53](https://github.com/sosalejandro/atlas/issues/53)) ([#55](https://github.com/sosalejandro/atlas/issues/55)) ([eff6368](https://github.com/sosalejandro/atlas/commit/eff636819bfc7f28d62fca632392adde35f3423d))

## [0.3.1](https://github.com/sosalejandro/atlas/compare/v0.3.0...v0.3.1) (2026-05-23)


### Bug Fixes

* **ci:** re-stamp release PR even when prior stamps already in tree (closes [#49](https://github.com/sosalejandro/atlas/issues/49)) ([#54](https://github.com/sosalejandro/atlas/issues/54)) ([50ce307](https://github.com/sosalejandro/atlas/commit/50ce307292ccb48fc89d8795fb5dbea1c5439750))


### Documentation

* refresh stale testreg-era command docs + add quickstart and per-language guides ([#51](https://github.com/sosalejandro/atlas/issues/51)) ([4833562](https://github.com/sosalejandro/atlas/commit/4833562e75bee40f7efdb7d316517390dd67f9a7))

## [0.3.0](https://github.com/sosalejandro/atlas/compare/v0.2.0...v0.3.0) (2026-05-23)


### Features

* **codeindex/py:** add Python AST scanner mirroring TS scanner layout (closes [#46](https://github.com/sosalejandro/atlas/issues/46)) ([#47](https://github.com/sosalejandro/atlas/issues/47)) ([56e197b](https://github.com/sosalejandro/atlas/commit/56e197b2aed8e2833207cf85a05991ffe87cc96c))

## [0.2.0](https://github.com/sosalejandro/atlas/compare/v0.1.4...v0.2.0) (2026-05-22)


### Features

* **coverage:** framework-parser integration tests (W2-E) ([#41](https://github.com/sosalejandro/atlas/issues/41)) ([1a849a5](https://github.com/sosalejandro/atlas/commit/1a849a5a7b54f74f9a05c448042345094457a93f))


### Bug Fixes

* **ci:** extract release version from PR title, not action output ([#43](https://github.com/sosalejandro/atlas/issues/43)) ([#45](https://github.com/sosalejandro/atlas/issues/45)) ([247c1b9](https://github.com/sosalejandro/atlas/commit/247c1b9be3950f57c08bfb81cd5ebfa90f2e1d85))

## [0.1.4](https://github.com/sosalejandro/atlas/compare/v0.1.3...v0.1.4) (2026-05-19)


### Bug Fixes

* **cli:** real version string from runtime/debug.ReadBuildInfo() ([#37](https://github.com/sosalejandro/atlas/issues/37)) ([5030af6](https://github.com/sosalejandro/atlas/commit/5030af676acd27e9b27d9bc249ec66bf591b5dbb))

## [0.1.3](https://github.com/sosalejandro/atlas/compare/v0.1.2...v0.1.3) (2026-05-19)


### Documentation

* **readme:** drop stale 'Phase 7 ships' qualifier + add --version note ([#35](https://github.com/sosalejandro/atlas/issues/35)) ([05e500b](https://github.com/sosalejandro/atlas/commit/05e500b15468926ead0c5a9fa05d153566bfba1b))
