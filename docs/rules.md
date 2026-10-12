# Rules and alarms

Rules are stored per namespace and evaluated by the `rules` service. A condition is CEL. The names it can read are `reported`, `desired`, `connected`, `device`, `title`, `tags`, and `ns`.

Each action is one of `log`, `webhook`, `mqtt`, or `desired`. A webhook POST includes `X-Infinimesh-Signature: sha256=<hex>` when the action has a secret. The signature is HMAC-SHA256 of the body. MQTT publish uses `RULES_MQTT_BROKER` (`tcp://host:port`). A desired action patches that device's desired state and does not run when the alarm clears.

Alarm state for a rule and a device is `raised`, `acked`, or `cleared`. `cooldown` is the shortest gap between raises. `repeat` fires the actions again while the alarm stays raised. The `rules` service writes the alarm to ArangoDB `Alarms` and Redis key `rules:alarm:<namespace>:<rule>:<device>`. Ack and clear from the API are read back before the next evaluation.

The REST gateway serves `GET` and `POST /v1/rules`, `PATCH` and `DELETE /v1/rules/{uuid}`, and `GET /v1/alarms`. `any_state=true` lists every alarm state. The console page is "Rules and Alarms". It creates, edits, and deletes rules, and lists, acks, and clears alarms.
