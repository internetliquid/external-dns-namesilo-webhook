# Changelog

## [0.1.1](https://github.com/internetliquid/external-dns-namesilo-webhook/compare/v0.1.0...v0.1.1) (2026-10-05)


### Bug Fixes

* **ci:** clear govulncheck (Go 1.26.8, x/text v0.42.0); the Claude review moves to v1 and reads the whole PR ([c8868e0](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/c8868e0f67686bde390c794497646ef1fa373651))
* **namesilo:** call /apibatch and accept the relative hosts the live API returns ([f31a3dc](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/f31a3dc55088683b68514f53ca1e4d99b3068c5c))
* **provider:** treat every Namesilo host as relative ([28d626c](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/28d626c99f3c33b346b24716b951074d78e71cfa))

## 0.1.0 (2026-06-23)


### Features

* ExternalDNS provider implementation ([eb50bf3](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/eb50bf31aba09f8114ed90e59e069a08c38f8f57))
* typed Namesilo JSON API client ([9ba96a8](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/9ba96a89f507e8cec6b3738d557f46a585a24915))
* webhook server with health/metrics and Prometheus telemetry ([aee769b](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/aee769bc34c761d1ca5a2b1352ea1dc2e113083a))


### Documentation

* README, deploy examples, and Apache-2.0 license ([b88cebd](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/b88cebdedfa24ec10398e4bb1e68830313f8c400))
