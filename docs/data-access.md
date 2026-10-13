# Reads, export, and partner access

These records describe who touched a device. They are product features for device makers and their partners.

The `AuditLog` collection is append-only. Each row is who, which device, when, which token, which namespace, and an action. A shadow `Get` that arrives with an account id or a token `jti` appends action `read`. Creating a device appends action `create` with the account and the device. Minting a device token appends action `token` with the account, each device on the token, and the token id. Each device whose desired `ota` write succeeds appends action `ota_assign`. An addon write sets `account` to the plugin uuid and `token_id` to the addon token id. Acknowledging an alarm appends `alarm_ack`. Clearing an alarm appends `alarm_clear`. Creating a plugin appends `plugin_create`. Deleting a plugin appends `plugin_delete`. Both name the root account and the plugin's namespace. `GET /v1/audit` takes `device`, `account`, or `namespace`. A request with none of them returns an error. A namespace filter requires read access to that namespace.

`GET /v1/devices/{device}/export` returns one JSON document: the registry entry, the current shadow, and references to the shadow keys and audit rows for that device.

The REST gateway serves `POST /v1/grants`. That call creates an access edge with `expires_at`. The graph access check ignores an edge whose expiry is in the past. A zero expiry does not expire. The API removes expired edges about once a minute. Each grant also stores a consent row: who granted access, to whom, for which scope, when, and when it was revoked. `POST /v1/grants/{id}/revoke` removes the edge and sets `revoked_at`.
