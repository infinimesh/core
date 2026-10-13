# Rules and alarms

Rules are stored per namespace and evaluated by the `rules` service. A condition is CEL. The names it can read are `reported`, `desired`, `connected`, `device`, `title`, `tags`, and `ns`.

A reading stored as `reported.data.temp` is `reported.temp` in a condition. `reported.temp > 70` is true when that value is above 70. The same flattening applies to `desired`: the API field `desired.data.ota` is `desired.ota` in a rule.

The service watches every device in the namespace. A new or changed rule is checked against each device's current state. It does not wait for the next reading. A device added later, or a tag change, is checked the same way. `DEVICE_ADDED`, `TAG_ADDED`, and `TAG_REMOVED` on the namespace stream refresh that device.

Each action is one of `log`, `webhook`, `mqtt`, or `desired`. A webhook POST includes `X-Infinimesh-Signature: sha256=<hex>` when the action has a secret. The signature is HMAC-SHA256 of the body. MQTT publish uses `RULES_MQTT_BROKER` (`tcp://host:port`). A desired action patches that device's desired state and does not run when the alarm clears.

Alarm state for a rule and a device is `raised`, `acked`, or `cleared`. The stored `rule` field is the rule uuid. The stored `title` is the rule name, not the device title. The device title is the CEL name `title` and the action payload field `title`. Two rules with the same name do not share an alarm. An alarm whose `rule` is not a current rule uuid is removed on the next evaluation. The `rules` service writes the alarm to ArangoDB `Alarms` and Redis key `rules:alarm:<namespace>:<rule uuid>:<device>`. Ack and clear from the API are read back before the next evaluation. Deleting a rule deletes its alarms.

An alarm clears when its `clear` condition is true. A rule with no `clear` clears when `when` is false. Clear does not stick. The next check raises the alarm again while `when` is still true. `cooldown` is the shortest time after a raise before that can happen. A cooldown of 0 raises again on the next check. `repeat` fires the actions again while the alarm stays raised. Ack stops those repeats. A later clear still lets the alarm come back.

The REST gateway serves `GET` and `POST /v1/rules`, `PATCH` and `DELETE /v1/rules/{uuid}`, and `GET /v1/alarms`. `any_state=true` lists every alarm state. The console page is "Rules and Alarms". It creates, edits, and deletes rules, and lists, acks, and clears alarms. The alarm table shows the rule name and acks by the rule uuid.
