# Device Authentication & MQTT

infinimesh has no device SDK. A device brings its own MQTT client.

Devices authenticate with a TLS client certificate. Port 8883 requires the certificate. Port 1883 accepts the same certificate's SHA-256 fingerprint, base64-encoded, as the MQTT password. The username is the device id. The device must be enabled, and basic auth must be enabled for that device. Turn basic auth on in the console (the lock on the device card) or with `POST /devices/<device-id>/toggle/basic`.

A device token (JWT) is for the HTTP, gRPC and Connect API. The bridge does not accept that token as the MQTT password.

A token from device `Get` is read-only and expires in 24 hours. A token from `GetByFingerprint` may post reported state and expires in 24 hours. `POST /devices/token` with `post` true may write reported state without raising the token to the caller's level. Desired state still requires manage level. When `expires_at` and `exp` are both unset, that token expires in 24 hours. Each device token has a `jti`. `POST /v1/devices/token/revoke` denies that id until the token would have expired.

`POST /v1/ingest` accepts a JSON object as reported state. The bearer token must name exactly one device and be allowed to post. The body is at most 16 KB. `GET /v1/ingest/desired` returns the desired JSON.

Port 1883 is not encrypted. Anyone who knows the fingerprint can log in. Use it for local tests.

## Certificate

Generate a key/cert pair (self-signed example):
```bash
openssl genrsa -out device.key 4096
openssl req -new -x509 -sha256 -key device.key -out device.crt -days 365 \
  -subj "/CN=demo-sensor"
```

Upload the public cert when creating the device (UI or CLI `inf devices create` with `--crt device.crt`).

TLS:
```bash
mosquitto_pub --cafile hack/server.crt \
  --cert device.crt --key device.key \
  -t devices/demo-sensor/state/reported \
  -m '{"temp":21.5}' \
  -h api.${BASE_DOMAIN} -p 8883 --tls-version tlsv1.2
```

Plain MQTT, same device. The password is the base64 SHA-256 fingerprint of `device.crt`:
```bash
FP=$(openssl x509 -in device.crt -outform DER | openssl dgst -sha256 -binary | base64)
mosquitto_pub -h api.${BASE_DOMAIN} -p 1883 \
  -u demo-sensor -P "$FP" \
  -t devices/demo-sensor/state/reported \
  -q 1 \
  -m '{"temp":21.5}'
```

On a public domain the broker certificate is the Let's Encrypt certificate for `mqtt.$BASE_DOMAIN`, so the system CA bundle validates it. See [public-tls.md](public-tls.md).

## API token

The CLI uses a device token against the API, not against MQTT:
```bash
inf devices token <device-id> --allow-post > demo-sensor.token
inf devices state <device-id> --token "$(cat demo-sensor.token)" --report '{"online":true,"temp":21.5}'
inf devices state <device-id>
inf devices state <device-id> --patch '{"threshold":50}'
```

`inf devices state mqtt` can publish too. `--basic` is `device-id:password`. The CLI default topic is still `shadow/devices/%s/desired`, which this bridge rejects:
```bash
inf devices state mqtt --basic "demo-sensor:$FP" \
  --topic 'devices/%s/state/reported' --report '{"temp":21.5}'
inf devices state mqtt --device-id demo-sensor --basic "demo-sensor:$FP" \
  --topic 'devices/%s/state/desired' --desired
```

## Topics

A device may publish only `devices/<id>/state/reported` and subscribe only to `devices/<id>/state/desired`. `<id>` is that device's id. Wildcards and any other topic, including another device's, are rejected. A publish to `desired` is not stored as reported.

The retain flag is accepted on `reported`. The stored reported state is that payload. Payloads must be JSON objects. Desired state is pushed when the device subscribes, at the QoS granted for that subscription.

`shadow/devices/...` is not a bridge topic.

## QoS, session, and will

QoS 0 is delivered once, with no acknowledgement. QoS 1 publishes receive `PUBACK`. QoS 2 uses `PUBREC`, then `PUBREL` and `PUBCOMP`. The reported payload is stored when `PUBREL` arrives. A repeated QoS 2 packet id completes that handshake and is stored once.

Desired state is sent at the subscribed QoS. An MQTT 5 desired publish carries content type `application/json` when the sender did not set one, plus any response topic, correlation data, and user properties supplied with the send. Those properties stay on the message through retries and a session resume. QoS 1 and 2 stay inflight until `PUBACK` or `PUBCOMP`, and are retried with the DUP flag. MQTT 3.1.1 keeps that inflight state for one hour when the clean session flag is 0. MQTT 5 keeps it for the session expiry interval in the CONNECT packet. Expiry 0 drops the session when the connection closes. The state lives in the bridge process. A bridge restart clears it.

MQTT 5 `CONNACK` advertises a receive maximum of 32, a maximum packet size of 1 MiB, and a topic alias maximum of 16. Inbound topic aliases use that server maximum for the connection. The topic alias maximum in the client's `CONNECT` does not change it. An alias is forgotten when the connection closes. The client's maximum packet size limits publishes the bridge sends. An inbound packet larger than 1 MiB is rejected.

The bridge reads the client keepalive and closes the connection after one and a half times that interval with no packet. It also checks that the device is still enabled at least every 30 seconds. A disabled device is disconnected with not authorized, and that disconnect does not store the will. MQTT 5 `CONNACK`, `PUBACK`, `PUBREC`, `SUBACK`, and `DISCONNECT` carry reason codes. The bridge accepts session expiry, the client's receive maximum, the client's maximum packet size, topic aliases, message expiry, content type, response topic, correlation data, and user properties. Message expiry of zero is acknowledged and not stored. A later expiry is applied again while the message waits to be sent or for `PUBREL`; once that time has passed the message is dropped. The remaining interval is written on the outbound publish. The bridge holds at most 32 inbound QoS 2 packet ids. Another MQTT 5 QoS 2 publish receives `PUBREC` reason `0x93` and is not stored. A held id is dropped after two minutes, or sooner when its message expiry elapses. A `PUBREL` for a missing id receives `PUBCOMP` reason `0x92`. A will message is stored as reported state when its topic is `devices/<id>/state/reported` and the connection drops without a `DISCONNECT`. The bridge does not publish a reply to the response topic.

## Troubleshooting

- MQTT connection closed right after connect: the certificate is not registered, the device is disabled, or on 1883 the username is not the device id, the password is not the fingerprint, or basic auth is off.
- 401/permission errors: ensure token includes the device with at least read; reissue with `--allow-post` for writes.
- Connection refused: check broker host/port and that `docker compose ps` shows `mqtt-bridge` healthy.
- TLS failures: locally, `--cafile hack/server.crt`. On a public domain the broker certificate is the Let's Encrypt certificate for `mqtt.$BASE_DOMAIN`, so the system CA bundle validates it. See [public-tls.md](public-tls.md).
