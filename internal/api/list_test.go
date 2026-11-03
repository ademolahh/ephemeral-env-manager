package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func TestListEnvsOnlyShowsCallerEnvironments(t *testing.T) {
	s := newServer(t)
	owner := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})
	created := s.createEnv(t, owner)

	for _, tc := range []struct {
		name       string
		key        string
		wantStatus int
		wantCount  int
	}{
		{"owner", owner, http.StatusOK, 1},
		{"other caller", s.keyWithRole(t, db.RoleUser), http.StatusOK, 0},
		{"missing key", "", http.StatusUnauthorized, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/envs", nil)
			if tc.key != "" {
				req.Header.Set("Authorization", "Bearer "+tc.key)
			}
			rec := httptest.NewRecorder()
			s.handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.wantStatus, rec.Body)
			}
			if rec.Code != http.StatusOK {
				return
			}
			var body []map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response fields: %v", err)
			}
			if len(body) > 0 {
				for _, field := range []string{"caller_id", "deleted_at", "instance_status"} {
					if _, ok := body[0][field]; ok {
						t.Errorf("internal field %q present in list response", field)
					}
				}
			}
			var got []db.Environment
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("count = %d, want %d", len(got), tc.wantCount)
			}
			if tc.wantCount == 1 && got[0].ID != created.ID {
				t.Errorf("environment = %s, want %s", got[0].ID, created.ID)
			}
		})
	}
}

func TestFrontendServedWithoutAPIKey(t *testing.T) {
	handler := Routes(NewHandler(nil, nil))
	for _, path := range []string{"/", "/app.js", "/styles.css"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}
