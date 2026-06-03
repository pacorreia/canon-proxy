# Changelog

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
