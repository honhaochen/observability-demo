package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type resp struct {
	TotalMs int64  `json:"total_ms"`
	Result  string `json:"result"`
	TraceID string `json:"trace_id"`
}

var (
	reqCounter metric.Int64Counter
	durHist    metric.Float64Histogram
)

func initMetrics(service string) {
	meter := otel.Meter("obs-demo/" + service)
	reqCounter, _ = meter.Int64Counter(
		"demo_http_server_requests_total",
		metric.WithDescription("Total HTTP requests handled"),
	)
	durHist, _ = meter.Float64Histogram(
		"demo_http_server_duration_seconds",
		metric.WithDescription("HTTP handler duration in seconds"),
	)
}

func record(ctx context.Context, service, route, code string, d time.Duration) {
	reqCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("service", service),
		attribute.String("route", route),
		attribute.String("code", code),
	))
	durHist.Record(ctx, d.Seconds(), metric.WithAttributes(
		attribute.String("service", service),
		attribute.String("route", route),
	))
}

func main() {
	service := "gateway"

	ctx := context.Background()
	closers, slogger, err := initOTel(ctx, service)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		_ = closers.ShutdownLog(context.Background())
		_ = closers.ShutdownMetric(context.Background())
		_ = closers.ShutdownTrace(context.Background())
	}()

	initMetrics(service)

	apiURL := mustEnv("API_URL") // e.g. http://api:8081
	tr := otel.Tracer("obs-demo/" + service)

	client := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   5 * time.Second,
	}

	mux := http.NewServeMux()

	mux.Handle("/gateway", otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := "/gateway"
		t0 := time.Now()

		ctx, span := tr.Start(r.Context(), "Gateway /gateway")
		defer span.End()

		slogger.InfoContext(ctx, "received request", "route", route, "remote", r.RemoteAddr)

		req, _ := http.NewRequestWithContext(ctx, "GET", apiURL+"/api", nil)
		res, err := client.Do(req)

		code := "200"
		if err != nil {
			code = "502"
			slogger.ErrorContext(ctx, "api call failed", "error", err)
			http.Error(w, "api call failed: "+err.Error(), http.StatusBadGateway)
			record(ctx, service, route, code, time.Since(t0))
			return
		}
		defer res.Body.Close()

		if res.StatusCode >= 500 {
			code = strconv.Itoa(res.StatusCode)
		}

		sc := trace.SpanFromContext(ctx).SpanContext()
		out := resp{
			TotalMs: time.Since(t0).Milliseconds(),
			Result:  fmt.Sprintf("api_status=%d", res.StatusCode),
			TraceID: sc.TraceID().String(),
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)

		record(ctx, service, route, code, time.Since(t0))
	}), "gateway"))

	mux.Handle("/burst", otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := "/burst"
		t0 := time.Now()

		n := 30
		if q := r.URL.Query().Get("n"); q != "" {
			if v, err := strconv.Atoi(q); err == nil && v > 0 && v <= 200 {
				n = v
			}
		}

		ctx, span := tr.Start(r.Context(), "Gateway /burst")
		defer span.End()

		ok := 0
		for i := 0; i < n; i++ {
			req, _ := http.NewRequestWithContext(ctx, "GET", apiURL+"/api", nil)
			res, err := client.Do(req)
			if err == nil && res != nil {
				_ = res.Body.Close()
				if res.StatusCode < 500 {
					ok++
				}
			}
		}

		sc := trace.SpanFromContext(ctx).SpanContext()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"requested":  n,
			"ok":         ok,
			"elapsed_ms": time.Since(t0).Milliseconds(),
			"trace_id":   sc.TraceID().String(),
		})

		record(ctx, service, route, "200", time.Since(t0))
	}), "burst"))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	addr := ":8080"
	slogger.Info("listening", "addr", addr, "api_url", apiURL)
	_ = slogger.With(slog.String("service", service))

	log.Fatal(http.ListenAndServe(addr, mux))
}
