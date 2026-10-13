# Over-the-air updates

`desired.data.ota` and `reported.data.ota` are ordinary shadow JSON. The platform does not interpret them. An OTA addon reads and writes these fields.

Desired:

- `version` is the target the device should install.
- `url` is where the image is fetched.
- `sha256` is the image digest.

Reported adds:

- `state` is the device's own status string, such as `downloading` or `installed`.
- `progress` is a number from 0 to 100.
- `error` is the last failure string.

A namespace stores the addon settings on `Namespace.ota`: `enabled`, `backend_url`, and `backend`. `backend` is `hawkBit` or `mender`. The console "Enable OTA" switch on the namespace page writes that object. The device card has no on/off switch. It shows `reported.data.ota` and sets `desired.data.ota.version`.

A rule reads `reported.ota` and `desired.ota`. The API and the stored shadow use `reported.data.ota` and `desired.data.ota`.

`TAG_ADDED` and `TAG_REMOVED` on the account or namespace shadow stream carry the device's tags. A rollout group that follows a tag can update from that stream. `POST /v1/devices/states:patchMany` sets desired state for every device in a namespace, or for every device with a tag, that the caller can manage.
