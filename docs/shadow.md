# Device shadow

`GET /devices/states/stream` follows reported state, desired state, and connection.

A device token streams the devices in that token. An account token streams the devices that account can read. `namespace` limits an account token to one readable namespace. Leaving it empty streams every namespace the account can read.

An account stream emits `DEVICE_ADDED` for the devices present at subscribe, and later when a device is created, moved in, renamed, retagged, or shared directly with that account. It emits `DEVICE_REMOVED` when a device is deleted or moved out. A later `DEVICE_ADDED` for a device already on the stream refreshes its title, namespace, and tags.

Each message has `eventId`. The same id is the same event, including after a retry. State messages include `title`, `namespace`, and `tags` when the registry has them.

`GET /devices/states`, `PATCH /devices/states`, and key removal still require a device token.
