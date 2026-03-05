package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"

	// Logs exporter is still evolving in OTel-Go; this repo uses the otlplog http exporter.
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"

	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

type OTelClosers struct {
	ShutdownTrace  func(context.Context) error
	ShutdownMetric func(context.Context) error
	ShutdownLog    func(context.Context) error
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Errorf("missing env %s", key))
	}
	return v
}

func initOTel(ctx context.Context, serviceName string) (OTelClosers, *slog.Logger, error) {
	endpoint := mustEnv("OTEL_EXPORTER_OTLP_ENDPOINT") // http://otel-collector:4318

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithHost(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			attribute.String("demo", "obs-demo"),
		),
	)
	if err != nil {
		return OTelClosers{}, nil, err
	}

	// ---- Traces ----
	traceExp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpoint+"/v1/traces"),
	)
	if err != nil {
		return OTelClosers{}, nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	// ---- Metrics ----
	metricExp, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(endpoint+"/v1/metrics"),
	)
	if err != nil {
		return OTelClosers{}, nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(5*time.Second))),
	)
	otel.SetMeterProvider(mp)

	// ---- Logs ----
	logExp, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpointURL(endpoint+"/v1/logs"),
	)
	if err != nil {
		// If logs exporter is unavailable in your environment, return an error with a helpful hint.
		return OTelClosers{}, nil, errors.New("failed to init OTLP log exporter (OTel-Go logs API may be missing in your Go environment). If this happens, pin newer OTel-Go, or switch to stdout+Promtail")
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	global.SetLoggerProvider(lp)

	// Bridge slog -> OTel logs (includes trace context automatically)
	logger := slog.New(otelslog.NewHandler(serviceName))

	return OTelClosers{
		ShutdownTrace:  tp.Shutdown,
		ShutdownMetric: mp.Shutdown,
		ShutdownLog:    lp.Shutdown,
	}, logger, nil
}
