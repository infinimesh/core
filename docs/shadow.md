# Device shadow

`GET /devices/states/stream` follows reported state, desired state, and connection. Connection state published on `mqtt.incoming` is delivered to the device stream. A sync snapshot includes the stored connection.

A device token streams the devices in that token. An account token streams the devices that account can read. `namespace` limits an account token to one readable namespace. Leaving it empty streams every namespace the account can read.

An account stream emits `DEVICE_ADDED` for the devices present at subscribe, and later when a device is created, moved in, renamed, retagged, or shared directly with that account. It emits `DEVICE_REMOVED` when a device is deleted or moved out. A later `DEVICE_ADDED` for a device already on the stream refreshes its title, namespace, and tags.

Each message has `eventId`. The same id is the same event, including after a retry. State messages include `title`, `namespace`, and `tags` when the registry has them.

`only_delta` defaults to false. A state message is then the stored shadow with the published change merged in. `only_delta` true sends that published change. `include` limits a state message to `REPORTED`, `DESIRED`, or `CONNECTION`. An empty `include` sends all three. Membership events are sent whole.

`GET /devices/states`, `PATCH /devices/states`, and key removal still require a device token.

`POST /devices/token` with `stream` true and an empty device map mints a read-only token for the caller account. `namespace` limits that token to one namespace the account can read. The token opens `GET /devices/states/stream` and the sync stream. It is rejected by every other API.
