# Reads, export, and partner access

These records describe who touched a device. They are product features for device makers and their partners.

The `AuditLog` collection is append-only. Each row is who, which device, when, which token, and an action. A shadow `Get` that arrives with an account id or a token `jti` appends action `read`. Creating a device appends action `create` with the account and the device. Minting a device token appends action `token` with the account, each device on the token, and the token id. `GET /v1/audit?device=` and `GET /v1/audit?account=` both return those rows. A request with neither returns an error.

`GET /v1/devices/{device}/export` returns one JSON document: the registry entry, the current shadow, and references to the shadow keys and audit rows for that device.

The REST gateway serves `POST /v1/grants`. That call creates an access edge with `expires_at`. The graph access check ignores an edge whose expiry is in the past. A zero expiry does not expire. The API removes expired edges about once a minute. Each grant also stores a consent row: who granted access, to whom, for which scope, when, and when it was revoked. `POST /v1/grants/{id}/revoke` removes the edge and sets `revoked_at`.
