# Infinimesh

[![Go Report Card](https://goreportcard.com/badge/github.com/infinimesh/core)](https://goreportcard.com/report/github.com/infinimesh/core)
[![Release](https://img.shields.io/github/v/release/infinimesh/core)](https://github.com/infinimesh/core/releases)
[![License](https://img.shields.io/github/license/infinimesh/core)](LICENSE)

Infinimesh is an open source IoT platform you run yourself. It gives you a device registry, device auth, a device shadow (digital twin), an MQTT bridge, and REST, gRPC and Connect APIs. It runs on Docker Compose or Kubernetes.

What you get:

- MQTT 3 and 5, with TLS 1.2 and 1.3
- Device shadow with reported and desired state
- Messages from device to cloud and from cloud to device
- Multi-tenant namespaces with graph-based access control
- A web console
- Docker Compose and Kubernetes setups

The APIs are beta. They may still change between releases.

## Quickstart

You need Docker with Docker Compose.

```bash
git clone https://github.com/infinimesh/core.git
cd core
cp .env.example .env
docker compose up -d
```

Images come from GHCR. Set `INFINIMESH_VERSION` in `.env` to pin a release.

`BASE_DOMAIN` in `.env` is the root host for all services. The default is `infinimesh.local`. Set it to `127.0.0.1.nip.io` and you do not need to touch `/etc/hosts`. If you keep the default, add these names to `/etc/hosts`: `console`, `api`, `traefik`, `db`, `rbmq` and `media`, each followed by `.infinimesh.local`.

Then open:

- Console: `http://console.${BASE_DOMAIN}`
- API (gRPC, Connect, REST): `http://api.${BASE_DOMAIN}`
- Traefik dashboard: `http://traefik.${BASE_DOMAIN}`
- ArangoDB UI: `http://db.${BASE_DOMAIN}`
- RabbitMQ management: `http://rbmq.${BASE_DOMAIN}`

Default logins. Change them before you expose anything.

- Platform admin: user `root`, password from `INF_DEFAULT_ROOT_PASS` (`infinimesh` by default)
- ArangoDB: user `root`, password `openSesame` (see `docker-compose.yaml`)
- RabbitMQ: `infinimesh` / `infinimesh` (from `.env`)

MQTT listens on 1883 (plain) and 8883 (TLS with `hack/server.crt` and `hack/server.key`).

## When not to use it

- You want a fully managed cloud service and do not want to run servers.
- You need stable, frozen APIs today. Ours are still beta.
- You only have a few devices and one user. A plain MQTT broker is simpler.

## CLI (inf)

The CLI lives in [infinimesh/inf-cli](https://github.com/infinimesh/inf-cli). There are no prebuilt packages right now (no Homebrew tap, Snap, AUR or release downloads). Install it with Go 1.27 or newer:

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
API=http://api.${BASE_DOMAIN}:8000

# log in as root
inf login --api "$API" --username root --password infinimesh --insecure

# list namespaces and devices
inf namespaces list
inf devices list

# issue a device token that may post state
inf devices token <device-id> --allow-post > device.token

# report state for the device
inf devices state <device-id> --token "$(cat device.token)" --report '{"online":true,"temp":21.5}'
```

Create devices in the console, or with `inf devices create <template.json> --crt <cert.pem>`. Run `inf help` and `inf <command> --help` for all flags.

Use the token over MQTT. The username is the device ID, the password is the token:

```bash
mosquitto_pub -h api.${BASE_DOMAIN} -p 1883 \
  -u <device-id> -P "$(cat device.token)" \
  -t shadow/devices/<device-id>/desired \
  -m '{"reboot":true}'
```

## Documentation

- [docs/installation.md](docs/installation.md): prerequisites and Docker Compose setup
- [docs/public-tls.md](docs/public-tls.md): Traefik or Caddy with Let's Encrypt on a public domain
- [docs/device-auth.md](docs/device-auth.md): device tokens, certificates and MQTT
- [api.swagger.json](api.swagger.json): API schema

## Releases

Current version: `v3.2.0`. See [CHANGELOG.md](CHANGELOG.md) and [Releases](https://github.com/infinimesh/core/releases).

A tag `vX.Y.Z` or `vX.Y.Z-rN` on `main` publishes `ghcr.io/infinimesh/core/<service>` for `api`, `console`, `gateway`, `mqtt-bridge`, `pairing` and `shadow`. Each tag is also published as `:latest`. A revision tag also publishes the base version `:vX.Y.Z`. The GitHub release text is the matching `## [tag]` section in `CHANGELOG.md`. The same tag on [platform-operator](https://github.com/infinimesh/platform-operator) publishes `ghcr.io/infinimesh/platform-operator`.

## Repositories

- [core](https://github.com/infinimesh/core): this platform
- [proto](https://github.com/infinimesh/proto): gRPC, Connect and OpenAPI schemas. Regenerate with `buf generate` there.
- [inf-cli](https://github.com/infinimesh/inf-cli): the `inf` command
- [platform-operator](https://github.com/infinimesh/platform-operator): Kubernetes operator
- [k8s](https://github.com/infinimesh/k8s): cluster install recipes
- [addons](https://github.com/infinimesh/addons): connectors for Redis streams, Elasticsearch, Snowflake, SAP HANA, RedisTimeSeries and cloud storage
- [website](https://github.com/infinimesh/website): the site at [infinimesh.org](https://infinimesh.org/)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Questions and bugs go to [GitHub issues](https://github.com/infinimesh/core/issues).

## License

Apache 2.0. See [LICENSE](LICENSE).
