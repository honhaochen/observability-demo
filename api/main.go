package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/uptrace/opentelemetry-go-extra/otelsql"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

var (
	reqCounter metric.Int64Counter
	durHist    metric.Float64Histogram
)

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func initMetrics(service string) {
	meter := otel.Meter("obs-demo/" + service)
	reqCounter, _ = meter.Int64Counter("demo_http_server_requests_total")
	durHist, _ = meter.Float64Histogram(
		"demo_http_server_duration_seconds",
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 50, 100, 120),
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

func openDB(ctx context.Context, dsn string) (*sql.DB, error) {
	// Instrumented MySQL driver: traces and metrics for each query.
	db, err := otelsql.Open("mysql", dsn,
		otelsql.WithAttributes(semconv.DBSystemMySQL),
	)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func main() {
	service := "api"

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

	dsn := mustEnv("MYSQL_DSN")
	workDelayMs := envInt("WORK_DELAY_MS", 550)
	queryDelayMs := envInt("QUERY_DELAY_MS", 0)

	db, err := openDB(ctx, dsn)
	if err != nil {
		log.Fatal("mysql open: ", err)
	}
	defer db.Close()

	// Ensure demo table exists
	_, _ = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS demo_work (
		id INT AUTO_INCREMENT PRIMARY KEY,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)

	mux := http.NewServeMux()
	mux.Handle("/api", otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := "/api"
		t0 := time.Now()

		slogger.InfoContext(r.Context(), "work started", "work_delay_ms", workDelayMs, "query_delay_ms", queryDelayMs)
		time.Sleep(time.Duration(workDelayMs) * time.Millisecond)

		reqCtx := r.Context()

		var count int
		err := db.QueryRowContext(reqCtx, "SELECT COUNT(*) FROM demo_work").Scan(&count)
		code := "200"
		if err != nil {
			code = "502"
			slogger.ErrorContext(r.Context(), "db query failed", "error", err)
			http.Error(w, "db query failed: "+err.Error(), http.StatusBadGateway)
			record(r.Context(), service, route, code, time.Since(t0))
			return
		}

		// Simulate slow DB query: MySQL SLEEP so the DB span shows up as slow in traces
		if queryDelayMs > 0 {
			sec := queryDelayMs / 1000
			if sec < 1 {
				sec = 1
			}
			_, _ = db.ExecContext(reqCtx, "SELECT SLEEP(?)", sec)
		}

		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
		record(r.Context(), service, route, code, time.Since(t0))
	}), "api"))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	addr := ":8081"
	slogger.Info("listening", "addr", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
