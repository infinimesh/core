# Reads, export, and partner access

These records describe who touched a device. They are product features for device makers and their partners.

A shadow `Get` that arrives with an account id or a token `jti` appends one row to the `AuditLog` collection: who, which device, when, and which token. The log is append-only. Creating a device or minting a token does not append a row, so the log is empty until something reads that device's shadow with an account id or a token id. The REST gateway serves `GET /v1/audit`, which lists the log by device or by account. A request with neither returns an error.

`GET /v1/devices/{device}/export` returns one JSON document: the registry entry, the current shadow, and references to the shadow keys and audit rows for that device.

The REST gateway serves `POST /v1/grants`. That call creates an access edge with `expires_at`. The graph access check ignores an edge whose expiry is in the past. A zero expiry does not expire. The API removes expired edges about once a minute. Each grant also stores a consent row: who granted access, to whom, for which scope, when, and when it was revoked. `POST /v1/grants/{id}/revoke` removes the edge and sets `revoked_at`.
