# Reads, export, and partner access

These records describe who touched a device. They are product features for device makers and their partners.

A shadow `Get` that arrives with an account id or a token `jti` appends one row to the `AuditLog` collection: who, which device, when, and which token. The log is append-only. `GET /v1/audit` lists it by device or by account.

`GET /v1/devices/{device}/export` returns one JSON document: the registry entry, the current shadow, and references to the shadow keys and audit rows for that device.

`POST /v1/grants` creates an access edge with `expires_at`. The graph access check ignores an edge whose expiry is in the past. A zero expiry does not expire. The API removes expired edges about once a minute. Each grant also stores a consent row: who granted access, to whom, for which scope, when, and when it was revoked. `POST /v1/grants/{id}/revoke` removes the edge and sets `revoked_at`.
