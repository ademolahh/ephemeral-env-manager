package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
)

func TestSetPermission(t *testing.T) {
	s := newServer(t)
	agentID, agent := s.callerWithRole(t, db.RoleAgent)
	admin := s.adminToken(t)

	rec := s.put(t, admin, "/callers/"+agentID.String()+"/permission", `{"permitted_env": "PR_REVIEW", "prod_bug_cost_cap": 0}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	if rec := s.post(t, agent, withBody(t, func(m map[string]any) { m["type"] = "PROD_BUG" }), nil); rec.Code != http.StatusForbidden {
		t.Errorf("PROD_BUG without permission = %d, want 403; body %s", rec.Code, rec.Body)
	}

	if rec := s.put(t, admin, "/callers/"+agentID.String()+"/permission", `{"permitted_env": "PROD_BUG", "prod_bug_cost_cap": 0}`); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	if rec := s.post(t, agent, withBody(t, func(m map[string]any) { m["type"] = "PROD_BUG" }), nil); rec.Code != http.StatusForbidden {
		t.Errorf("PROD_BUG over cap = %d, want 403; body %s", rec.Code, rec.Body)
	}

	if rec := s.put(t, admin, "/callers/"+agentID.String()+"/permission", `{"permitted_env": "PROD_BUG", "prod_bug_cost_cap": 5000}`); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	if rec := s.post(t, agent, withBody(t, func(m map[string]any) { m["type"] = "PROD_BUG" }), nil); rec.Code != http.StatusAccepted {
		t.Errorf("PROD_BUG within cap = %d, want 202; body %s", rec.Code, rec.Body)
	}

	var budget budgetResponse
	if err := json.Unmarshal(s.getBudget(t, agent, "me").Body.Bytes(), &budget); err != nil {
		t.Fatalf("decode budget: %v", err)
	}
	if budget.ProdBugCostCap != 5000 {
		t.Errorf("prod bug cost cap = %d, want 5000", budget.ProdBugCostCap)
	}
}

func TestAgentCannotCreateRegularTesting(t *testing.T) {
	s := newServer(t)
	agent := s.keyWithRole(t, db.RoleAgent)

	if rec := s.post(t, agent, validBody, nil); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body %s", rec.Code, rec.Body)
	}
}

func TestSetPermissionRejected(t *testing.T) {
	s := newServer(t)
	callerID, key := s.callerWithRole(t, db.RoleAgent)
	admin := s.adminToken(t)
	path := "/callers/" + callerID.String() + "/permission"

	tests := []struct {
		name string
		key  string
		path string
		body string
		want int
	}{
		{name: "not admin", key: key, path: path, body: `{"permitted_env": "PROD_BUG", "prod_bug_cost_cap": 100}`, want: http.StatusForbidden},
		{name: "bad type", key: admin, path: path, body: `{"permitted_env": "STAGING", "prod_bug_cost_cap": 100}`, want: http.StatusBadRequest},
		{name: "missing cap", key: admin, path: path, body: `{"permitted_env": "PROD_BUG"}`, want: http.StatusBadRequest},
		{name: "negative cap", key: admin, path: path, body: `{"permitted_env": "PROD_BUG", "prod_bug_cost_cap": -1}`, want: http.StatusBadRequest},
		{name: "invalid id", key: admin, path: "/callers/nope/permission", body: `{"permitted_env": "PROD_BUG", "prod_bug_cost_cap": 100}`, want: http.StatusBadRequest},
		{name: "unknown caller", key: admin, path: "/callers/00000000-0000-0000-0000-000000000001/permission", body: `{"permitted_env": "PROD_BUG", "prod_bug_cost_cap": 100}`, want: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := s.put(t, tt.key, tt.path, tt.body); rec.Code != tt.want {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
}
