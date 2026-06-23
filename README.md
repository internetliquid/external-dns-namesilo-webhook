# ExternalDNS Namesilo Webhook

[![CI](https://github.com/internetliquid/external-dns-namesilo-webhook/actions/workflows/ci.yml/badge.svg)](https://github.com/internetliquid/external-dns-namesilo-webhook/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/internetliquid/external-dns-namesilo-webhook)](https://goreportcard.com/report/github.com/internetliquid/external-dns-namesilo-webhook)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

An [ExternalDNS](https://github.com/kubernetes-sigs/external-dns) **webhook
provider** for [Namesilo](https://www.namesilo.com/) DNS. It lets ExternalDNS
manage records in your Namesilo-hosted zones by running as a small sidecar
alongside ExternalDNS and speaking the ExternalDNS webhook protocol over
localhost.

> **Disclaimer:** This is an independent, community project. It is **not
> affiliated with, endorsed by, or supported by Namesilo LLC.** "Namesilo" is a
> trademark of its respective owner.

## Why a webhook?

ExternalDNS no longer accepts new in-tree providers; the
[webhook provider](https://kubernetes-sigs.github.io/external-dns/latest/docs/tutorials/webhook-provider/)
pattern is the supported way to add one. ExternalDNS talks to this sidecar over
HTTP on localhost, and the sidecar translates those calls into Namesilo API
requests.

```text
┌──────────────────────── Pod ────────────────────────┐
│  external-dns  ──HTTP──▶  webhook (localhost:8888)  │
│  (controller)             │                         │
│                           └──HTTPS──▶ Namesilo API  │
│                          health/metrics :8080       │
└─────────────────────────────────────────────────────┘
```

## Features

- Talks to the Namesilo JSON API (`type=json`) end to end — no XML.
- **Internal rate limiter** (default ~1 req/s) honouring Namesilo's per-IP
  guidance; a hit surfaces to ExternalDNS as a retryable error.
- **Per-zone record cache** with a configurable TTL, invalidated on writes, so
  reconciles don't re-list unchanged zones.
- Structured logging via `slog`; the API key is **never logged** (and is
  scrubbed from transport errors).
- Distroless, non-root, multi-arch image (`linux/amd64` + `linux/arm64`).
- Health/readiness probes and Prometheus metrics on a separate port — including
  Namesilo API call counts/latency, rate-limiter wait time, and cache hit ratio.

## Quick start

1. Create the API key secret (edit it first):

   ```sh
   kubectl create namespace external-dns
   kubectl apply -f deploy/secret.yaml
   ```

2. **Helm** (recommended) — as a sidecar of the official chart:

   ```sh
   helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/
   helm upgrade --install external-dns external-dns/external-dns \
     -n external-dns -f deploy/helm-values.yaml
   ```

   Or **raw manifests**:

   ```sh
   kubectl apply -f deploy/manifests.yaml
   ```

See [`deploy/`](deploy/) for the full examples. Set `DOMAIN_FILTER` to the zones
you want managed.

## Configuration

All configuration is via environment variables.

| Variable | Default | Description |
| --- | --- | --- |
| `NAMESILO_API_KEY` | — | **Required.** Namesilo API key. Treated as a secret. |
| `DOMAIN_FILTER` | — | **Required.** Comma-separated zones this webhook manages. |
| `DRY_RUN` | `false` | Log intended changes without calling the Namesilo API. |
| `DEFAULT_TTL` | `3600` | TTL (seconds) applied to records whose TTL ExternalDNS leaves unset. |
| `NAMESILO_MIN_TTL` | `3600` | Floor applied to every record TTL before writing. Namesilo enforces a 3600s minimum; lower TTLs are clamped up so writes aren't rejected and the plan doesn't churn. Set `0` to disable. |
| `NAMESILO_RATE_LIMIT` | `1` | Maximum Namesilo requests per second. |
| `RECORD_CACHE_TTL` | `60s` | How long `dnsListRecords` results are cached per zone (`0` disables). |
| `NAMESILO_TIMEOUT` | `30s` | Per-request timeout for Namesilo API calls. |
| `WEBHOOK_HOST` | `localhost` | Bind host for the ExternalDNS provider API. |
| `WEBHOOK_PORT` | `8888` | Bind port for the ExternalDNS provider API. |
| `METRICS_HOST` | `0.0.0.0` | Bind host for the health/metrics server. |
| `METRICS_PORT` | `8080` | Bind port for the health/metrics server. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `LOG_FORMAT` | `json` | `json` or `text`. |

The health/metrics server exposes `GET /healthz` (liveness), `GET /readyz`
(readiness), and `GET /metrics` (Prometheus).

### Metrics

Alongside the standard Go runtime and process collectors, `/metrics` exposes
webhook-specific series for operating against a rate-limited registrar:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `namesilo_webhook_api_requests_total` | counter | `operation`, `result` | Namesilo API calls by operation and `success`/`error`. |
| `namesilo_webhook_api_request_duration_seconds` | histogram | `operation` | API call latency. |
| `namesilo_webhook_ratelimit_wait_seconds` | histogram | — | Time spent blocked on the rate limiter before a call. |
| `namesilo_webhook_cache_hits_total` | counter | — | `dnsListRecords` cache hits. |
| `namesilo_webhook_cache_misses_total` | counter | — | `dnsListRecords` cache misses (lists that reached the API). |

## Supported record types

`A`, `AAAA`, `CNAME`, `MX`, and `TXT`. `TXT` is required because ExternalDNS
uses TXT records for its ownership registry. MX targets use ExternalDNS's
`<preference> <exchange>` format (e.g. `10 mail.example.com`).

## Known limitations

- **No bulk apply.** Every record change is an individual Namesilo API call.
- **Minimum TTL.** Namesilo enforces a 3600-second minimum TTL. TTLs below that
  (e.g. a `300` from an ingress annotation) are clamped up to `NAMESILO_MIN_TTL`
  before writing, so a sub-minimum TTL is silently raised rather than honoured.
- **Rate limiting.** Namesilo recommends ~1 request/second per IP. Large
  changesets are therefore applied gradually; a single reconcile that exceeds
  ExternalDNS's client deadline simply completes over subsequent reconciles
  (DNS reconciliation is idempotent).
- **Propagation delay.** Namesilo may take some time to propagate changes; they
  won't be visible to `Records` until they appear in `dnsListRecords`.

## Compatibility

Built against ExternalDNS **v0.21.0** and its webhook provider API
(`application/external.dns.webhook+json;version=1`). It works with ExternalDNS
versions that support the webhook provider (v0.14+). Go 1.26+.

## Status

This is an early release. One detail of Namesilo's JSON response shapes is
implemented against a documented assumption and marked in the code with
`// TODO: verify against live API`: whether TXT values are returned/accepted with
enclosing quotes (the client strips them defensively on read). If you spot a
mismatch against a live account, please open an issue.

## Development

```sh
make build    # build ./bin/external-dns-namesilo-webhook
make test     # go test -race with coverage
make lint     # golangci-lint
make cover    # coverage summary
make docker-build
make help     # list all targets
```

## Contributing

Issues and pull requests are welcome. Please keep commits
[Conventional](https://www.conventionalcommits.org/), run `make test` and
`gofmt`, and note any new assumptions about Namesilo's API behaviour in the PR
description.

## License

[Apache 2.0](LICENSE).
