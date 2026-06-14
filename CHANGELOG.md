# Changelog

## [1.2.1](https://github.com/pacorreia/canon-proxy/compare/v1.2.0...v1.2.1) (2026-06-14)


### Bug Fixes

* Merge pull request [#33](https://github.com/pacorreia/canon-proxy/issues/33) from pacorreia/dependabot/go_modules/github.com/aws/aws-sdk-go-v2/service/s3-1.103.2 ([e1d712a](https://github.com/pacorreia/canon-proxy/commit/e1d712ac216ea1aa965fa147295d5e952280443a))

## [1.2.0](https://github.com/pacorreia/canon-proxy/compare/v1.1.0...v1.2.0) (2026-06-04)


### Features

* add camera pairing GUID to settings page ([3de4b61](https://github.com/pacorreia/canon-proxy/commit/3de4b619c465c4e5604d3df5fcff77ad0c504969))


### Bug Fixes

* allow release-please PR merges to pass commit check and build binaries ([b433d22](https://github.com/pacorreia/canon-proxy/commit/b433d22b3e1754afc9b8ea5c7e3f3b6564ca55cc))
* correct parseGUID comment and handle spaces in GUID strings ([0a71596](https://github.com/pacorreia/canon-proxy/commit/0a7159633eaa1a9d8d79be97a388a877129b249e))
* Merge pull request [#20](https://github.com/pacorreia/canon-proxy/issues/20) from pacorreia/copilot/release-workflow-fix ([e248831](https://github.com/pacorreia/canon-proxy/commit/e2488311524d442af9b2647074ad3ee07a5d0b38))
* release binaries not built when merging release-please PR ([e248831](https://github.com/pacorreia/canon-proxy/commit/e2488311524d442af9b2647074ad3ee07a5d0b38))

## [1.1.0](https://github.com/pacorreia/canon-proxy/compare/v1.0.0...v1.1.0) (2026-06-03)


### Features

* make config.yaml optional and skip camera when not configured ([7fbfd01](https://github.com/pacorreia/canon-proxy/commit/7fbfd0103a217d05087b6d67a5d05622fbd51a14))


### Bug Fixes

* Merge pull request [#17](https://github.com/pacorreia/canon-proxy/issues/17) from pacorreia/copilot/make-config-optional ([17c8ac8](https://github.com/pacorreia/canon-proxy/commit/17c8ac899b4d430984c16568a1159577335042ed))

## 1.0.0 (2026-06-03)


### Features

* add web UI for image review and selective push (manual mode) ([443a48a](https://github.com/pacorreia/canon-proxy/commit/443a48a5db19b60a9e7c2c0350b7d15eb3ed3ef6))
* web UI for image review and selective push ([cb9bb57](https://github.com/pacorreia/canon-proxy/commit/cb9bb57267ac243b758f4c2859cd43f08c69f6c3))


### Bug Fixes

* address all PR review comments ([090aa06](https://github.com/pacorreia/canon-proxy/commit/090aa066a5345f05ae953ea6a96ff534c5e88790))
* address all unresolved review comments ([b25be57](https://github.com/pacorreia/canon-proxy/commit/b25be575feb0772d1c5cd9ef8bbbe8aba11d1ba4))
* address code review feedback (custom http client, io.Copy, filename index) ([94d0621](https://github.com/pacorreia/canon-proxy/commit/94d0621c9b5b06b05049f25ae331126e88548b1f))
* check context in recvResponse/recvData and add prefix fields to Helm chart ([e97b8da](https://github.com/pacorreia/canon-proxy/commit/e97b8da47cb8c196e47877ced4b1c74770e5bff0))
* ClearQueue resets all DB-queued records and add /data volume to README Docker example ([ea361a3](https://github.com/pacorreia/canon-proxy/commit/ea361a3dd7b898cf54944c12981acf412202f674))
* prevent duplicate uploads, improve store error handling, update README ([6120a55](https://github.com/pacorreia/canon-proxy/commit/6120a551d224592eb050ebe19ab6e55fb88ca54b))
* Queue() respects pause state before marking images as uploading ([1885ef1](https://github.com/pacorreia/canon-proxy/commit/1885ef12fc5532fd718260849d0c3dcc399a7ee2))
* re-check gate after dequeueing to make Pause() race-free ([4219ea7](https://github.com/pacorreia/canon-proxy/commit/4219ea7e3a0a4e4fdb86585f87fc6deef56318d2))
* re-queue image when paused after dequeue to prevent stuck uploads ([68b9a8e](https://github.com/pacorreia/canon-proxy/commit/68b9a8eb1aa597e982575991a2fd86ac8e90578a))
* update Dockerfile Go image to 1.25 and fix release workflow permissions ([536d787](https://github.com/pacorreia/canon-proxy/commit/536d7871ee49a459ad9fca1dbdbd58f9e42884a3))
* use Go 1.25 Docker image and fix release-please permissions ([14516cc](https://github.com/pacorreia/canon-proxy/commit/14516cc09bf19a6a782e5d0e664a9c12d592f316))
