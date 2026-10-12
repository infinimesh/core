# Installation & Local Run

This guide replaces the old PDF and matches the current repo layout and defaults.

## Prerequisites

See [prerequisites.md](prerequisites.md) for OS-specific installs. You need:
- Docker + Docker Compose (desktop or engine + compose plugin)
- Git
- OpenSSL (for self-signed certs if you need TLS for MQTT)
- MQTT client for testing (e.g., `mosquitto-clients`)

The default `BASE_DOMAIN=127.0.0.1.nip.io` (see `.env.example`) resolves to `127.0.0.1`, so the stack works locally with no `/etc/hosts` edits. For real use, set `BASE_DOMAIN` to your own domain. See [public-tls.md](public-tls.md).

If your network blocks nip.io (some DNS resolvers drop answers that point to private addresses), set `BASE_DOMAIN` to a name of your own and add `api`, `console`, `traefik`, `media` and `mqtt` under it to `/etc/hosts`, pointing at `127.0.0.1`.

## Bring up the stack

```bash
git clone https://github.com/infinimesh/core.git
cd core
cp .env.example .env
docker compose up -d
```

Services and URLs:

- Console UI: https://console.${BASE_DOMAIN}
- API (gRPC/Connect/REST via Traefik): http://api.${BASE_DOMAIN}
- Traefik dashboard: http://traefik.${BASE_DOMAIN}

ArangoDB and RabbitMQ are reachable by the other containers. Their admin UIs are not published. Redis and AMQP have no host ports in the base file.

## Default credentials (for local only)

- Platform admin user: `infinimesh` / `infinimesh` (the password comes from `INF_DEFAULT_ROOT_PASS` in `.env`)
- ArangoDB, on the Compose network: `root` / `openSesame`
- RabbitMQ, on the Compose network: `infinimesh` / `infinimesh`

Change these for anything beyond local testing.

## CLI

- Install the CLI:
  ```bash
  go install github.com/infinimesh/inf-cli@main
  ```
- Point the CLI at the local API (port defaults to 8000):
  ```bash
  inf login --api http://api.${BASE_DOMAIN}:8000 --username infinimesh --password infinimesh --insecure
  inf namespaces create demo
  inf namespaces use demo
  inf devices create demo-sensor
  inf devices token demo-sensor > demo-sensor.token
  ```
- Shadow/MQTT examples are in [device-auth.md](device-auth.md).

## Public domain

`127.0.0.1.nip.io` does not get a Let's Encrypt certificate. For a public name, Traefik or Caddy, and TLS for the device registry and MQTT, see [public-tls.md](public-tls.md).
