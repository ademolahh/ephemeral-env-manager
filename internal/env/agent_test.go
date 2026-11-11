package env

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type agent struct {
	callerID uuid.UUID
	keyID    uuid.UUID
}

func (a agent) authenticated() db.AuthenticatedCaller {
	return db.AuthenticatedCaller{ID: a.callerID, Role: db.RoleAgent, KeyID: a.keyID}
}

func (f fixture) key(t *testing.T, callerID uuid.UUID, mutate func(*db.NewAPIKey)) uuid.UUID {
	t.Helper()
	_, hash := auth.GenerateAPIKey()
	k := db.NewAPIKey{
		CallerID:  callerID,
		Name:      "agent",
		Hash:      hash,
		Role:      db.RoleAgent,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if mutate != nil {
		mutate(&k)
	}
	created, err := db.CreateAPIKey(context.Background(), f.q, k)
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	return created.ID
}

func (f fixture) agent(t *testing.T, permitted db.EnvType, prodBugCap int64) agent {
	t.Helper()
	id := f.caller(t, nil)
	if err := db.SetCallerPermission(context.Background(), f.q, id, permitted, prodBugCap); err != nil {
		t.Fatalf("SetCallerPermission() error = %v", err)
	}
	return agent{callerID: id, keyID: f.key(t, id, nil)}
}

func (f fixture) createAs(a agent, typ db.EnvType, r provider.Resources) (uuid.UUID, error) {
	return f.createWith(a.callerID, a.keyID, typ, r, "")
}

func (f fixture) mustCreateAs(t *testing.T, a agent, typ db.EnvType, r provider.Resources) uuid.UUID {
	t.Helper()
	id, err := f.createAs(a, typ, r)
	if err != nil {
		t.Fatalf("Create(%s, %+v) as agent error = %v", typ, r, err)
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
	a := f.agent(t, db.EnvProdBug, 10000)

	if _, err := f.createAs(a, db.EnvRegularTesting, cpu(1)); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("REGULAR_TESTING error = %v, want ErrNotPermitted", err)
	}
	if _, err := f.createAs(a, db.EnvPRReview, cpu(1)); err != nil {
		t.Errorf("PR_REVIEW error = %v, want allowed", err)
	}
	if _, err := f.createAs(a, db.EnvProdBug, cpu(1)); err != nil {
		t.Errorf("PROD_BUG error = %v, want allowed", err)
	}
}

func TestCreateAgentProdBugNeedsPermission(t *testing.T) {
	f := newFixture(t, cpu(100))
	a := f.agent(t, db.EnvPRReview, 10000)

	if _, err := f.createAs(a, db.EnvProdBug, cpu(1)); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("Create() error = %v, want ErrNotPermitted", err)
	}
}

func TestCreateAgentProdBugCostCap(t *testing.T) {
	f := newFixture(t, cpu(100))
	probe := f.agent(t, db.EnvProdBug, 10000)
	f.mustCreateAs(t, probe, db.EnvProdBug, cpu(1))
	cost := f.prodBugCost(t, probe.callerID)

	a := f.agent(t, db.EnvProdBug, cost)
	f.mustCreateAs(t, a, db.EnvProdBug, cpu(1))

	if _, err := f.createAs(a, db.EnvProdBug, cpu(1)); !errors.Is(err, ErrProdBugCapExceeded) {
		t.Errorf("Create() over cap error = %v, want ErrProdBugCapExceeded", err)
	}
	if got := f.prodBugCost(t, a.callerID); got != cost {
		t.Errorf("prod bug cost = %d, want %d", got, cost)
	}
	if _, err := f.createAs(a, db.EnvPRReview, cpu(1)); err != nil {
		t.Errorf("PR_REVIEW after cap error = %v, want allowed", err)
	}
}

func TestCreateAgentProdBugZeroCap(t *testing.T) {
	f := newFixture(t, cpu(100))
	a := f.agent(t, db.EnvProdBug, 0)

	if _, err := f.createAs(a, db.EnvProdBug, cpu(1)); !errors.Is(err, ErrProdBugCapExceeded) {
		t.Errorf("Create() error = %v, want ErrProdBugCapExceeded", err)
	}
}

func TestCreateUserProdBugHasNoCap(t *testing.T) {
	f := newFixture(t, cpu(100))
	f.mustCreate(t, f.caller(t, nil), db.EnvProdBug, cpu(1))
}

func TestCreateAgentChargesOwner(t *testing.T) {
	f := newFixture(t, cpu(100))
	a := f.agent(t, db.EnvProdBug, 0)

	id := f.mustCreateAs(t, a, db.EnvPRReview, cpu(1))
	e, err := db.GetEnvironment(context.Background(), f.q, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if e.CallerID != a.callerID || e.APIKeyID == nil || *e.APIKeyID != a.keyID {
		t.Errorf("caller, key = %s, %v, want %s, %s", e.CallerID, e.APIKeyID, a.callerID, a.keyID)
	}
	if got := f.used(t, a.callerID); got == 0 {
		t.Error("owner cost used = 0, want the agent's environment charged to the owner")
	}
}

func TestCreateKeyPermissionBelowOwner(t *testing.T) {
	f := newFixture(t, cpu(100))
	owner := f.caller(t, nil)
	review := db.EnvPRReview
	a := agent{callerID: owner, keyID: f.key(t, owner, func(k *db.NewAPIKey) { k.PermittedEnv = &review })}

	if _, err := f.createAs(a, db.EnvProdBug, cpu(1)); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("PROD_BUG with PR_REVIEW key error = %v, want ErrNotPermitted", err)
	}
	f.mustCreateAs(t, a, db.EnvPRReview, cpu(1))
}

