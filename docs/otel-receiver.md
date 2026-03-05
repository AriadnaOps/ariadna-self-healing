# OTLP Receiver (OpenTelemetry)

The operator includes an OTLP receiver that accepts telemetry (logs, and in the future metrics and traces) via the standard OpenTelemetry protocol. This allows applications instrumented with the OTel SDK to send data directly to the operator for detection and remediation.

## Cluster prerequisites

To test the OTLP receiver in a Kubernetes cluster:

### 1. Operator deployed with OTel enabled

The OTLP receiver is enabled by default (`otel.receiver.enabled: true`). **With multiple replicas, only the Leader pod runs the OTLP receiver**. Use the dedicated OTLP Service (`selfhealing-operator-otlp`) so traffic routes only to the Leader. See [Architecture](architecture.md#leader--workers) for why only the Leader runs the pipeline. Verify the configuration:

```yaml
# config/default or your overlay
otel:
  receiver:
    enabled: true
    grpc:
      enabled: true
      endpoint: "0.0.0.0:4317"
    http:
      enabled: true
      endpoint: "0.0.0.0:4318"
```

### 2. OTLP Service

The `selfhealing-operator-otlp` Service routes OTLP traffic only to the Leader pod (selector includes `ariadna-ops.com/leader: "true"`). The default manifest (`config/default/service-otlp.yaml`) is included in the deployment.

### 3. ScenarioLibrary OTel applied

Apply the ScenarioLibrary that defines OTel log detection:

```bash
kubectl apply -f test/e2e/testdata/manifests/scenariolibrary-otel.yaml
```

### 4. Applications sending OTLP to the operator

Configure your applications to export OTLP to the operator. From inside the cluster, the endpoint is:

- **HTTP**: `http://selfhealing-operator-otlp.selfhealing-system.svc:4318/v1/logs` (include `/v1/logs` in the URL)
- **gRPC**: `selfhealing-operator-otlp.selfhealing-system.svc:4317`

#### Environment variables for instrumented applications

```bash
# OTLP HTTP (recommended for logs)
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://selfhealing-operator-otlp.selfhealing-system.svc:4318/v1/logs
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf

# OTLP gRPC
OTEL_EXPORTER_OTLP_ENDPOINT=http://selfhealing-operator-otlp.selfhealing-system.svc:4317
OTEL_EXPORTER_OTLP_PROTOCOL=grpc
```

## Testing the receiver

### E2E test (no cluster)

```bash
go test ./test/e2e/ -run TestOTelDetection -v
```

This test starts the OTLP receiver on HTTP (port 14318), emits an ERROR log via the OTel SDK, and verifies that a `DetectionResult` is produced for scenario S2001.

### Manual test in cluster

1. Deploy the operator and ScenarioLibrary OTel.
2. Build and load the otel-sender image (for local clusters like kind/minikube):

   ```bash
   make build-sender docker-build-sender
   # For kind: kind load docker-image ghcr.io/ariadna-ops/otel-sender:dev
   # For minikube: eval $(minikube docker-env) && make docker-build-sender
   ```

3. Run the test Job that sends an OTLP log:

   ```bash
   kubectl apply -f test/e2e/testdata/manifests/otel-sender-job.yaml
   kubectl logs -f job/otel-sender -n ariadna
   ```

4. Check the operator logs for the emitted `DetectionResult`.

### Using OpenTelemetry Collector as gateway

If your applications send OTLP to a Collector, you can configure the Collector to re-export to the operator:

```yaml
exporters:
  otlphttp:
    endpoint: http://selfhealing-operator-otlp.selfhealing-system.svc:4318/v1/logs
    tls:
      insecure: true

service:
  pipelines:
    logs:
      receivers: [otlp]
      exporters: [otlphttp]
```

## Data available for CEL

OTel logs are converted to `DetectionInput` with the following structure:

| Field | Description |
|-------|-------------|
| `data.message` | Log body (string) |
| `data.severity` | Severity text (INFO, WARN, ERROR, FATAL) |
| `data.severityNumber` | OTel number (9=INFO, 17=ERROR, 21=FATAL) |
| `data.<attr>` | Log record attributes |
| `labels.telemetryType` | Always `"log"` for logs |
| `labels.k8s.namespace.name` | Pod namespace (if present) |
| `labels.k8s.pod.name` | Pod name (if present) |

Example CEL expression for ERROR/FATAL logs:

```cel
has(data.message) &&
(data.severity == "ERROR" || data.severity == "FATAL" ||
 (has(data.severityNumber) && data.severityNumber >= 17))
```

## Configuration

| Parameter | Default | Description |
|-----------|---------|-------------|
| `otel.receiver.enabled` | `true` | Enable OTLP receiver |
| `otel.receiver.grpc.enabled` | `true` | Enable gRPC endpoint |
| `otel.receiver.grpc.endpoint` | `0.0.0.0:4317` | gRPC endpoint |
| `otel.receiver.http.enabled` | `true` | Enable HTTP endpoint |
| `otel.receiver.http.endpoint` | `0.0.0.0:4318` | HTTP endpoint |
