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
- API (gRPC/Connect/REST via Traefik): https://api.${BASE_DOMAIN}
- Media files: https://media.${BASE_DOMAIN}
- Traefik dashboard: http://traefik.${BASE_DOMAIN}

Accept the self-signed certificate once for each of `console`, `api`, and `media`. The browser does not reuse an exception from one host on another. With `BASE_DOMAIN=localhost` those are `console.localhost`, `api.localhost`, and `media.localhost`. Until `api` is accepted, login fails with no message. Until `media` is accepted, the media page shows `Offline :(`.

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
- Point the CLI at `api.localhost:443`. The default Traefik certificate does not cover `api.localhost`, so skip verification:
  ```bash
  inf login --api api.localhost:443 --username infinimesh --password infinimesh --insecure-skip-verify
  inf login --root --api api.localhost:443 --username infinimesh --password infinimesh --insecure-skip-verify
  inf ns create --title demo --max-devices 10
  inf --token "$ADDON" ota assign --device <device> --version 1.2.3
  ```
  `--root` is the token for `inf plugins create`, `inf plugins rotate`, and `inf plugins delete`. `inf plugins create` prints the addon bearer once. Pass it with `--token`. Quotas are `--max-devices`, `--device-mps`, and `--namespace-mps` on `inf ns create` and `inf ns update`. Zero means unlimited.
- Shadow/MQTT examples are in [device-auth.md](device-auth.md).

## Public domain

`127.0.0.1.nip.io` does not get a Let's Encrypt certificate. For a public name, Traefik or Caddy, and TLS for the device registry and MQTT, see [public-tls.md](public-tls.md).
