# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Fixed
- The REST gateway serves rules, audit, and grants. `GET /v1/rules`, `GET /v1/alarms`, `GET /v1/audit`, and `GET /v1/grants` reach the API. The console deletes a rule and lists alarms with `any_state`.

### Added
- Rules and alarms run in core. CEL conditions read `reported`, `desired`, `connected`, `device`, `title`, `tags`, and `ns`. Actions are log, webhook (HMAC `X-Infinimesh-Signature`), MQTT, and desired state. Alarm state is raised, acked, or cleared, with cooldown, in ArangoDB and Redis. The console page is "Rules and Alarms".
- Namespace `ota` stores `enabled`, `backend_url`, and `backend` (`hawkBit` or `mender`). Shadow `data.ota` carries `version`, `url`, and `sha256`, and reported state adds `state`, `progress`, and `error`. Tag changes are `TAG_ADDED` and `TAG_REMOVED` on the shadow stream. `POST /v1/devices/states:patchMany` sets desired state by namespace or tag.
- `BulkCreate`, `IssueClaim`, `Claim`, `RotateCertificate`, and `RevokeCertificate`. A rotated certificate keeps the previous fingerprint valid. `GetByFingerprint` rejects a revoked certificate.
- Shadow reads with an account or token id are appended to `AuditLog`. `GET /v1/devices/{device}/export` returns the registry entry, the current shadow, and history references. Grants carry `expires_at` and a consent record.
- `/metrics` on api, shadow, gateway, pairing, console, mqtt-bridge, and rules. `OTEL_EXPORTER_OTLP_ENDPOINT` exports traces. `MQTT_SESSION_STORE=redis` keeps bridge sessions in Redis. Namespace quotas limit devices and message rates on the bridge and on ingest. Zero means no limit.
- MQTT TLS handshake time is `TLS_HANDSHAKE_TIMEOUT` (default `30s`).
- An optional ECDSA server certificate (`TLS_EC_CERT_FILE`, `TLS_EC_KEY_FILE`) is served beside the primary certificate. The listener requires TLS 1.2. `hack/server-ec.crt` is a P-256 test certificate with a subject alternative name for localhost.
- Session tickets last 7 days in the bridge process. A resumed connection is still identified by the client certificate. TLS-PSK needs a terminator such as stunnel. Maximum fragment length is not supported. See `docs/guides/small-devices-tls.md`.

## [v3.3.0] - 2026-10-12

### Added
- `GET /devices/states/stream` accepts an account token. `namespace` limits the stream to one readable namespace. Omitting it streams every device that account can read. The stream emits `DEVICE_ADDED` and `DEVICE_REMOVED`. Each message has `eventId`. State messages include title, namespace, and tags when the registry has them. A repeated `eventId` is the same event.
- `only_delta` defaults to false: a state message is the stored shadow with the published change merged in. `only_delta` true sends the published change. `include` limits a state message to `REPORTED`, `DESIRED`, or `CONNECTION`. An empty `include` sends all three. Membership events are sent whole.
- `POST /devices/token` with `stream` true and an empty device map mints a read-only token for the caller account. `namespace` limits it to one readable namespace. The token opens the shadow streams and no other API.
- `POST /v1/ingest` accepts reported JSON from a device token that names exactly one device and may post. The body is at most 16 KB. The same device is limited to 60 requests a minute. `GET /v1/ingest/desired` long-polls desired JSON and answers `304` when the `ETag` matches.
- `POST /v1/devices/token/revoke` denies a device token by its `jti` until that token would have expired.
- Create requests honour `Idempotency-Key` for 24 hours in the API process.

