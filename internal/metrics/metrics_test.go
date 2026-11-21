package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func TestInstrumentHTTPCountsByRouteAndCode(t *testing.T) {
	route := "GET /test/{id}"
	h := InstrumentHTTP(route, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	before := testutil.ToFloat64(httpRequests.WithLabelValues(route, "get", "418"))
	for range 3 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/test/abc", nil))
	}

	if got := testutil.ToFloat64(httpRequests.WithLabelValues(route, "get", "418")) - before; got != 3 {
		t.Errorf("requests counted = %v, want 3", got)
	}
}

func TestStateCollectorReadsEnvironmentsAndCapacity(t *testing.T) {
	tx := dbtest.Tx(t)
	ctx := context.Background()

	if err := db.SetCapacityPool(ctx, tx, provider.Docker, provider.Resources{CPUMillis: 8000, RAMMB: 16384, DiskGB: 100}); err != nil {
		t.Fatalf("SetCapacityPool() error = %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE capacity_pools SET available_cpu_millis = 6000 WHERE provider = 'docker';
		INSERT INTO callers (id, quota_cpu_millis, quota_ram_mb, quota_disk_gb, cost_budget_total, budget_expires_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 1000, 1024, 1, 100, now() + interval '1 day');
		INSERT INTO environments (id, expires_at, type, provider, cpu_millis, ram_mb, disk_gb, hourly_rate,
			caller_id, template_name, template_version, estimated_cost, reserved_cost, status)
		VALUES
			(gen_random_uuid(), now(), 'PR_REVIEW', 'docker', 1000, 1024, 1, 1,
				'00000000-0000-0000-0000-000000000001', 't', 'v1', 0, 0, 'RUNNING'),
			(gen_random_uuid(), now(), 'PR_REVIEW', 'docker', 1000, 1024, 1, 1,
				'00000000-0000-0000-0000-000000000001', 't', 'v1', 0, 0, 'RUNNING')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	want := `
# HELP eem_capacity_used Capacity reserved by environments, by provider and resource.
# TYPE eem_capacity_used gauge
eem_capacity_used{provider="docker",resource="cpu_millis"} 2000
eem_capacity_used{provider="docker",resource="disk_gb"} 0
eem_capacity_used{provider="docker",resource="ram_mb"} 0
# HELP eem_environments Environments by type and status.
# TYPE eem_environments gauge
eem_environments{status="RUNNING",type="PR_REVIEW"} 2
`
	if err := testutil.CollectAndCompare(NewStateCollector(tx), strings.NewReader(want), "eem_environments", "eem_capacity_used"); err != nil {
		t.Error(err)
	}
}
