// Package metrics holds the Prometheus metrics the services export.
package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
)

var (
	EnvironmentsCreated = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "eem_environments_created_total",
		Help: "Environments accepted for provisioning, by type.",
	}, []string{"type"})

	Preemptions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "eem_preemptions_total",
		Help: "Lower-priority environments deleted to make room, by the preempted environment's type.",
	}, []string{"type"})

	ProvisionFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "eem_provision_failures_total",
		Help: "Environments that failed to provision or become ready.",
	})

	TimeToReady = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "eem_environment_ready_seconds",
		Help:    "Time from creation until every container is running and healthy.",
		Buckets: []float64{1, 2, 5, 10, 20, 30, 60, 120, 300},
	})

	WorkerActions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "eem_worker_actions_total",
		Help: "Reconciliation actions the worker took, by action.",
	}, []string{"action"})

	WorkerTaskErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "eem_worker_task_errors_total",
		Help: "Worker task runs that returned an error, by task.",
	}, []string{"task"})

	ProxyRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "eem_proxy_requests_total",
		Help: "Requests the environment proxy handled, by status code.",
	}, []string{"code"})

	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "eem_http_requests_total",
		Help: "API requests, by route, method and status code.",
	}, []string{"route", "method", "code"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "eem_http_request_duration_seconds",
		Help:    "API request latency, by route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})
)

// InstrumentHTTP counts and times requests to the handler registered for
// route, and logs each one at debug level.
func InstrumentHTTP(route string, next http.Handler) http.Handler {
	counter := httpRequests.MustCurryWith(prometheus.Labels{"route": route})
	duration := httpDuration.MustCurryWith(prometheus.Labels{"route": route})
	h := promhttp.InstrumentHandlerCounter(counter, promhttp.InstrumentHandlerDuration(duration, next))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		slog.Debug("request", "route", route, "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

// StateCollector reports environment counts and capacity pool usage read
// from Postgres at scrape time, so every replica reports the same numbers.
type StateCollector struct {
	db           db.DBTX
	environments *prometheus.Desc
	capacity     *prometheus.Desc
	capacityUsed *prometheus.Desc
}

func NewStateCollector(q db.DBTX) *StateCollector {
	return &StateCollector{
		db: q,
		environments: prometheus.NewDesc("eem_environments",
			"Environments by type and status.", []string{"type", "status"}, nil),
		capacity: prometheus.NewDesc("eem_capacity_total",
			"Capacity pool size by provider and resource (cpu_millis, ram_mb, disk_gb).", []string{"provider", "resource"}, nil),
		capacityUsed: prometheus.NewDesc("eem_capacity_used",
			"Capacity reserved by environments, by provider and resource.", []string{"provider", "resource"}, nil),
	}
}

func (c *StateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.environments
	ch <- c.capacity
	ch <- c.capacityUsed
}

func (c *StateCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := c.db.Query(ctx, `SELECT type::text, status::text, count(*) FROM environments GROUP BY 1, 2`)
	if err != nil {
		slog.Error("collect environment metrics", "err", err)
		ch <- prometheus.NewInvalidMetric(c.environments, err)
		return
	}
	for rows.Next() {
		var typ, status string
		var n float64
		if err := rows.Scan(&typ, &status, &n); err != nil {
			rows.Close()
			ch <- prometheus.NewInvalidMetric(c.environments, err)
			return
		}
		ch <- prometheus.MustNewConstMetric(c.environments, prometheus.GaugeValue, n, typ, status)
	}
	rows.Close()

	pools, err := db.ListCapacityPools(ctx, c.db)
	if err != nil {
		slog.Error("collect capacity metrics", "err", err)
		ch <- prometheus.NewInvalidMetric(c.capacity, err)
		return
	}
	for _, p := range pools {
		total, avail := p.Default, p.Available
		for _, r := range []struct {
			name         string
			total, avail int
		}{
			{"cpu_millis", total.CPUMillis, avail.CPUMillis},
			{"ram_mb", total.RAMMB, avail.RAMMB},
			{"disk_gb", total.DiskGB, avail.DiskGB},
		} {
			ch <- prometheus.MustNewConstMetric(c.capacity, prometheus.GaugeValue, float64(r.total), string(p.Provider), r.name)
			ch <- prometheus.MustNewConstMetric(c.capacityUsed, prometheus.GaugeValue, float64(r.total-r.avail), string(p.Provider), r.name)
		}
	}
}
