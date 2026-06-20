# Changelog

## 0.1.0 (2026-06-20)


### Features

* add environment-based configuration loader ([f23e082](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/f23e0824b5581cc9f200951ed1ce87267abd671e))
* add ExternalDNS provider implementation ([9c868d7](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/9c868d79d2bfd86ab366e54c4afb1ed3f8c1cd8f))
* add typed Namesilo JSON API client ([4d53a12](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/4d53a123bab992bb92458709dd4096e4781dcc65))
* **metrics:** expose Namesilo API/cache telemetry ([1b376e0](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/1b376e034b73271263adfbd75b2adcf0b2becc04))
* wire the webhook binary with a health/metrics server ([9111ac2](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/9111ac28855b9fd506e3afb2a6cc8441b3f32126))


### Bug Fixes

* **provider:** clamp record TTL up to Namesilo's 3600s floor ([ecdd76e](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/ecdd76e578f60f20b87dc99abd28c4b2f24fafb1))


### Continuous Integration

* pin first release to 0.1.0 and keep feat-&gt;minor in 0.x ([fc28ec7](https://github.com/internetliquid/external-dns-namesilo-webhook/commit/fc28ec7b4e5c56a4ba17415d77cec3be9766c4bb))
