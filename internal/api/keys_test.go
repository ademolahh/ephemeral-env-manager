package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func (s server) callerSession(t *testing.T, callerID uuid.UUID) string {
	t.Helper()
	token, _, err := s.sessions.CreateCaller(context.Background(), callerID, time.Hour)
	if err != nil {
		t.Fatalf("CreateCaller() session error = %v", err)
	}
	return token
}

func (s server) signedIn(t *testing.T, permitted db.EnvType) (uuid.UUID, string) {
	t.Helper()
	callerID, err := db.CreateCaller(context.Background(), s.tx, db.NewCaller{
		PermittedEnv:    permitted,
		Quota:           provider.Resources{CPUCore: 100, RAMGB: 100, DiskGB: 500},
		CostBudgetTotal: 10000,
		BudgetExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	return callerID, s.callerSession(t, callerID)
}

func decodeKey(t *testing.T, rec *httptest.ResponseRecorder) createKeyResponse {
	t.Helper()
	var got createKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode key: %v; body %s", err, rec.Body)
	}
	return got
}

func decodeKeys(t *testing.T, rec *httptest.ResponseRecorder) []db.APIKey {
	t.Helper()
	var got listKeysResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode keys: %v; body %s", err, rec.Body)
	}
	return got.Keys
}

func TestCreateKey(t *testing.T) {
	s := newServer(t)
	callerID, session := s.signedIn(t, db.EnvProdBug)
	before := time.Now()

	rec := s.send(t, http.MethodPost, "/keys", session, `{"name": "ci agent", "role": "agent", "permitted_env": "PR_REVIEW", "cost_cap": 500, "expires_in": "48h"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body)
	}
	got := decodeKey(t, rec)
	if got.Key == "" || got.CallerID != callerID || got.Name != "ci agent" || got.Role != db.RoleAgent {
		t.Errorf("created key = %+v", got)
	}
	if got.PermittedEnv == nil || *got.PermittedEnv != db.EnvPRReview || got.CostCap == nil || *got.CostCap != 500 {
		t.Errorf("permitted_env, cost_cap = %v, %v, want PR_REVIEW, 500", got.PermittedEnv, got.CostCap)
	}
	if want := before.Add(48 * time.Hour); got.ExpiresAt.Before(want.Add(-time.Minute)) || got.ExpiresAt.After(want.Add(time.Minute)) {
		t.Errorf("expires_at = %v, want about %v", got.ExpiresAt, want)
	}
	if want := "/keys/" + got.ID.String(); rec.Header().Get("Location") != want {
		t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), want)
	}

	me := s.request(t, http.MethodGet, "/me", got.Key)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"role":"agent"`) || !strings.Contains(me.Body.String(), `"permitted_env":"PR_REVIEW"`) {
		t.Errorf("GET /me with new key = %d %s, want agent limited to PR_REVIEW", me.Code, me.Body)
	}

	list := s.request(t, http.MethodGet, "/keys", session)
	keys := decodeKeys(t, list)
	if len(keys) != 1 || keys[0].ID != got.ID || strings.Contains(list.Body.String(), got.Key) {
		t.Errorf("GET /keys = %s, want one key without its secret", list.Body)
	}
}

func TestCreateKeyDefaults(t *testing.T) {
	s := newServer(t)
	_, session := s.signedIn(t, db.EnvRegularTesting)
	before := time.Now()

	got := decodeKey(t, s.send(t, http.MethodPost, "/keys", session, `{"name": "laptop"}`))
	if got.Role != db.RoleUser || got.PermittedEnv != nil || got.CostCap != nil {
		t.Errorf("key = %+v, want user key without limits", got)
	}
	if want := before.Add(defaultKeyTTL); got.ExpiresAt.Before(want.Add(-time.Minute)) || got.ExpiresAt.After(want.Add(time.Minute)) {
		t.Errorf("expires_at = %v, want about %v", got.ExpiresAt, want)
	}
}

