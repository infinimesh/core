# Production on Kubernetes

Production installs use the [infinimesh/k8s](https://github.com/infinimesh/k8s) chart `charts/infinimesh`.

```bash
helm install infinimesh charts/infinimesh
```

`api`, `shadow`, and `mqtt-bridge` default to 2 replicas. Each has a PodDisruptionBudget with `minAvailable: 1` when the replica count is greater than 1. Image tags default to `v3.4.3` and `IfNotPresent`. A tag of `latest` uses `Always`.

`external.arangodb`, `external.rabbitmq`, and `external.redis` skip the in-cluster databases. Set `external.hosts.*` to the host and port the processes should dial.

Each process serves Prometheus metrics on `:2112`. `METRICS_ADDR` changes that address. `metrics.serviceMonitor` creates a ServiceMonitor for that port. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to export traces over OTLP gRPC. An empty value leaves tracing off.

`MQTT_SESSION_STORE=redis` keeps MQTT session subscriptions and QoS 1/2 inflight state in Redis so a reconnect can land on another bridge replica. The default store is the bridge process memory.

Namespace `quota.max_devices`, `quota.device_messages_per_second`, and `quota.namespace_messages_per_second` are enforced when they are greater than zero. Zero does not limit. Message rates apply on the MQTT bridge and on `POST /v1/ingest`, in addition to the ingest limit of 60 requests a minute per device.

The chart and the kustomize base also run `rules` (one replica, metrics on 2112), `http-fs` (port 8000, local volume), and can run `coap-gateway`. Helm leaves CoAP off unless `coap.enabled` is true. The CoAP Service is ClusterIP. Port 5683 is plaintext UDP with no DTLS. `httpFs.publicURL` is the `HTTP_FS` value on the gateway. Leave it empty and the console hides the media page. Set `httpFs.s3.bucket` and `httpFs.s3.endpoint` to store objects in S3 instead of the volume. The chart image tag is `v3.4.3`. Later release candidates are not written into the chart on their own.

The MQTT Service publishes 8883. Plaintext 1883 is off unless `mqtt.plaintext` is true. The bridge reads `MQTT_PLAINTEXT`.

The platform-operator still reconciles a Platform into the same processes, including `rules`, `http-fs`, and `coap`. It does not publish MQTT 1883. `http-fs` uses a volume at `/static`. It does not delete older Kafka or Dgraph workloads. `kubectl apply -k base` remains the plain kustomize install and still uses `:latest`.

Create the MQTT TLS secret before the bridge serves 8883. The sample pair is `hack/server.crt` and `hack/server.key` in core. Change the chart auth values before the cluster is reachable.
