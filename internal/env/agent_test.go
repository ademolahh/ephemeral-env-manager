package env

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
)

func (f fixture) agent(t *testing.T, permitted db.EnvType, prodBugCap int64) uuid.UUID {
	t.Helper()
	id := f.caller(t, func(c *db.NewCaller) { c.Role = db.RoleAgent })
	if err := db.SetCallerPermission(context.Background(), f.q, id, permitted, prodBugCap); err != nil {
		t.Fatalf("SetCallerPermission() error = %v", err)
	}
	return id
}

func (f fixture) prodBugCost(t *testing.T, callerID uuid.UUID) int64 {
	t.Helper()
	b, err := db.GetCallerBudget(context.Background(), f.q, callerID)
	if err != nil {
		t.Fatalf("GetCallerBudget() error = %v", err)
	}
	return b.ProdBugCost
}

func TestCreateAgentTypes(t *testing.T) {
	f := newFixture(t, cpu(100))
	agentID := f.agent(t, db.EnvProdBug, 10000)

	if _, err := f.create(agentID, db.EnvRegularTesting, cpu(1)); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("REGULAR_TESTING error = %v, want ErrNotPermitted", err)
	}
	if _, err := f.create(agentID, db.EnvPRReview, cpu(1)); err != nil {
		t.Errorf("PR_REVIEW error = %v, want allowed", err)
	}
	if _, err := f.create(agentID, db.EnvProdBug, cpu(1)); err != nil {
		t.Errorf("PROD_BUG error = %v, want allowed", err)
	}
}

func TestCreateAgentProdBugNeedsPermission(t *testing.T) {
	f := newFixture(t, cpu(100))
	agentID := f.agent(t, db.EnvPRReview, 10000)

	if _, err := f.create(agentID, db.EnvProdBug, cpu(1)); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("Create() error = %v, want ErrNotPermitted", err)
	}
}

func TestCreateAgentProdBugCostCap(t *testing.T) {
	f := newFixture(t, cpu(100))
	probe := f.agent(t, db.EnvProdBug, 10000)
	f.mustCreate(t, probe, db.EnvProdBug, cpu(1))
	cost := f.prodBugCost(t, probe)

	agentID := f.agent(t, db.EnvProdBug, cost)
	f.mustCreate(t, agentID, db.EnvProdBug, cpu(1))

	if _, err := f.create(agentID, db.EnvProdBug, cpu(1)); !errors.Is(err, ErrProdBugCapExceeded) {
		t.Errorf("Create() over cap error = %v, want ErrProdBugCapExceeded", err)
	}
	if got := f.prodBugCost(t, agentID); got != cost {
		t.Errorf("prod bug cost = %d, want %d", got, cost)
	}
	if _, err := f.create(agentID, db.EnvPRReview, cpu(1)); err != nil {
		t.Errorf("PR_REVIEW after cap error = %v, want allowed", err)
	}
}

func TestCreateAgentProdBugZeroCap(t *testing.T) {
	f := newFixture(t, cpu(100))
	agentID := f.agent(t, db.EnvProdBug, 0)

	if _, err := f.create(agentID, db.EnvProdBug, cpu(1)); !errors.Is(err, ErrProdBugCapExceeded) {
		t.Errorf("Create() error = %v, want ErrProdBugCapExceeded", err)
	}
}

func TestCreateUserProdBugHasNoCap(t *testing.T) {
	f := newFixture(t, cpu(100))
	f.mustCreate(t, f.caller(t, nil), db.EnvProdBug, cpu(1))
}
