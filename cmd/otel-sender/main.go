// otel-sender sends a single ERROR log via OTLP/HTTP for e2e testing.
//
// Usage:
//
//	otel-sender [-endpoint=URL]
//
// Default endpoint: http://127.0.0.1:4318/v1/logs
// In-cluster: http://selfhealing-operator-otlp.selfhealing-system.svc:4318/v1/logs
//
// With multiple operator replicas (leader election), only the Leader runs the OTLP
// receiver. The sender retries on connection refused to handle hitting a Worker pod.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

const maxRetries = 5
const retryDelay = 2 * time.Second

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:4318/v1/logs", "OTLP HTTP endpoint (include /v1/logs)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exp, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpointURL(*endpoint),
		otlploghttp.WithInsecure(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create exporter: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = exp.Shutdown(ctx) }()

	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	)
	defer func() { _ = provider.Shutdown(ctx) }()

	logger := provider.Logger("otel-sender")
	var r log.Record
	r.SetSeverity(log.SeverityError)
	r.SetSeverityText("ERROR")
	r.SetBody(log.StringValue("E2E test error from otel-sender"))
	r.SetTimestamp(time.Now())
	logger.Emit(ctx, r)

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := provider.ForceFlush(ctx); err != nil {
			lastErr = err
			if isRetryable(err) && attempt < maxRetries-1 {
				fmt.Fprintf(os.Stderr, "flush attempt %d failed (will retry): %v\n", attempt+1, err)
				time.Sleep(retryDelay)
				continue
			}
			fmt.Fprintf(os.Stderr, "flush: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "flush: %v\n", lastErr)
	os.Exit(1)
}

func isRetryable(err error) bool {
	// Connection refused when hitting a Worker pod (OTLP receiver runs only on Leader)
	if errors.Is(err, net.ErrClosed) {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") || strings.Contains(s, "connect: connection refused")
}
