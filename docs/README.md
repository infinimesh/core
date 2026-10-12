# Infinimesh Documentation

This folder contains the living, text-based docs for the platform. Start here:

- [Prerequisites](prerequisites.md) — OS support, required tools, and how to install them on Linux/Windows/macOS (Intel).
- [Installation & Local Run](installation.md) — prerequisites, Docker Compose bring-up, hostnames, defaults.
- [Public domain TLS](public-tls.md) — Traefik or Caddy, Let's Encrypt, and the device registry on a public name.
- [Device Authentication & MQTT](device-auth.md) — tokens vs certificates and MQTT examples.
- [Small-device TLS](guides/small-devices-tls.md) — handshake timeout, ECDSA certificates, session tickets, TLS-PSK, and fragment length.
- [Device shadow](shadow.md) — state streams for a device token, an account, or one namespace.
- [API versioning](api-versioning.md) — `/v1` paths, deprecated fields, and ingest.
- [Rules and alarms](rules.md) — CEL conditions, actions, and alarm state.
- [Over-the-air updates](ota.md) — `desired.ota` and `reported.ota`.
- [Fleet provisioning](fleet.md) — bulk create, claim, certificate rollover, and revocation.
- [Reads, export, and partner access](data-access.md) — audit log, device export, and expiring grants.
- [Production on Kubernetes](production-on-kubernetes.md) — Helm, metrics, and external databases.
- [CoAP ingest](coap.md) — reported state from UDP CoAP.
- [Files](http-fs.md) — namespace files and avatars on `http-fs`.

For API reference, see `api.swagger.json`. That file is the copy from the pinned [github.com/infinimesh/proto](https://github.com/infinimesh/proto) module.
