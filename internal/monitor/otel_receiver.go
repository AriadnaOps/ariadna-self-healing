package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/configgrpc"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/confignet"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// otelReceiverImpl implements Monitor using OTLP (gRPC/HTTP). It converts incoming telemetry to DetectionInput using resource attributes (e.g. k8s.pod.name) for ResourceReference.
type otelReceiverImpl struct {
	config   *config.Config
	log      logr.Logger
	outputCh chan<- types.DetectionInput

	ready    bool
	readyMu  sync.RWMutex
	stopOnce sync.Once

	// OTLP receiver components (created in Run; only logs used for detection)
	logsReceiver receiver.Logs
	host         component.Host
}

// newOTelReceiverImpl creates a new otelReceiverImpl.
func newOTelReceiverImpl(cfg *config.Config, log logr.Logger, outputCh chan<- types.DetectionInput) (*otelReceiverImpl, error) {
	return &otelReceiverImpl{
		config:   cfg,
		log:      log.WithName("otel-receiver"),
		outputCh: outputCh,
		host:     componenttest.NewNopHost(),
	}, nil
}

// Run starts the OTLP receiver and blocks until context is cancelled.
func (r *otelReceiverImpl) Run(ctx context.Context) error {
	r.log.Info("Starting OTel receiver",
		"grpcEnabled", r.config.OTel.Receiver.GRPC.Enabled,
		"grpcEndpoint", r.config.OTel.Receiver.GRPC.Endpoint,
		"httpEnabled", r.config.OTel.Receiver.HTTP.Enabled,
		"httpEndpoint", r.config.OTel.Receiver.HTTP.Endpoint,
	)

	// Build OTLP receiver config from our config
	otlpCfg := r.buildOTLPConfig()
	if err := otlpCfg.Validate(); err != nil {
		return fmt.Errorf("invalid OTLP config: %w", err)
	}

	factory := otlpreceiver.NewFactory()
	settings := receivertest.NewNopSettings()

	// Create consumers that convert OTLP data to DetectionInput
	logsConsumer, err := consumer.NewLogs(r.consumeLogs)
	if err != nil {
		return fmt.Errorf("create logs consumer: %w", err)
	}

	// Create and start receivers for each enabled signal
	if r.config.OTel.Receiver.GRPC.Enabled || r.config.OTel.Receiver.HTTP.Enabled {
		r.logsReceiver, err = factory.CreateLogsReceiver(ctx, settings, otlpCfg, logsConsumer)
		if err != nil {
			return fmt.Errorf("create logs receiver: %w", err)
		}
		if r.logsReceiver != nil {
			if err := r.logsReceiver.Start(ctx, r.host); err != nil {
				return fmt.Errorf("start logs receiver: %w", err)
			}
		}
	}

	r.setReady(true)
	r.log.Info("OTel receiver ready")

	<-ctx.Done()
	return nil
}

// buildOTLPConfig builds otlpreceiver.Config from our config.
func (r *otelReceiverImpl) buildOTLPConfig() *otlpreceiver.Config {
	cfg := &otlpreceiver.Config{}

	if r.config.OTel.Receiver.GRPC.Enabled {
		cfg.GRPC = &configgrpc.ServerConfig{
			NetAddr: confignet.AddrConfig{
				Endpoint:  r.config.OTel.Receiver.GRPC.Endpoint,
				Transport: confignet.TransportTypeTCP,
			},
		}
	}
	if r.config.OTel.Receiver.HTTP.Enabled {
		httpCfg := confighttp.NewDefaultServerConfig()
		httpCfg.Endpoint = r.config.OTel.Receiver.HTTP.Endpoint
		httpCfg.TLSSetting = nil // Plain HTTP (no TLS) for OTLP ingestion
		cfg.HTTP = &otlpreceiver.HTTPConfig{
			ServerConfig:   &httpCfg,
			TracesURLPath:  "/v1/traces",
			MetricsURLPath: "/v1/metrics",
			LogsURLPath:    "/v1/logs",
		}
	}
	return cfg
}

// consumeLogs converts plog.Logs to DetectionInput and sends to outputCh.
func (r *otelReceiverImpl) consumeLogs(ctx context.Context, ld plog.Logs) error {
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		rl := ld.ResourceLogs().At(i)
		resource := rl.Resource()
		for j := 0; j < rl.ScopeLogs().Len(); j++ {
			sl := rl.ScopeLogs().At(j)
			for k := 0; k < sl.LogRecords().Len(); k++ {
				lr := sl.LogRecords().At(k)
				input := r.logRecordToDetectionInput(resource, lr)
				r.sendDetectionInput(input)
			}
		}
	}
	return nil
}

// logRecordToDetectionInput converts a log record and its resource to DetectionInput.
func (r *otelReceiverImpl) logRecordToDetectionInput(resource pcommon.Resource, lr plog.LogRecord) types.DetectionInput {
	// Extract K8s resource attributes for ResourceReference
	attrs := resource.Attributes()
	ns, _ := attrs.Get("k8s.namespace.name")
	podName, _ := attrs.Get("k8s.pod.name")
	uid, _ := attrs.Get("k8s.pod.uid")
	serviceName, _ := attrs.Get("service.name")

	kind := "Pod"
	name := podName.Str()
	if name == "" {
		name = serviceName.Str()
		kind = "Service"
	}
	if name == "" {
		name = "unknown"
	}

	// Build Data map for CEL evaluation
	data := map[string]interface{}{
		"message":  lr.Body().AsString(),
		"severity": lr.SeverityText(),
	}
	if lr.SeverityNumber() != plog.SeverityNumberUnspecified {
		data["severityNumber"] = int32(lr.SeverityNumber())
	}

	// Add log record attributes to data
	lr.Attributes().Range(func(k string, v pcommon.Value) bool {
		data[k] = v.AsRaw()
		return true
	})

	// Add resource attributes to labels for filtering
	labels := map[string]string{
		"telemetryType": "log",
	}
	attrs.Range(func(k string, v pcommon.Value) bool {
		labels[k] = v.AsString()
		return true
	})

	return types.DetectionInput{
		ID:        uuid.New().String(),
		Source:    types.DetectionSourceOTel,
		Resource:  types.ResourceReference{APIVersion: "v1", Kind: kind, Namespace: ns.Str(), Name: name, UID: uid.Str()},
		Timestamp: lr.Timestamp().AsTime(),
		Data:      data,
		Labels:    labels,
	}
}

// sendDetectionInput sends a detection input to the output channel (non-blocking).
func (r *otelReceiverImpl) sendDetectionInput(input types.DetectionInput) {
	select {
	case r.outputCh <- input:
		r.log.V(2).Info("Sent OTel detection input", "id", input.ID, "source", input.Source, "resource", input.Resource.String())
	default:
		r.log.V(1).Info("Detection input channel full, dropping OTel input", "id", input.ID)
	}
}

// Stop gracefully stops the receiver.
func (r *otelReceiverImpl) Stop(ctx context.Context) error {
	var err error
	r.stopOnce.Do(func() {
		r.log.Info("Stopping OTel receiver")
		r.setReady(false)

		if r.logsReceiver != nil {
			stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if stopErr := r.logsReceiver.Shutdown(stopCtx); stopErr != nil {
				err = stopErr
			}
		}
	})
	return err
}

// Ready returns true when the receiver is ready to accept data.
func (r *otelReceiverImpl) Ready() bool {
	r.readyMu.RLock()
	defer r.readyMu.RUnlock()
	return r.ready
}

func (r *otelReceiverImpl) setReady(ready bool) {
	r.readyMu.Lock()
	defer r.readyMu.Unlock()
	r.ready = ready
}