func TestCreateKeyRejected(t *testing.T) {
	s := newServer(t)
	_, session := s.signedIn(t, db.EnvPRReview)

	tests := []struct {
		name string
		body string
		code int
		want string
	}{
		{"missing name", `{}`, http.StatusBadRequest, "name is required"},
		{"blank name", `{"name": "   "}`, http.StatusBadRequest, "name is required"},
		{"long name", `{"name": "` + strings.Repeat("n", 65) + `"}`, http.StatusBadRequest, "name is required"},
		{"bad role", `{"name": "k", "role": "admin"}`, http.StatusBadRequest, "role must be"},
		{"bad expiry", `{"name": "k", "expires_in": "soon"}`, http.StatusBadRequest, "expires_in"},
		{"long expiry", `{"name": "k", "expires_in": "9000h"}`, http.StatusBadRequest, "expires_in"},
		{"negative cap", `{"name": "k", "cost_cap": -1}`, http.StatusBadRequest, "cost_cap"},
		{"bad env", `{"name": "k", "permitted_env": "STAGING"}`, http.StatusBadRequest, "permitted_env"},
		{"above owner", `{"name": "k", "permitted_env": "PROD_BUG"}`, http.StatusForbidden, "not permitted"},
		{"unknown field", `{"name": "k", "caller_id": "x"}`, http.StatusBadRequest, "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := s.send(t, http.MethodPost, "/keys", session, tt.body)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("got %d %s, want %d containing %q", rec.Code, rec.Body, tt.code, tt.want)
			}
		})
	}
}

func TestKeysNeedSession(t *testing.T) {
	s := newServer(t)
	for name, tc := range map[string]struct {
		key  string
		want int
	}{
		"no credentials": {"", http.StatusUnauthorized},
		"user api key":   {s.keyWithRole(t, db.RoleUser), http.StatusForbidden},
		"agent api key":  {s.keyWithRole(t, db.RoleAgent), http.StatusForbidden},
		"admin":          {s.adminToken(t), http.StatusForbidden},
	} {
		if rec := s.send(t, http.MethodPost, "/keys", tc.key, `{"name": "k"}`); rec.Code != tc.want {
			t.Errorf("%s: POST /keys = %d, want %d", name, rec.Code, tc.want)
		}
		if rec := s.request(t, http.MethodGet, "/keys", tc.key); rec.Code != tc.want {
			t.Errorf("%s: GET /keys = %d, want %d", name, rec.Code, tc.want)
		}
	}
}

func TestRevokeKey(t *testing.T) {
	s := newServer(t)
	_, session := s.signedIn(t, db.EnvRegularTesting)
	created := decodeKey(t, s.send(t, http.MethodPost, "/keys", session, `{"name": "k"}`))
	if rec := s.request(t, http.MethodGet, "/me", created.Key); rec.Code != http.StatusOK {
		t.Fatalf("new key = %d, want 200", rec.Code)
	}

	if rec := s.request(t, http.MethodDelete, "/keys/"+created.ID.String(), session); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /keys = %d, want 204; body %s", rec.Code, rec.Body)
	}
	if rec := s.request(t, http.MethodGet, "/me", created.Key); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked key = %d, want 401", rec.Code)
	}
	keys := decodeKeys(t, s.request(t, http.MethodGet, "/keys", session))
	if len(keys) != 1 || keys[0].RevokedAt == nil {
		t.Errorf("keys = %+v, want the key listed as revoked", keys)
	}
}

func TestRevokeKeyAccess(t *testing.T) {
	s := newServer(t)
	_, owner := s.signedIn(t, db.EnvRegularTesting)
	_, other := s.signedIn(t, db.EnvRegularTesting)
	created := decodeKey(t, s.send(t, http.MethodPost, "/keys", owner, `{"name": "k"}`))
	path := "/keys/" + created.ID.String()

	if rec := s.request(t, http.MethodDelete, path, other); rec.Code != http.StatusNotFound {
		t.Errorf("other caller revoke = %d, want 404", rec.Code)
	}
	if rec := s.request(t, http.MethodGet, "/me", created.Key); rec.Code != http.StatusOK {
		t.Errorf("key after other caller's revoke = %d, want still valid", rec.Code)
	}
	if rec := s.request(t, http.MethodDelete, path, created.Key); rec.Code != http.StatusNoContent {
		t.Errorf("key revoking itself = %d, want 204", rec.Code)
	}

	second := decodeKey(t, s.send(t, http.MethodPost, "/keys", owner, `{"name": "k2"}`))
	if rec := s.request(t, http.MethodDelete, "/keys/"+second.ID.String(), s.adminToken(t)); rec.Code != http.StatusNoContent {
		t.Errorf("admin revoke = %d, want 204", rec.Code)
	}
	if rec := s.request(t, http.MethodDelete, "/keys/"+uuid.NewString(), owner); rec.Code != http.StatusNotFound {
		t.Errorf("unknown key = %d, want 404", rec.Code)
	}
	if rec := s.request(t, http.MethodDelete, "/keys/nope", owner); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid id = %d, want 400", rec.Code)
	}
}

