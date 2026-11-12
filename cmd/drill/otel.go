package main

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// startOTLP sends the harness's own traces and metrics over OTLP/gRPC to
// OTEL_EXPORTER_OTLP_ENDPOINT (default localhost:4317), so runs show up in
// Grafana, Tempo, Jaeger, Datadog or any OpenTelemetry backend.
func startOTLP(ctx context.Context) (func(), error) {
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName("drill-down-harness"), semconv.ServiceVersion(version)))
	if err != nil {
		return nil, err
	}
	te, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	me, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(te), sdktrace.WithResource(res))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me, sdkmetric.WithInterval(5*time.Second))))
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	return func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = errors.Join(tp.Shutdown(c), mp.Shutdown(c)) // flushes the last run
	}, nil
}
