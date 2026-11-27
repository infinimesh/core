# First device

Run these commands from the root of this repository. You need Docker Compose,
[the `inf` CLI](../README.md#cli-inf), and `mosquitto_pub` (the Mosquitto clients package).

## Start the local stack and log in

For a new checkout:

```bash
cp .env.example .env
docker compose up -d
```

Wait for `docker compose ps` to show the services healthy. Use the same
`BASE_DOMAIN` as in `.env`; the default is `127.0.0.1.nip.io`. If you chose
`BASE_DOMAIN=localhost`, use that value here too.

```bash
BASE_DOMAIN=127.0.0.1.nip.io
inf login --api "api.${BASE_DOMAIN}:443" \
  --username infinimesh --password infinimesh --insecure-skip-verify
```

The local API uses a self-signed certificate. If you changed the admin password,
use your new password.

## Create the device

The [sample template](../hack/sample_device.yml) enables the device and leaves
basic auth disabled. Register its [public certificate](../hack/device_certs/sample_1.crt):

```bash
inf devices create hack/sample_device.yml \
  --crt hack/device_certs/sample_1.crt --namespace infinimesh
```

`--namespace` (`-n`) is required. `infinimesh` is the root namespace on a fresh
stack; use `inf namespaces list` to find another namespace you can manage.

The command prints `Device Created, UUID: <uuid>`. Copy that UUID:

```bash
DEVICE_ID='paste-uuid-here'
```

The sample certificate and [private key](../hack/device_certs/sample_1.key) are
shared test fixtures. Use them only for this local walkthrough. For your own
device, generate a new pair as shown in [Device authentication](device-auth.md#certificate)
and register the new public certificate instead.

## Publish reported state over TLS

The default Compose bridge exposes TLS directly on port 8883 using
[`hack/server.crt`](../hack/server.crt). Trust that server certificate and send
the registered client certificate and key:

```bash
mosquitto_pub -h localhost -p 8883 \
  --cafile hack/server.crt --insecure \
  --cert hack/device_certs/sample_1.crt \
  --key hack/device_certs/sample_1.key \
  -t "devices/${DEVICE_ID}/state/reported" -q 1 \
  -m '{"online":true,"temp":21.5}'
```

`--insecure` skips hostname verification for the local server certificate,
which has no subject alternative name. The CA file still verifies the server
certificate. For a public deployment, use its MQTT hostname and trusted CA
without `--insecure`; see [public TLS](public-tls.md).

## Read the shadow

```bash
inf devices state "$DEVICE_ID"
```

The reported state should contain `online: true` and `temp: 21.5`. Persistence
is asynchronous; if the state is initially empty, run the command again.
The device can show disconnected after the one-shot publisher exits while
its reported state remains stored.

For authentication failures, topic rules, desired state, and plaintext basic
auth, see [Device authentication and MQTT](device-auth.md).
