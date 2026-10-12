# Files

`cmd/http-fs` is the media service the console already calls. The image is `ghcr.io/infinimesh/core/http-fs`. Compose starts it with the rest of the stack. Traefik serves it at `https://media.$BASE_DOMAIN`. The gateway `HTTP_FS` value is that URL, and the console reads it as `console_services.http_fs`.

The console media page is bound to the selected namespace.

| Call | What it does |
|---|---|
| `GET /{namespace}` | `{ "files": [{ "name", "size", "mod_time" }], "file_limit" }` |
| `POST /{namespace}/{name}` | multipart field `file` |
| `DELETE /{namespace}/{name}` | removes that file |
| `GET /{namespace}/{name}` | the file itself |

`GET /{namespace}/{name}` has no bearer check. The console opens that URL from a link, and the page says the file is public. List, upload, and delete send `Authorization: Bearer` with the same account token as the API. The token is HS256, signed with `SIGNING_KEY`, and the `account` claim is checked against the permission graph for that namespace. List needs read access. Upload and delete need management access.

Names are letters, digits, `.`, `_`, and `-`. The console strips other characters before upload.

Files go to `HTTP_FS_ROOT` (default `/static`). Set `HTTP_FS_S3_ENDPOINT` to use S3 instead. That endpoint is a host or an `http://` / `https://` URL, so MinIO, AWS, and Hetzner Object Storage all use the same variables. `HTTP_FS_S3_BUCKET` is required with an endpoint. `HTTP_FS_S3_REGION` defaults to `us-east-1`. `HTTP_FS_S3_ACCESS_KEY` and `HTTP_FS_S3_SECRET_KEY` are the credentials. `HTTP_FS_S3_PATH_STYLE` defaults on. Set it to `0` for a virtual-host AWS bucket.

`HTTP_FS_MAX_BYTES` defaults to 10485760. `HTTP_FS_TYPES` is a comma-separated allow list. `*` allows every type. The default list is `image/jpeg`, `image/png`, `image/gif`, `image/webp`, `application/pdf`, `text/plain`, `text/csv`, `application/json`, `application/zip`, and `application/octet-stream`. An `application/octet-stream` part is sniffed. Profile images are files of those image types. The console profile page does not call this service.

`DB_HOST`, `DB_CRED`, and `INF_DEFAULT_ROOT_PASS` are the same ArangoDB settings as the API. The process listens on `HTTP_FS_ADDR` (default `:8000`) and serves metrics on `:2112`.

A core tag `vX.Y.Z` or `vX.Y.Z-rN` publishes the image and `:latest`. A tag `vX.Y.Z-rc.N` publishes only that tag.