### Changed
- Pairing is `infinimesh.pairing.v1.PairingService`. HTTP is `POST /v1/pairing` and `GET /v1/pairing/connection`. `/v1/handsfree`, `/handsfree`, and `/handsfree/connection` remain for one minor release. Device create sends `pairing` instead of `handsfree`.
- `api.swagger.json` is the file from the pinned `github.com/infinimesh/proto` module. CI rejects a hand-edited copy and runs `buf lint` on that module.
- REST paths are also served under `/v1`. The previous paths remain. Create is `POST` on the `/v1` path.
- A device token from `Get` is read-only and expires in 24 hours. A token from `GetByFingerprint` may post reported state and expires in 24 hours. `MakeDevicesToken` uses `post` for reported writes and manage level for desired writes. A requested level of none is not copied from the caller. When `expires_at` and `exp` are both unset, a device token expires in 24 hours. A stream token still omits expiry when both are unset.
- Reported shadow writes require `post` or manage level. Desired writes require manage level.
- List methods return one page. `page_size` defaults to 100 and is at most 500. `page_token` is an offset.
- A shadow stream replays events after `resume_after`. Each event has `sequence`. Reported and desired state versions increment when that side is patched.
- API errors are Connect errors with an `ErrorInfo` reason on domain `infinimesh.org`. Shadow process errors carry the same reason, and the API keeps it.
- The gateway reads at most 1 MB from a request body and allows 120 requests a minute per client address. It proxies `/v1/ingest` to the API.
- A GitHub release attaches `api.swagger.json`.
- Plugin queues `plugin.<name>` are durable. Each message carries the device title, namespace, and tags. A queue of that name left from before this change has to be deleted before the shadow service declares it again.
- The MQTT codec lives in `pkg/mqtt/packet`. The bridge no longer imports `github.com/slntopp/mqtt-go`.
- The MQTT bridge allows a device to publish `devices/<id>/state/reported` and subscribe to `devices/<id>/state/desired`. QoS 1 and 2 are acknowledged. Desired state is delivered at the subscribed QoS and retried until the client acknowledges it. MQTT 3.1.1 keeps that inflight state for one hour when clean session is 0. MQTT 5 uses the session expiry interval. Both live in the bridge process.
- MQTT 5 `CONNACK` advertises receive maximum 32, maximum packet size 1 MiB, and topic alias maximum 16. Inbound topic aliases use that server maximum. Desired publishes send content type, response topic, correlation data, and user properties, including on retry. A message expiry is enforced while the message is queued, and an expiry of zero is not stored. The bridge holds at most 32 inbound QoS 2 packet ids for two minutes and answers a further MQTT 5 publish with `PUBREC` `0x93`. A disabled device is disconnected within 30 seconds. A will whose topic is the device's reported topic is stored when the connection drops.
- Compose pins `INFINIMESH_VERSION` to `v3.3.0`, Redis `7.4.11`, ArangoDB `3.12.12`, and Watchtower `1.7.1`. Redis `6379` and AMQP `5672` are not published on the host. ArangoDB and RabbitMQ admin UIs are not routed through Traefik or Caddy. Image builds attach an SBOM and provenance.
- Console lockfile: `postcss` 8.5.29.
- Console: `vite` 6.4.4, `vitest` 4.1.11, `vue` 3.5.43, `lodash` 4.18.1. Transitive `lodash-es`, `brace-expansion`, `minimatch`, `linkify-it`, `markdown-it`, `source-map-js`, `glob`, `@babel/runtime`, `cross-spawn`, and `tinypool` are pinned to patched releases.
- Device and account tokens use `github.com/golang-jwt/jwt/v4`. The `v3` module is no longer required.
- Request logs strip CR and LF from the method, path, and OAuth `state`.
- Third-party Actions are pinned to a commit. `docker-build` sets `contents: read`.

### Fixed
- Connection state published on `mqtt.incoming` reaches a shadow stream, including the sync snapshot.

## [v3.2.0] - 2026-10-11

### Changed
- Go 1.27.2. Service images build from `golang:1.27-alpine`.
- Go modules: `connect` 1.21.0, `grpchealth` 1.5.0, `protobuf` 1.36.12, `amqp091-go` 1.14.0, `go-ntlmssp` 0.1.1, `go-driver` 1.6.9.
- Console: `axios` 1.20.0, `highlight.js` 11.12.0, `vue-router` 4.5.1, `@vitejs/plugin-vue` 6.0.9, `happy-dom` 20.14.5, `vite` 5.4.21, `@types/markdown-it` 14.2.0, `nanoid` 3.3.20, `svgo` 3.3.5.
- Actions: `setup-go` v7, `setup-node` v7, `pnpm/action-setup` v6, `docker/setup-buildx-action` v4, `docker/login-action` v4.

### Fixed
- Arango mocks implement `EnsureCreatedAnalyzer`, `EnsureMDIIndex`, and `EnsureMDIPrefixedIndex` from `go-driver` 1.6.9.
- The Vitest config is ESM so `@vitejs/plugin-vue` 6 loads.

