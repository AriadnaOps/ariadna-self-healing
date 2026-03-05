package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/detection"
	"github.com/ariadna-ops/ariadna-self-healing/internal/monitor"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// otelScenarioLoader returns the OTel log detection scenario for e2e tests.
var otelScenarioLoader = &staticScenarioLoader{
	scenarios: []*detection.LoadedScenario{
		{
			ID:       "S2001",
			Name:     "High Severity OTel Log",
			Enabled:  true,
			Severity: types.SeverityMedium,
			Source:   "otel",
			Expression: `has(data.message) &&
(data.severity == "ERROR" || data.severity == "FATAL" ||
 (has(data.severityNumber) && data.severityNumber >= 17))`,
			Threshold: &detection.ThresholdConfig{
				Count:  1,
				Window: 5 * time.Minute,
			},
			Actions: []types.ActionConfig{
				{Type: types.ActionTypeNotify, Order: 1, Params: map[string]interface{}{"severity": "medium"}},
			},
		},
	},
}

// TestOTelDetection_MonitorToDetection validates the OTLP receiver pipeline:
//
//	OTel SDK -> OTLP/HTTP -> otelReceiverImpl -> detectionInputCh -> Detection Engine -> detectionResultCh
//
// Steps:
//  1. Configure operator with OTel enabled, K8s disabled.
//  2. Start OTel receiver (HTTP on 14318) and detection engine.
//  3. Use OTel SDK to export an ERROR log to the receiver.
//  4. Assert we receive a DetectionResult for scenario S2001.
func TestOTelDetection_MonitorToDetection(t *testing.T) {
	cfg := config.Default()
	cfg.Kubernetes.Enabled = false
	cfg.OTel.Receiver.Enabled = true
	cfg.OTel.Receiver.GRPC.Enabled = false
	cfg.OTel.Receiver.HTTP.Enabled = true
	cfg.OTel.Receiver.HTTP.Endpoint = "127.0.0.1:14318"

	detectionInputCh := make(chan types.DetectionInput, 100)
	detectionResultCh := make(chan types.DetectionResult, 100)

	engine, err := detection.NewEngine(cfg, logr.Discard(), detectionInputCh, detectionResultCh,
		detection.WithScenarioLoader(otelScenarioLoader))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	otelMonitor, err := monitor.NewOTelReceiver(cfg, logr.Discard(), detectionInputCh)
	if err != nil {
		t.Fatalf("NewOTelReceiver: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	go func() { _ = engine.Run(ctx) }()
	go func() { _ = otelMonitor.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for !engine.Ready() || !otelMonitor.Ready() {
		select {
		case <-deadline:
			t.Fatalf("engine ready=%v, otel monitor ready=%v — timed out",
				engine.Ready(), otelMonitor.Ready())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Export an ERROR log via OTLP/HTTP to our receiver
	exp, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpoint("127.0.0.1:14318"),
		otlploghttp.WithInsecure(),
	)
	if err != nil {
		t.Fatalf("create OTLP log exporter: %v", err)
	}
	defer func() { _ = exp.Shutdown(ctx) }()

	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	)
	defer func() { _ = loggerProvider.Shutdown(ctx) }()

	logger := loggerProvider.Logger("otel-e2e-test")
	var r log.Record
	r.SetSeverity(log.SeverityError)
	r.SetSeverityText("ERROR")
	r.SetBody(log.StringValue("E2E test error log"))
	r.SetTimestamp(time.Now())
	logger.Emit(ctx, r)
	_ = loggerProvider.ForceFlush(ctx)

	t.Log("Emitted ERROR log via OTLP. Waiting for detection result...")

	select {
	case result := <-detectionResultCh:
		t.Logf("Detection result: scenario=%s resource=%s",
			result.ScenarioName, result.Resource.String())
		if result.ScenarioID != "S2001" {
			t.Errorf("expected scenario S2001, got %s", result.ScenarioID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for OTel detection result")
	}

	cancel()
}
