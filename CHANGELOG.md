# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Changed
- The MQTT bridge allows a device to publish `devices/<id>/state/reported` and subscribe to `devices/<id>/state/desired`. QoS 1 and 2 are acknowledged. Desired state is delivered at the subscribed QoS and retried until the client acknowledges it. MQTT 3.1.1 keeps that inflight state for one hour when clean session is 0. MQTT 5 uses the session expiry interval. Both live in the bridge process.
- MQTT 5 CONNECT and PUBLISH reason codes, session expiry, message expiry, topic aliases, receive maximum, maximum packet size, content type, response topic, correlation data, and user properties are read. A will whose topic is the device's reported topic is stored when the connection drops.
- Compose pins `INFINIMESH_VERSION` to `v3.2.0`, Redis `7.4.11`, ArangoDB `3.12.12`, and Watchtower `1.7.1`. Redis `6379` and AMQP `5672` are not published on the host. ArangoDB and RabbitMQ admin UIs are not routed through Traefik or Caddy. Image builds attach an SBOM and provenance.
- Console lockfile: `postcss` 8.5.29.
- Console: `vite` 6.4.4, `vitest` 4.1.11, `vue` 3.5.43, `lodash` 4.18.1. Transitive `lodash-es`, `brace-expansion`, `minimatch`, `linkify-it`, `markdown-it`, `source-map-js`, `glob`, `@babel/runtime`, `cross-spawn`, and `tinypool` are pinned to patched releases.
- Device and account tokens use `github.com/golang-jwt/jwt/v4`. The `v3` module is no longer required.
- Request logs strip CR and LF from the method, path, and OAuth `state`.
- Third-party Actions are pinned to a commit. `docker-build` sets `contents: read`.

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