func TestCreateKeyCannotRaiseOwnerPermission(t *testing.T) {
	f := newFixture(t, cpu(100))
	owner := f.caller(t, func(c *db.NewCaller) { c.PermittedEnv = db.EnvPRReview })
	prodBug := db.EnvProdBug
	a := agent{callerID: owner, keyID: f.key(t, owner, func(k *db.NewAPIKey) { k.PermittedEnv = &prodBug })}

	if _, err := f.createAs(a, db.EnvProdBug, cpu(1)); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("PROD_BUG error = %v, want ErrNotPermitted", err)
	}
}

func TestCreateKeyCostCap(t *testing.T) {
	f := newFixture(t, cpu(100))
	owner := f.caller(t, nil)
	probe := agent{callerID: owner, keyID: f.key(t, owner, nil)}
	f.mustCreateAs(t, probe, db.EnvPRReview, cpu(1))
	cost := f.used(t, owner)

	capped := agent{callerID: owner, keyID: f.key(t, owner, func(k *db.NewAPIKey) { k.CostCap = &cost })}
	f.mustCreateAs(t, capped, db.EnvPRReview, cpu(1))

	if _, err := f.createAs(capped, db.EnvPRReview, cpu(1)); !errors.Is(err, ErrKeyCostCapExceeded) {
		t.Errorf("Create() over key cap error = %v, want ErrKeyCostCapExceeded", err)
	}
	f.mustCreateAs(t, probe, db.EnvPRReview, cpu(1))
}

func TestCreateRevokedKey(t *testing.T) {
	f := newFixture(t, cpu(100))
	a := f.agent(t, db.EnvProdBug, 0)
	if err := db.RevokeAPIKey(context.Background(), f.q, a.keyID, a.callerID); err != nil {
		t.Fatalf("RevokeAPIKey() error = %v", err)
	}

	if _, err := f.createAs(a, db.EnvPRReview, cpu(1)); !errors.Is(err, db.ErrAPIKeyInactive) {
		t.Errorf("Create() error = %v, want ErrAPIKeyInactive", err)
	}
}

func TestCreateOtherCallersKey(t *testing.T) {
	f := newFixture(t, cpu(100))
	a := f.agent(t, db.EnvProdBug, 0)
	other := f.caller(t, nil)

	if _, err := f.createWith(other, a.keyID, db.EnvPRReview, cpu(1), ""); !errors.Is(err, db.ErrAPIKeyNotFound) {
		t.Errorf("Create() error = %v, want ErrAPIKeyNotFound", err)
	}
}

func TestAgentKeySeesOnlyItsEnvironments(t *testing.T) {
	f := newFixture(t, cpu(100))
	a := f.agent(t, db.EnvProdBug, 0)
	sibling := agent{callerID: a.callerID, keyID: f.key(t, a.callerID, nil)}
	ctx := context.Background()

	own := f.mustCreateAs(t, a, db.EnvPRReview, cpu(1))
	theirs := f.mustCreateAs(t, sibling, db.EnvPRReview, cpu(1))
	human := f.mustCreate(t, a.callerID, db.EnvRegularTesting, cpu(1))

	if _, _, err := f.m.Get(ctx, a.authenticated(), own); err != nil {
		t.Errorf("Get(own) error = %v", err)
	}
	for name, id := range map[string]uuid.UUID{"sibling key": theirs, "owner": human} {
		if _, _, err := f.m.Get(ctx, a.authenticated(), id); !errors.Is(err, db.ErrEnvNotFound) {
			t.Errorf("Get(%s) error = %v, want ErrEnvNotFound", name, err)
		}
		if _, err := f.m.Delete(ctx, a.authenticated(), id); !errors.Is(err, db.ErrEnvNotFound) {
			t.Errorf("Delete(%s) error = %v, want ErrEnvNotFound", name, err)
		}
	}

	ownerSession := db.AuthenticatedCaller{ID: a.callerID, Role: db.RoleUser, Session: true}
	for _, id := range []uuid.UUID{own, theirs, human} {
		if _, _, err := f.m.Get(ctx, ownerSession, id); err != nil {
			t.Errorf("owner Get(%s) error = %v", id, err)
		}
	}
}

func TestAgentIdempotencyKeyScopedToKey(t *testing.T) {
	f := newFixture(t, cpu(100))
	a := f.agent(t, db.EnvProdBug, 0)
	sibling := agent{callerID: a.callerID, keyID: f.key(t, a.callerID, nil)}

	first, err := f.createWith(a.callerID, a.keyID, db.EnvPRReview, cpu(1), "pr-1")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	replay, err := f.createWith(a.callerID, a.keyID, db.EnvPRReview, cpu(1), "pr-1")
	if err != nil || replay != first {
		t.Errorf("replay = %s, %v, want %s", replay, err, first)
	}
	if _, err := f.createWith(sibling.callerID, sibling.keyID, db.EnvPRReview, cpu(1), "pr-1"); !errors.Is(err, ErrIdempotencyKeyInUse) {
		t.Errorf("sibling replay error = %v, want ErrIdempotencyKeyInUse", err)
	}
}