func TestAdminCallerKeys(t *testing.T) {
	s := newServer(t)
	admin := s.adminToken(t)
	callerID, _ := s.signedIn(t, db.EnvPRReview)
	path := "/callers/" + callerID.String() + "/keys"

	rec := s.send(t, http.MethodPost, path, admin, `{"name": "bootstrap", "role": "agent"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin POST %s = %d, want 201; body %s", path, rec.Code, rec.Body)
	}
	created := decodeKey(t, rec)
	if created.CallerID != callerID || created.Role != db.RoleAgent {
		t.Errorf("created = %+v, want agent key for %s", created, callerID)
	}

	keys := decodeKeys(t, s.request(t, http.MethodGet, path, admin))
	if len(keys) != 1 || keys[0].ID != created.ID {
		t.Errorf("admin GET %s = %+v", path, keys)
	}

	if rec := s.send(t, http.MethodPost, path, admin, `{"name": "k", "permitted_env": "PROD_BUG"}`); rec.Code != http.StatusForbidden {
		t.Errorf("admin key above caller permission = %d, want 403", rec.Code)
	}
	unknown := "/callers/" + uuid.NewString() + "/keys"
	if rec := s.send(t, http.MethodPost, unknown, admin, `{"name": "k"}`); rec.Code != http.StatusNotFound {
		t.Errorf("POST unknown caller = %d, want 404", rec.Code)
	}
	if rec := s.request(t, http.MethodGet, unknown, admin); rec.Code != http.StatusNotFound {
		t.Errorf("GET unknown caller = %d, want 404", rec.Code)
	}
	if rec := s.send(t, http.MethodPost, path, s.keyWithRole(t, db.RoleUser), `{"name": "k"}`); rec.Code != http.StatusForbidden {
		t.Errorf("caller POST %s = %d, want 403", path, rec.Code)
	}
}

func TestAgentKeyListsOnlyItsEnvironments(t *testing.T) {
	s := newServer(t)
	callerID, session := s.signedIn(t, db.EnvProdBug)
	agent := s.keyFor(t, callerID, db.NewAPIKey{Role: db.RoleAgent})
	sibling := s.keyFor(t, callerID, db.NewAPIKey{Role: db.RoleAgent})

	review := withBody(t, func(m map[string]any) { m["type"] = "PR_REVIEW" })
	mine := decodeEnv(t, s.post(t, agent, review, nil))
	theirs := decodeEnv(t, s.post(t, sibling, review, nil))
	human := s.createEnv(t, session)

	list := s.request(t, http.MethodGet, "/envs", agent)
	var got listEnvsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(got.Environments) != 1 || got.Environments[0].ID != mine.ID || got.Total != 1 {
		t.Errorf("agent list = %s, want only %s", list.Body, mine.ID)
	}
	if got.Environments[0].APIKeyID == nil {
		t.Error("api_key_id missing from agent's environment")
	}

	for _, id := range []uuid.UUID{theirs.ID, human.ID} {
		if rec := s.request(t, http.MethodGet, "/envs/"+id.String(), agent); rec.Code != http.StatusNotFound {
			t.Errorf("agent GET %s = %d, want 404", id, rec.Code)
		}
	}

	var all listEnvsResponse
	if err := json.Unmarshal(s.request(t, http.MethodGet, "/envs", session).Body.Bytes(), &all); err != nil {
		t.Fatalf("decode owner list: %v", err)
	}
	if all.Total != 3 {
		t.Errorf("owner list total = %d, want 3", all.Total)
	}
}

func TestKeyCostCapOverHTTP(t *testing.T) {
	s := newServer(t)
	callerID, _ := s.signedIn(t, db.EnvRegularTesting)
	key := s.keyFor(t, callerID, db.NewAPIKey{CostCap: new(int64(0))})

	if rec := s.post(t, key, validBody, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "api key cost cap") {
		t.Errorf("create over key cap = %d %s, want 403 api key cost cap", rec.Code, rec.Body)
	}
}
