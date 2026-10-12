# API versioning

The schema lives in [github.com/infinimesh/proto](https://github.com/infinimesh/proto). `api.swagger.json` in this repo is a copy of that module. `sh scripts/swagger_check.sh` fails when the copy drifts. `sh scripts/buf_lint_pin.sh` runs `buf lint` on that module. `buf breaking` runs in the proto repository against the latest tag. Until that tag exists, the check is skipped.

Protobuf packages are `infinimesh.*.v1`. REST methods are under `/v1`, and the previous path stays as an alias for one minor release. Create is `POST` on the `/v1` path. `exp`, `inf`, and `GetRequest.pool` remain, marked deprecated, beside `expires_at`, `infinite`, `devices`, and `device`.

API errors are Connect errors. Each one carries a `google.rpc.ErrorInfo` reason on domain `infinimesh.org`. The shadow process returns that same reason on its gRPC status. The API keeps the reason when it converts the status.

`POST /v1/ingest` posts reported JSON for a device token that names exactly one device and may post. The body is at most 16 KB, and that device is limited to 60 requests a minute. `GET /v1/ingest/desired?wait=N` returns desired JSON. `wait` is 0 to 60 seconds. A matching `If-None-Match` returns `304`.
