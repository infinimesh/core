# Device Authentication & MQTT

infinimesh has no device SDK. Your device brings its own MQTT client, for example mosquitto, Paho or Node-RED.

Every device that talks MQTT needs a client certificate registered in infinimesh. The MQTT bridge finds the device by the SHA-256 fingerprint of that certificate. Device tokens (JWT) are for the HTTP, gRPC and Connect shadow API, not for MQTT.

## Register the device

Generate a key and a certificate (self-signed example):

```bash
openssl genrsa -out device.key 4096
openssl req -new -x509 -sha256 -key device.key -out device.crt -days 365 \
  -subj "/CN=demo-sensor"
```

Upload the certificate when you create the device, in the console or with the CLI:

```bash
inf devices create device.json --crt device.crt
```

The device must be enabled.

## MQTT with TLS on 8883

The device presents its certificate during the TLS handshake:

```bash
mosquitto_pub --cafile /etc/ssl/certs/ca-certificates.crt \
  --cert device.crt --key device.key \
  -h api.${BASE_DOMAIN} -p 8883 --tls-version tlsv1.2 \
  -t shadow/devices/<device-id>/reported \
  -m '{"temp":21.5}'
```

Adjust `--cafile` to your OS trust store. Locally, use `--cafile hack/server.crt`.

## MQTT without TLS on 1883

Port 1883 has no TLS, so the certificate is not sent. Instead you log in with:

- username: the device ID
- password: the base64 of the SHA-256 fingerprint of the device certificate (DER form)

Basic auth is off per device. Turn it on in the console (the lock switch on the device card) or with `POST /devices/<device-id>/toggle/basic`.

Compute the password from the certificate:

```bash
openssl x509 -in device.crt -outform DER | openssl dgst -sha256 -binary | base64
```

Then publish:

```bash
mosquitto_pub -h api.${BASE_DOMAIN} -p 1883 \
  -u <device-id> -P "<base64 fingerprint>" \
  -t shadow/devices/<device-id>/reported \
  -m '{"temp":21.5}'
```

Anyone who knows the fingerprint can log in on 1883, and the traffic is not encrypted. Use it for local tests only.

## How the bridge treats topics

The bridge does not check topic names today.

- Every message a device publishes is stored as **reported** state, whatever the topic. A device cannot set desired state over MQTT.
- When a device subscribes to any topic, the bridge sends it the current desired state and every later change, on the topic name it subscribed with.

We suggest `shadow/devices/<device-id>/reported` for publishing and `shadow/devices/<device-id>/desired` for subscribing, so your devices keep working if topic checks are added later.

Payloads must be JSON objects. Reported and desired state are merged into the stored state with JSON merge patch.

The bridge sends messages to devices with QoS 0, and it does not acknowledge QoS 1 publishes from devices. Publish with QoS 0. MQTT 3.1.1 and MQTT 5 clients can connect, but MQTT 5 support is partial: properties and most MQTT 5 features are not used.

## Shadow over the API with device tokens

A device token is a JWT that lists devices and an access level for each. Use it for the API, not for MQTT:

```bash
inf devices token <device-id> --allow-post > demo-sensor.token
inf devices state <device-id> --token "$(cat demo-sensor.token)" --report '{"online":true,"temp":21.5}'
```

Set desired state, which the bridge then pushes to subscribed devices:

```bash
inf devices state <device-id> --patch '{"threshold":50}'
inf devices state <device-id>
```

## Troubleshooting

- MQTT connection closed right after connect: the certificate is not registered, the device is disabled, or on 1883 the username is not the device ID, the password is not the fingerprint, or basic auth is off.
- 401 or permission errors on the API: check that the token includes the device. Reissue with `--allow-post` for writes.
- Connection refused: check host and port, and that `docker compose ps` shows the `mqtt` service running.
- TLS failures: locally, `--cafile hack/server.crt`. On a public domain the broker certificate is the Let's Encrypt certificate for `mqtt.$BASE_DOMAIN`, so the system CA bundle validates it. See [public-tls.md](public-tls.md).
