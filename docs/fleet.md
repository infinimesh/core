# Fleet provisioning

`BulkCreate` creates many devices in one namespace. Each entry can carry its own certificate. The caller needs namespace admin access. `max_devices` on the namespace, when greater than zero, rejects the call once the namespace is full. Zero means no limit.

`IssueClaim` stores a factory token for a namespace. A device calls `Claim` with that token and a certificate and is created in the namespace. Claim does not require an account token. The same device quota applies.

`RotateCertificate` keeps the previous fingerprint in `rollover_fingerprint` until the next rotation, so both certificates match `GetByFingerprint`. `RevokeCertificate` with an empty fingerprint sets `revoked`. A fingerprint appends `revoked_fingerprints` and drops it from the current and rollover fields. `GetByFingerprint` returns permission denied for a revoked certificate.

`POST /v1/devices/states:patchMany` writes one desired document to devices in a namespace or with a tag. The response lists a result for each device. `POST /v1/devices/ota:assign` sets an OTA target version the same way.
