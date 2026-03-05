# Observability Demo (Go) — Traces + Metrics + Logs in Grafana

This repo spins up a tiny 2-service app plus MySQL:

**Client → Gateway → API → MySQL**

…and a full observability stack:

- **OpenTelemetry Collector** (gateway)
- **Jaeger** (traces backend)
- **Prometheus** (metrics backend)
- **Loki** (logs backend)
- **Grafana** (single UI for traces + metrics + logs)

## 1) Run

```bash
docker compose up --build
```

## 2) Generate traffic

```bash
curl -s localhost:8080/gateway | jq .
# or burst:
curl -s "localhost:8080/burst?n=30" | jq .
```

## 3) Open UIs

- Grafana: http://localhost:3000 (admin / admin)
- Jaeger: http://localhost:16686

## 4) What to show in a casual demo

1. `curl /gateway` shows **total latency**
2. Grafana dashboard shows **metrics + logs**
3. Grafana Explore:
   - **Jaeger** datasource → pick `Service = gateway` → open latest trace → see **API** and **MySQL** spans (including slow query if `QUERY_DELAY_MS` is set)
   - **Loki** datasource → query `{service=~"gateway|api"}` and filter by `trace_id` from the trace

## Notes

- **Gateway** forwards requests to **API**; **API** talks to **MySQL** with OpenTelemetry-instrumented `database/sql` (otelsql).
- Slow DB queries can be simulated by setting `QUERY_DELAY_MS` (e.g. 500) on the api service; it runs `SELECT SLEEP(...)` so the DB span appears slow in traces.
- Services emit telemetry via **OpenTelemetry SDK**:
  - Traces: OTLP → Collector → Jaeger
  - Metrics: OTLP → Collector → Prometheus exporter (scraped by Prometheus)
  - Logs: OTLP → Collector → Loki
