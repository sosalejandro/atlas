# Changelog

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
