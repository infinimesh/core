# Public domain with Let's Encrypt

Single-host Docker Compose on a name you control. The default `BASE_DOMAIN=127.0.0.1.nip.io` is for local use only and stays on the self-signed pair in `hack/server.crt` and `hack/server.key`. For real use, set `BASE_DOMAIN` to your own domain as below.

Two proxies terminate HTTPS for the console, API, and the other HTTP hostnames:

- Traefik is already the `proxy` service in `docker-compose.yaml`. Use `docker-compose.public.yml` with it.
- Caddy replaces that proxy. Use `docker-compose.caddy.yml` and `Caddyfile`.

Pick one. The device registry and the MQTT listener are the same either way.

## DNS and ports

Point these names at the host. A wildcard record covers them.

| Name | What answers |
|---|---|
| `console.$BASE_DOMAIN` | Console |
| `api.$BASE_DOMAIN` | REST, Connect, and gRPC for the device registry |
| `mqtt.$BASE_DOMAIN` | Certificate name for MQTT. Clients use port 8883 |
| `media.$BASE_DOMAIN` | http-fs (`ghcr.io/infinimesh/core/http-fs`) |
| `traefik.$BASE_DOMAIN` | Traefik dashboard. Unused with Caddy |

Open TCP 80, 443, and 8883.

The base compose file publishes plaintext MQTT `1883`, gRPC `8000`, and metrics `2112` on every interface. It does not publish Redis `6379` or AMQP `5672`. `docker-compose.public.yml` and `docker-compose.caddy.yml` drop `1883`, `8000`, and `2112` from the public interface and bind AMQP to `127.0.0.1`.

ArangoDB and RabbitMQ admin UIs are not routed. Change the root, ArangoDB, and RabbitMQ passwords before the host is on the internet. The values in `.env.example` are for a local machine.

## Shared setup

```bash
git clone https://github.com/infinimesh/core.git
cd core
cp .env.example .env
```

Set `BASE_DOMAIN` to the public name. Set `ACME_EMAIL` to a mailbox you read. Let's Encrypt uses it for expiry notices. Traefik reads the address from `traefik.yml` (`certificatesResolvers.letsencrypt.acme.email`). Caddy reads `ACME_EMAIL`.

```bash
mkdir -p letsencrypt
touch letsencrypt/acme.json
chmod 600 letsencrypt/acme.json
```

`letsencrypt/acme.json` holds account keys and private keys. The directory is gitignored.

## Traefik

Traefik 2.11.58 listens on 80 and 443. It negotiates the Docker API version, which Docker 29 requires. Port 80 redirects to HTTPS. The `letsencrypt` resolver uses the TLS-ALPN challenge, so 443 must reach this host. Routers for `console`, `api`, `traefik`, and `media` request certificates.

```bash
docker compose -f docker-compose.yaml -f docker-compose.public.yml up -d
```

MQTT does not go through Traefik. The bridge terminates TLS on `8883`. The listener requires TLS 1.2. It loads a certificate in one of two ways (`cmd/mqtt-bridge`):

- `ACME` set to a path: read a Traefik v2 `acme.json` and use the certificate whose domain starts with `mqtt.`
- `ACME` empty: read `TLS_CERT_FILE` and `TLS_KEY_FILE` (defaults `/cert/tls.crt` and `/cert/tls.key`)

`TLS_EC_CERT_FILE` and `TLS_EC_KEY_FILE`, when both are set, add an ECDSA certificate beside that one. Leave them unset for a public name unless the ECDSA certificate is valid for `mqtt.$BASE_DOMAIN`. `hack/server-ec.crt` is a localhost test certificate. `TLS_HANDSHAKE_TIMEOUT` defaults to `30s` and is at most `2m`. Port 1883 listens only when `MQTT_PLAINTEXT=1`. The local Compose file sets it. This overlay and the Caddy overlay set `MQTT_PLAINTEXT=0`. Session tickets, TLS-PSK, and maximum fragment length are in [guides/small-devices-tls.md](guides/small-devices-tls.md).

`docker-compose.public.yml` adds a router for `mqtt.$BASE_DOMAIN` so Let's Encrypt writes that name into `acme.json`, then sets `ACME=/acme/acme.json` on the bridge. The HTTPS route on that name is forwarded to the bridge's metrics port inside the Docker network. Port 8883 is the MQTT listener.

The bridge process exits until the `mqtt.` certificate is in `acme.json`. The service is `restart: always`. Watch it with:

