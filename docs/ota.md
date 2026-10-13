# Over-the-air updates

`desired.data.ota` and `reported.data.ota` are shadow documents. A patch may set any of these fields:

- `version` (string), the target to install
- `url` (string)
- `sha256`, 64 lowercase hex characters
- `size`, a whole number of zero or greater
- `status`: `idle`, `downloading`, `installing`, `success`, or `failed`
- `error` (string)
- `updated_at`, RFC3339

Any other key is rejected, including `state` and `progress`. A null `ota` clears the document. The check runs when state is patched.

A namespace stores `ota.enabled`, `ota.provider`, and `ota.webhook_url`. `provider` is `none`, `hawkbit`, `mender`, or `webhook`. `webhook_url` is required for `webhook`. `enabled` can be set only when an OTA addon is registered: a plugin of kind `OTA` that is public, or whose `namespace` is this namespace. `GET /v1/namespaces/{uuid}` sets `ota.addon_registered` on the response. The console switch stays off until that is true. With the switch off, nothing may write `desired.ota`.

When `provider` is `none`, a manage-level account may write `desired.ota`. A device token with manage access may write it too. When the provider is `hawkbit`, `mender`, or `webhook`, only the bearer token issued for a registered OTA plugin with that provider may write `desired.ota`. The plugin is public, or its `namespace` is the device's namespace. The header `x-infinimesh-ota-provider` is not a credential. An account token cannot write `desired.ota` for those providers. Reported `ota` is not provider-gated.

`POST /v1/plugins` with kind `OTA` requires a root token and `provider` `hawkbit`, `mender`, or `webhook`. The create response includes `token` once. That value is not stored. Later reads return `token_id` only. The addon sends `Authorization: Bearer <token>` on `POST /v1/devices/ota:assign` and on desired `ota` patches. Deleting the plugin, or changing its provider, invalidates the previous token.

A root token comes from the root account `infinimesh`. Its password is `INF_DEFAULT_ROOT_PASS`. `POST /v1/token` with `{"auth":{"type":"standard","data":["infinimesh","<password>"]}}` returns the token.

`POST /v1/devices/ota:assign` accepts a manage-level account token. It sets `desired.data.ota.version` and `updated_at` for a namespace, a tag, or a list of devices. The response `results` has one entry per device. Each success is an audit row with action `ota_assign` and the device's namespace. `POST /v1/devices/states:patchMany` and `POST /v1/devices/{id}/state` use the same gate when the desired document contains `ota`, and each success is audited the same way. Connect is `infinimesh.node.v1.ShadowService/AssignOTA`.

Saving a namespace OTA config keeps `backend_url` and `backend` when the request leaves them empty.

A desired version change publishes `OTA_TARGET_SET` on the device state stream. A reported status change publishes `OTA_STATUS_CHANGED`. The persister does not store those events again. An addon subscribes to the stream.

The device card has no on/off switch. The card and the device list show `reported.data.ota.status`. Assign calls `POST /v1/devices/ota:assign`.

A rule reads `reported.ota` and `desired.ota`. The API and the stored shadow use `reported.data.ota` and `desired.data.ota`.
