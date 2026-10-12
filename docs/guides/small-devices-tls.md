# Small-device TLS

The MQTT bridge terminates TLS on port 8883 with Go's `crypto/tls`. Port 1883 is plaintext. Device identity on 8883 is the SHA-256 fingerprint of the client certificate, including after a session resume. See [device-auth.md](../device-auth.md).

## Handshake timeout

`TLS_HANDSHAKE_TIMEOUT` is how long the bridge waits for the TLS handshake after it accepts the TCP connection. The default is `30s`. The value is a Go duration (`30s`, `1m`). Zero, a negative duration, and a number with no unit are rejected and the process exits.

The timer covers `Handshake` only. The MQTT `CONNECT` packet is read after the handshake succeeds.

## Certificates

The listener requires TLS 1.2 or newer. TLS 1.0 and TLS 1.1 are refused.

`TLS_CERT_FILE` and `TLS_KEY_FILE` are the primary pair. The defaults are `/cert/tls.crt` and `/cert/tls.key`. When `ACME` is set, that Traefik certificate replaces this pair. See [public-tls.md](../public-tls.md).

`TLS_EC_CERT_FILE` and `TLS_EC_KEY_FILE` are an optional ECDSA pair, served beside the primary certificate. Set both or neither. The ECDSA file must be an ECDSA certificate. The client selects the pair its signature algorithms allow. A TLS 1.2 client that offers only ECDSA cipher suites gets the ECDSA certificate. A client that offers only RSA cipher suites gets the primary certificate.

`hack/server-ec.crt` and `hack/server-ec.key` are a P-256 test pair. The certificate is a server leaf, not a CA, and its subject alternative names are `DNS:localhost` and `IP:127.0.0.1`. `hack/server.crt` has `CN=localhost` and no subject alternative name. Strict clients reject that certificate. Point a local bridge at the P-256 pair when the client checks the name:

```bash
TLS_EC_CERT_FILE=hack/server-ec.crt TLS_EC_KEY_FILE=hack/server-ec.key
```

Do not serve `hack/server-ec.crt` next to a public certificate. A client that prefers ECDSA would then receive a certificate for `localhost`. On a public name, put a certificate for that name in `TLS_EC_CERT_FILE`, or leave the pair unset.

## Session tickets

Session tickets stay enabled. `crypto/tls` sends a ticket lifetime of 7 days (`maxSessionTicketLifetime`). The process rotates its ticket key every 24 hours. A key can still decrypt tickets for 7 days after it was created. The keys are kept in memory. A bridge restart, or a connection that lands on another replica, cannot resume a ticket from the previous process. The client runs a full handshake instead.

A resumed handshake still identifies the device. Go stores the client certificate in the ticket and restores it on `ConnectionState.PeerCertificates` before `Handshake` returns. The bridge fingerprints that leaf the same way as a full handshake. The certificate is not presented again on the wire. `DidResume` is true.

`VerifyPeerCertificate` does not run on a resumed connection. The bridge does not use that callback. It reads the restored certificate after the handshake. A resumed certificate that is past `NotAfter` is rejected by `crypto/tls`.

## TLS-PSK

Go's `crypto/tls` has no TLS-PSK (pre-shared key) implementation. That is [golang/go#6379](https://github.com/golang/go/issues/6379). The bridge does not accept a PSK, and there is no environment variable that turns one on.

An optional terminator can sit in front of the bridge. [stunnel](https://www.stunnel.org/) is one. It accepts TLS-PSK from the device and opens a new TLS connection to the bridge with that device's client certificate. The bridge still requires the certificate on 8883 and still fingerprints it. The PSK identity is not forwarded. One stunnel service presents one client certificate, so one service is one device. Several identities in the same secrets file would all arrive as that one device.

The sample is [hack/stunnel/psk.conf](../../hack/stunnel/psk.conf). The PSK listener is `127.0.0.1:8884` and speaks TLS 1.2, which is the version those PSK cipher suites use. The hop to `127.0.0.1:19001` is plaintext MQTT and stays on localhost. The second service connects to the bridge on `127.0.0.1:8883` with the device certificate registered in infinimesh. `verifyPeer` pins the bridge leaf in `CAfile`. For this repository's local pair, that is `hack/server-ec.crt` when the bridge is serving it, because `hack/server.crt` has no subject alternative name. A public bridge uses `verifyChain` and the CA bundle for `mqtt.$BASE_DOMAIN`, with `checkHost` set to that name. Generate a new PSK. Do not deploy the example key.

```bash
openssl rand -hex 16
```

Write one line, `identity:hexkey`, to the secrets file named in the config. The identity is a label for the device. The key is at least 16 bytes, hex encoded.

```bash
stunnel hack/stunnel/psk.conf
```

Paths in the sample are relative to the working directory. Run that command from the repository root, or replace them with absolute paths. Port 8883 stays the certificate listener. Other certificate clients keep using it.

## Maximum fragment length

Maximum fragment length negotiation (RFC 6066 `max_fragment_length`) is not supported. `crypto/tls` does not implement the extension, and the bridge has no setting for it. A client may send the extension. The bridge ignores it and may still send records up to 16 KiB. A device that only accepts 512-byte or 1 KiB records cannot use this listener.