```bash
docker compose logs -f mqtt
```

A line `mqtt certificate not found` means Traefik has not finished issuing yet. Check `docker compose logs proxy` for the ACME exchange.

## Caddy

Caddy obtains certificates with the HTTP-01 challenge, so port 80 must reach this host. It stores PEMs in its data volume. The bridge does not parse that store. The Caddy overlay mounts the volume and sets `TLS_CERT_FILE` and `TLS_KEY_FILE`.

```bash
docker compose -f docker-compose.yaml -f docker-compose.caddy.yml up -d
```

The Traefik `proxy` service is left on the `traefik` profile, so this command does not start it. There is no Traefik dashboard.

After the first issue, Caddy's default storage layout is:

```text
/data/caddy/certificates/acme-v02.api.letsencrypt.org-directory/mqtt.example.com/mqtt.example.com.crt
/data/caddy/certificates/acme-v02.api.letsencrypt.org-directory/mqtt.example.com/mqtt.example.com.key
```

The compose file points the bridge at that pair for your `BASE_DOMAIN`. If the bridge cannot open the files, list what Caddy wrote and put those paths in `TLS_CERT_FILE` and `TLS_KEY_FILE`:

```bash
docker compose -f docker-compose.yaml -f docker-compose.caddy.yml exec caddy \
  find /data/caddy/certificates -type f -name '*.crt'
```

Recreate `mqtt` after changing them.

## Device registry

In this Compose stack the device registry is the `api` service. `mqtt`, `shadow`, and `gateway` dial `api:8000` with plaintext gRPC on the Docker network (`DEVICES_HOST`, `REGISTRY_HOST`). That port is not the public TLS endpoint.

Public clients use `api.$BASE_DOMAIN` on port 443. Traefik and Caddy send these path prefixes to `api`: `/infinimesh.node.`, `/infinimesh.shadow`, `/infinimesh.plugins`, `/oauth`. The console and the REST front go to `gateway`, and `gateway` dials `api` inside the network. Pairing (`/infinimesh.pairing.`) is included on HTTPS in both public overlays. In the base file it is only on the plaintext `:8000` entrypoint.

Log in with the CLI against port 443:

```bash
inf login --api api.example.com:443 --username infinimesh --password 'your-admin-password'
```

The CLI uses TLS unless you pass `--insecure`, and it skips certificate verification. A host with no port, including an `https://` URL, is dialed on port 8000. Port 8000 here is plaintext h2c, and the public overlays do not publish it.

Devices connect to `mqtt.$BASE_DOMAIN` port `8883`. The listener requires a client certificate (`tls.RequireAnyClientCert`). The server certificate is the Let's Encrypt certificate for `mqtt.$BASE_DOMAIN`, so the system CA bundle validates it. Register the device certificate as in [device-auth.md](device-auth.md):

```bash
mosquitto_pub --cafile /etc/ssl/certs/ca-certificates.crt \
  --cert device.crt --key device.key \
  -h mqtt.example.com -p 8883 --tls-version tlsv1.2 \
  -t devices/demo-sensor/state/reported \
  -m '{"temp":21.5}'
```

On macOS the CA bundle is often `/etc/ssl/cert.pem`.

### Kubernetes operator

[platform-operator](https://github.com/infinimesh/platform-operator) deploys this same stack. Images are `ghcr.io/infinimesh/core/<service>`. The static manifests are in [k8s](https://github.com/infinimesh/k8s). MQTT TLS is the secret named by `spec.mqtt.secretName`, mounted at `/cert`.

Create a TLS secret whose keys are `tls.crt` and `tls.key` (cert-manager or any other issuer). Then set the Platform resource:

```yaml
spec:
  mqtt:
    secretName: api-example-com-tls
  apiserver:
    restful:
      host: api.example.com
      tls:
        - hosts:
            - api.example.com
          secretName: api-example-com-tls
    grpc:
      host: grpc.api.example.com
      tls:
        - hosts:
            - grpc.api.example.com
          secretName: api-example-com-tls
  controller:
    device_registry: true
    mqtt_bridge: true
    apiserver_rest: true
```

The bridge mounts `spec.mqtt.secretName` at `/cert` and reads `/cert/tls.crt` and `/cert/tls.key`. The sample in the operator repo uses secret `api-infinimesh-io-tls` with hosts `api.infinimesh.io` and `grpc.api.infinimesh.io`. `controller.device_registry` starts that older Deployment. The Compose `api` service is the registry for the current single-host stack.
