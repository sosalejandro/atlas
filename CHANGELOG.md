# Changelog

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
