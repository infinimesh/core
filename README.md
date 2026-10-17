# infinimesh

[![Go Report Card](https://goreportcard.com/badge/github.com/infinimesh/core/v3)](https://goreportcard.com/report/github.com/infinimesh/core/v3)
[![Go Reference](https://pkg.go.dev/badge/github.com/infinimesh/core/v3.svg)](https://pkg.go.dev/github.com/infinimesh/core/v3)
[![Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/infinimesh/core/badges/coverage.json)](https://github.com/infinimesh/core/actions/workflows/go.yml)
[![Release](https://img.shields.io/github/v/release/infinimesh/core)](https://github.com/infinimesh/core/releases)
[![License](https://img.shields.io/github/license/infinimesh/core)](LICENSE)

![The inf CLI creates a namespace, a device and a rule, reports a hot reading, lists the raised alarm and acks it](docs/media/inf-cli-demo.gif)

infinimesh is an open source IoT platform you run yourself. It gives you a device registry, device auth, a device shadow (digital twin), an MQTT bridge, and REST, gRPC and Connect APIs. It runs on Docker Compose or Kubernetes.

What you get:

- MQTT 3.1.1 and 5, with TLS 1.2 and 1.3
- Device shadow with reported and desired state
- Messages from device to cloud and from cloud to device
- Multi-tenant namespaces with graph-based access control
- Rules on device state that raise alarms you ack and clear ([docs/rules.md](docs/rules.md))
- An OTA contract: target version per device and reported status, switched on once an OTA addon registers ([docs/ota.md](docs/ota.md))
- CoAP ingest on UDP 5683 for private networks ([docs/coap.md](docs/coap.md))
- Fleet provisioning with bulk create and factory claims ([docs/fleet.md](docs/fleet.md))
- An audit log, namespace quotas, Prometheus metrics, and MQTT bridge sessions in Redis
- A web console
- Docker Compose and Kubernetes setups

## Quickstart

You need Docker with Docker Compose.

```bash
git clone https://github.com/infinimesh/core.git
cd core
cp .env.example .env
docker compose up -d
```

Open https://console.127.0.0.1.nip.io

It redirects to HTTPS with a local self-signed certificate. Accept that certificate separately for `https://console.$BASE_DOMAIN`, `https://api.$BASE_DOMAIN`, and `https://media.$BASE_DOMAIN`. The browser keeps one exception per host. With `BASE_DOMAIN=localhost` those hosts are `console.localhost`, `api.localhost`, and `media.localhost`. Until `api` is accepted, login fails with no message. Until `media` is accepted, the media page shows `Offline :(`.

Log in as `infinimesh` / `infinimesh`. Change the password right away.

Images come from GHCR. Set `INFINIMESH_VERSION` in `.env` to pin a release. `v3.4.3` pulls that exact tag. A release candidate does not move `latest`.

`BASE_DOMAIN` in `.env` is the root host for all services. The default, `127.0.0.1.nip.io`, resolves to your own machine, so it works locally with no `/etc/hosts` edits. For real use, set `BASE_DOMAIN` to your own domain (see [docs/public-tls.md](docs/public-tls.md)).

Other services:

- API (gRPC, Connect, REST): `http://api.${BASE_DOMAIN}`
- Media files: `https://media.${BASE_DOMAIN}`
- Traefik dashboard: `http://traefik.${BASE_DOMAIN}`

ArangoDB and RabbitMQ stay on the Compose network. Their admin UIs are not routed through Traefik or Caddy. Redis `6379` and AMQP `5672` are not published on the host. The public overlay can bind AMQP to `127.0.0.1`.

Default logins. Change them before you expose anything.

- Platform admin: user `infinimesh`, password from `INF_DEFAULT_ROOT_PASS` (`infinimesh` by default)
- ArangoDB, inside the Compose network: user `root`, password `openSesame`
- RabbitMQ, inside the Compose network: `infinimesh` / `infinimesh` (from `.env`)

MQTT listens on 8883 (TLS with `hack/server.crt` and `hack/server.key`). Plaintext 1883 listens only when `MQTT_PLAINTEXT=1`. The local Compose file sets that. `hack/server-ec.crt` is a P-256 certificate with a subject alternative name for localhost. Small-device TLS is in [docs/guides/small-devices-tls.md](docs/guides/small-devices-tls.md).

## When not to use it

- You want a fully managed cloud service and do not want to run servers.
- You only have a few devices and one user. A plain MQTT broker is simpler.

## CLI (inf)

The CLI lives in [infinimesh/inf-cli](https://github.com/infinimesh/inf-cli). The easiest way to get it on macOS or Linux is Homebrew:

```bash
brew tap infinimesh/inf-cli https://github.com/infinimesh/inf-cli
brew install inf
```

New Homebrew versions ask you to trust a tap before they load it. If `brew install` says the tap is untrusted, run `brew trust infinimesh/inf-cli` and try again.

Prebuilt archives for Linux, macOS and Windows are on the [releases page](https://github.com/infinimesh/inf-cli/releases).

You can also install it with Go 1.27 or newer:

```bash
go install github.com/infinimesh/inf-cli@latest
```

`go install` names the binary `inf-cli`. The docs call it `inf`, so add an alias or rename it:

```bash
mv "$(go env GOPATH)/bin/inf-cli" "$(go env GOPATH)/bin/inf"
```

Or build from source:

```bash
git clone https://github.com/infinimesh/inf-cli.git
cd inf-cli
go build -o inf .
```

A short session against the local stack:

```bash
API=api.localhost:443

# log in as the admin account. The default Traefik certificate does not cover api.localhost.
inf login --api "$API" --username infinimesh --password infinimesh --insecure-skip-verify

# list namespaces and devices
inf namespaces list
inf devices list

# issue a device token that may post state
inf devices token <device-id> --allow-post > device.token

# report state for the device
inf devices state <device-id> --token "$(cat device.token)" --report '{"online":true,"temp":21.5}'
```

Create devices in the console, or with `inf devices create <template.json> --crt <cert.pem>`. Run `inf help` and `inf <command> --help` for all flags.

The device token is for the API. MQTT needs a client certificate registered on the device. On 8883 the device sends the certificate. On 1883 the username is the device id and the password is the base64 SHA-256 fingerprint of the certificate, and basic auth must be turned on for the device. Topic rules, QoS, and session behaviour are in [docs/device-auth.md](docs/device-auth.md).

```bash
mosquitto_pub -h api.${BASE_DOMAIN} -p 1883 \
  -u <device-id> -P "$(base64 < fingerprint.bin)" \
  -t devices/<device-id>/state/reported \
  -m '{"temp":21.5}'
```

## Documentation

- [docs/installation.md](docs/installation.md): prerequisites and Docker Compose setup
- [docs/public-tls.md](docs/public-tls.md): Traefik or Caddy with Let's Encrypt on a public domain
- [docs/device-auth.md](docs/device-auth.md): device tokens, certificates and MQTT
- [docs/rules.md](docs/rules.md), [docs/ota.md](docs/ota.md), [docs/coap.md](docs/coap.md), [docs/fleet.md](docs/fleet.md): the 3.4 features
- [CLI guide](https://github.com/infinimesh/docs/blob/main/guides/cli-getting-started.md): login, quotas, rules, OTA addon registration and audit with `inf`
- [api.swagger.json](api.swagger.json): API schema, copied from the pinned [proto](https://github.com/infinimesh/proto) module. `sh scripts/swagger_check.sh` fails if the copy drifts. `sh scripts/buf_lint_pin.sh` lints that module.

## Checks

`make commit-check` runs `fmt`, `race`, `test-fuzz`, and `code-ql-gate`. `fmt` writes Go files that `gofmt` would change. `race` is `go test -race`. `test-fuzz` runs each `Fuzz` target for `FUZZ_TIME` (default `30s`). `code-ql` runs the CodeQL CLI (`brew install --cask codeql`) and writes SARIF under `.tmp/codeql/`. The gate fails when a result is level `error`, ignoring paths under `vendor`. `jq` is required for the gate. Lower the Go database limit with `CODEQL_GO_RAM_MB` if the machine is tight. GitHub Actions runs the same race and fuzz steps, and CodeQL on pull requests with `security-extended` and `security-and-quality`.

## Releases

Current version: `v3.4.3`. inf-cli, platform-operator and the Helm chart in k8s are released with the same version from the core tag. See [CHANGELOG.md](CHANGELOG.md) and [Releases](https://github.com/infinimesh/core/releases).

A tag `vX.Y.Z` or `vX.Y.Z-rN` publishes `ghcr.io/infinimesh/core/<service>` and `:latest`. A revision tag also publishes `:vX.Y.Z`. A tag `vX.Y.Z-rc.N` publishes only that tag, and the GitHub release is a pre-release whose notes start with "for testing, not for production". The release text is the matching `## [tag]` section in `CHANGELOG.md`. The same tags on [platform-operator](https://github.com/infinimesh/platform-operator) publish `ghcr.io/infinimesh/platform-operator`. The Helm chart stays on its stable `image.tag` unless you set an RC tag yourself. RC images older than 30 days are deleted.

## Repositories

- [core](https://github.com/infinimesh/core): this platform
- [proto](https://github.com/infinimesh/proto): gRPC, Connect and OpenAPI schemas. Regenerate with `buf generate` there.
- [inf-cli](https://github.com/infinimesh/inf-cli): the `inf` command
- [platform-operator](https://github.com/infinimesh/platform-operator): Kubernetes operator
- [k8s](https://github.com/infinimesh/k8s): cluster install recipes
- [addons](https://github.com/infinimesh/addons): data sinks for Redis streams, Elasticsearch, Snowflake, SAP HANA, RedisTimeSeries and cloud storage, plus OTA addons for [hawkBit](https://github.com/infinimesh/addons/tree/main/ota-hawkbit), [Mender](https://github.com/infinimesh/addons/tree/main/ota-mender) and a [signed webhook](https://github.com/infinimesh/addons/tree/main/ota-webhook). The latest addons release is [v3.4.2](https://github.com/infinimesh/addons/releases/tag/v3.4.2). It ships as source only, with no published images and no Helm entries.
- [website](https://github.com/infinimesh/website): the site at [infinimesh.org](https://infinimesh.org/)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Questions and bugs go to [GitHub issues](https://github.com/infinimesh/core/issues).

## License

Apache 2.0. See [LICENSE](LICENSE).
