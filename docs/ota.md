# Over-the-air updates

`desired.data.ota` and `reported.data.ota` are shadow documents. A patch may set any of these fields:

- `version` (string), the target to install
- `requested_version` (string), the version an account asked for
- `url` (string)
- `sha256`, 64 lowercase hex characters
- `size`, a whole number of zero or greater
- `status`: `idle`, `downloading`, `installing`, `success`, or `failed`
- `error` (string)
- `updated_at`, RFC3339

That status set is final. `scheduled`, `rebooting`, `canceled`, and `rolled_back` are not statuses. Any other key is rejected, including `state`, `progress`, `target`, `current`, `deployment_id`, and `backend_id`. An addon maps a backend state onto the five statuses and keeps backend ids in its own state file. A null `ota` clears the document. The check runs when state is patched.

A namespace stores `ota.enabled`, `ota.provider`, and `ota.webhook_url`. `provider` is `none`, `hawkbit`, `mender`, or `webhook`. `webhook_url` is required for `webhook`. `enabled` can be set only when an OTA addon is registered: a plugin of kind `OTA` that is public, or whose `namespace` is this namespace. `GET /v1/namespaces/{uuid}` sets `ota.addon_registered` on the response. The console switch stays off until that is true. With the switch off, nothing may write `desired.ota`.

When `provider` is `none`, a manage-level account may write `desired.ota`. A device token with manage access may write it on `POST /v1/devices/{id}/state`. `POST /v1/devices/ota:assign` does not accept a device token. That call returns 401 `bulk assign needs an account or addon token`. When the provider is `hawkbit`, `mender`, or `webhook`, only the bearer token issued for a registered OTA plugin with that provider may write `desired.ota.version`. A token issued for a different provider is refused. The namespace document is loaded for that check without the caller's account. The plugin is public, or its `namespace` is the device's namespace. The header `x-infinimesh-ota-provider` is not a credential. An account token cannot write `desired.ota.version` for those providers. It can store `desired.ota.requested_version` with `POST /v1/devices/ota:request`. Reported `ota` is not provider-gated.

`POST /v1/plugins` with kind `OTA` requires a root token and `provider` `hawkbit`, `mender`, or `webhook`. The create response includes `token` once. That value is not stored. Later reads return `token_id` only. The addon sends `Authorization: Bearer <token>` on `POST /v1/devices/ota:assign`, on desired `ota` patches, and on `reported.data.ota` patches. A reported patch may contain only `ota`. Any other reported key, or a connection update, is refused with `addon token can write desired ota only`. The same token may open `StreamShadow` read-only for a namespace it serves. A namespace plugin is limited to its namespace. A public plugin passes the namespace, and the provider has to match. A token with the stream claim is still refused. Deleting the plugin, or changing its provider, invalidates the previous token.

A root token comes from the root account `infinimesh`. Its password is `INF_DEFAULT_ROOT_PASS`. `POST /v1/token` with `{"auth":{"type":"standard","data":["infinimesh","<password>"]},"inf":true}` returns the token. The `inf` field has to be true. A login without it has no root claim, and `POST /v1/plugins` returns 403. `infinite` is the same flag.

`POST /v1/devices/ota:assign` accepts a manage-level account token. It sets `desired.data.ota.version` and `updated_at` for a namespace, a tag, or a list of devices. A refused device is still HTTP 200. The error is the `error` field on that device's result. The response `results` has one entry per device. Each success is an audit row with action `ota_assign` and the device's namespace. An account token sets `account` to that account. An addon token sets `account` to the plugin uuid and `token_id` to the addon token id. `POST /v1/devices/states:patchMany` and `POST /v1/devices/{id}/state` use the same gate when the desired document contains `ota`, and each success is audited the same way. Connect is `infinimesh.node.v1.ShadowService/AssignOTA`.

`POST /v1/devices/ota:request` is the account path when the provider is `hawkbit`, `mender`, or `webhook`. A manage-level account token sets `desired.data.ota.requested_version` and `updated_at`. It does not change `desired.data.ota.version`. An addon token and a device token are refused. A refused device is still HTTP 200, with `error` set on that result. Each success is an audit row with action `ota_request`. Connect is `infinimesh.node.v1.ShadowService/RequestOTA`.

Saving a namespace OTA config keeps `backend_url` and `backend` when the request leaves them empty.

A desired version change publishes `OTA_TARGET_SET` on the device state stream. A desired `requested_version` change publishes `OTA_REQUESTED`. A reported status change publishes `OTA_STATUS_CHANGED`. The persister does not store those events again. An addon subscribes to the stream.

The device card has no on/off switch. A namespace row shows whether OTA is on. The card footer shows that, the desired version, and `reported.data.ota.status`. The OTA section on the card shows the target, the reported status, version, URL, error, and `updated_at`. Assign calls `POST /v1/devices/ota:assign` when the provider is `none`. It calls `POST /v1/devices/ota:request` when the provider is `hawkbit`, `mender`, or `webhook`.

A rule reads `reported.ota` and `desired.ota`. The API and the stored shadow use `reported.data.ota` and `desired.data.ota`.
