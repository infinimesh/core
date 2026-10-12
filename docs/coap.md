# CoAP ingest

`cmd/coap-gateway` listens for CoAP on UDP (`COAP_ADDR`, default `:5683`). A confirmable or non-confirmable POST to `devices/<id>/state/reported` with a JSON body is published on the RabbitMQ topic `mqtt.incoming`, the same topic the MQTT bridge uses for reported state. The response is `2.04 Changed` when the JSON is accepted.

This process does not speak DTLS. Port 5683 is plaintext UDP. Keep it on a private network. Compose starts it only with the `coap` profile. Boards that need DTLS PSK use a terminator in front of this UDP port. See `docs/guides/small-devices-tls.md`.