## [v3.1.0-r4] - 2026-10-11

### Changed
- Images are `api` (registry), `gateway` (HTTP/JSON), and `pairing` (short-code relay). The RPC prefix stays `/infinimesh.handsfree.`. Env `PAIRING` and `PAIRING_HOST` replace `HANDSFREE` and `HANDSFREE_HOST`. The registry address is `REGISTRY_HOST` (default `api:8000`).

## [v3.1.0-r3] - 2026-10-11

### Fixed
- One private package no longer cancels the other service image builds. The anonymous pull check runs after every image is pushed.

## [v3.1.0-r2] - 2026-10-11

### Fixed
- The console image installs pnpm 9 on Node 20, the same versions the test job uses. The v3.1.0-r1 build pulled current pnpm, which rejected `pnpm-lock.yaml`.

## [v3.1.0-r1] - 2026-10-11

### Fixed
- Service images build with the Go version in `go.mod`.
- A revision tag `vX.Y.Z-rN` also publishes `:vX.Y.Z` and `:latest`.
- The GitHub release body is the `## [tag]` section of this file.

## [v3.1.0] - 2026-10-11

### Changed
- Images publish to `ghcr.io/infinimesh/core/<service>` for `console`, `handsfree`, `mqtt-bridge`, `repo`, `shadow`, and `web`. The version tag is also published as `latest`.
- The cluster install is [k8s](https://github.com/infinimesh/k8s).

## [v3.0.0] - 2025-11-28

### Added
- Optional ACME/Let's Encrypt TLS termination in Traefik with HTTPS entrypoints across API, console, media, RabbitMQ, and ArangoDB.
- Production-focused compose options: Watchtower auto-updates (profile `prod`) and persistent RabbitMQ data/log volumes.
- SaaS deployment guide (`docs/saas.md`) covering DNS/ACME setup, bring-up, and persistence.
- gRPC-gateway TLS configuration helper tests for custom CA verification in `cmd/web`.

### Changed
- `docker-compose.yaml` now mounts Traefik ACME storage, exposes :443, and switches Traefik routers to HTTPS with certresolver.
- Web gateway supports `APISERVER_CA_FILE` for secure upstream verification and points HTTP_FS to HTTPS media by default.
- Traefik config now redirects HTTP→HTTPS and disables exposed-by-default docker services for safer production defaults.

### Fixed
- Internal web→repo TLS configuration now surfaces CA load errors explicitly instead of silently skipping verification.

## [v2.5.2] - 2025-11-27

### Added
- Vendored protobufs in `third_party/infinimesh-proto` with local regeneration instructions and Connect/grpc/OpenAPI outputs.
- New GitHub workflows: tag-only Docker builds, Swagger Pages publishing, and release publishing from changelog.
- Expanded test coverage for auth middleware, sessions controller, devices token issuance, MQTT bridge helpers, and shadow JSON merge.
- Fake repo helpers to unit test access-controlled controllers without a database.

### Changed
- README updated to reflect the current platform description, local proto usage, and build/test guidance.
- TLS setup in web gateway now supports custom CA and enforces verification.
- Redis session activity uses `SCAN` instead of `KEYS` to avoid blocking and handles empty results.

### Fixed
- OAuth provider handler now returns proper status on marshal errors.
- Root claim handling now uses boolean values so privileged tokens work as intended.
- Swagger Pages workflow now passes `GITHUB_TOKEN` to actions.

[v3.2.0]: https://github.com/infinimesh/core/releases/tag/v3.2.0
[v3.1.0-r4]: https://github.com/infinimesh/core/releases/tag/v3.1.0-r4
[v3.1.0-r3]: https://github.com/infinimesh/core/releases/tag/v3.1.0-r3
[v3.1.0-r2]: https://github.com/infinimesh/core/releases/tag/v3.1.0-r2
[v3.1.0-r1]: https://github.com/infinimesh/core/releases/tag/v3.1.0-r1
[v3.1.0]: https://github.com/infinimesh/core/releases/tag/v3.1.0
[v3.0.0]: https://github.com/2pk03/infinimesh-IoT/releases/tag/v3.0.0
[v2.5.2]: https://github.com/2pk03/infinimesh-IoT/releases/tag/v2.5.2
