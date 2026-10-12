# Device Authentication & MQTT

infinimesh has no device SDK. A device brings its own MQTT client.

Devices authenticate with a TLS client certificate. Port 8883 requires the certificate. Port 1883 accepts the same certificate's SHA-256 fingerprint, base64-encoded, as the MQTT password. The username is the device id. The device must be enabled, and basic auth must be enabled for that device. Turn basic auth on in the console (the lock on the device card) or with `POST /devices/<device-id>/toggle/basic`.

A device token (JWT) is for the HTTP, gRPC and Connect API. The bridge does not accept that token as the MQTT password.

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

QoS 0 is delivered once, with no acknowledgement. QoS 1 publishes receive `PUBACK`. QoS 2 uses `PUBREC`, `PUBREL`, and `PUBCOMP`. A duplicate QoS 2 publish is stored once.

Desired state is sent at the subscribed QoS. QoS 1 and 2 stay inflight until `PUBACK` or `PUBCOMP`, and are retried with the DUP flag. MQTT 3.1.1 keeps that inflight state for one hour when the clean session flag is 0. MQTT 5 keeps it for the session expiry interval in the CONNECT packet. Expiry 0 drops the session when the connection closes. The state lives in the bridge process. A bridge restart clears it.

The bridge reads the client keepalive and closes the connection after one and a half times that interval with no packet. MQTT 5 CONNECT and PUBLISH carry reason codes on `CONNACK`, `PUBACK`, `PUBREC`, `SUBACK`, and `DISCONNECT`. The bridge accepts session expiry, receive maximum, maximum packet size, topic alias maximum, topic aliases, message expiry, content type, response topic, correlation data, and user properties. Message expiry of zero is acknowledged and not stored. A will message is stored as reported state when its topic is `devices/<id>/state/reported` and the connection drops without a `DISCONNECT`. The bridge does not publish a reply to the response topic.

## Troubleshooting

- MQTT connection closed right after connect: the certificate is not registered, the device is disabled, or on 1883 the username is not the device id, the password is not the fingerprint, or basic auth is off.
- 401/permission errors: ensure token includes the device with at least read; reissue with `--allow-post` for writes.
- Connection refused: check broker host/port and that `docker compose ps` shows `mqtt-bridge` healthy.
- TLS failures: locally, `--cafile hack/server.crt`. On a public domain the broker certificate is the Let's Encrypt certificate for `mqtt.$BASE_DOMAIN`, so the system CA bundle validates it. See [public-tls.md](public-tls.md).
