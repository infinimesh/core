# CoAP ingest

`cmd/coap-gateway` listens for CoAP on UDP (`COAP_ADDR`, default `:5683`). A confirmable or non-confirmable POST to `devices/<id>/state/reported` with a JSON body is published on the RabbitMQ topic `mqtt.incoming`, the same topic the MQTT bridge uses for reported state. The response is `2.04 Changed` when the JSON is accepted.

Compose starts it with the `coap` profile. DTLS is not terminated in this process. Boards that need DTLS PSK use a terminator in front of this UDP port. See `docs/guides/small-devices-tls.md`.
