package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

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
			var body struct {
				Environments []map[string]any `json:"environments"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response fields: %v", err)
			}
			if len(body.Environments) > 0 {
				for _, field := range []string{"caller_id", "deleted_at", "instance_status"} {
					if _, ok := body.Environments[0][field]; ok {
						t.Errorf("internal field %q present in list response", field)
					}
				}
			}
			got := decodeList(t, rec)
			if len(got.Environments) != tc.wantCount || got.Total != tc.wantCount {
				t.Fatalf("environments, total = %d, %d, want %d", len(got.Environments), got.Total, tc.wantCount)
			}
			if tc.wantCount == 1 && got.Environments[0].ID != created.ID {
				t.Errorf("environment = %s, want %s", got.Environments[0].ID, created.ID)
			}
		})
	}
}

func TestFrontendServedWithoutAPIKey(t *testing.T) {
	handler := Routes(NewHandler(nil, nil, nil, nil))
	for _, path := range []string{"/", "/app.js", "/styles.css"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) listEnvsResponse {
	t.Helper()
	var got listEnvsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list %s: %v", rec.Body, err)
	}
	return got
}

func (s server) list(t *testing.T, key, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/envs"+query, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func TestListEnvsPaginationAndFilter(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 30, DiskGB: 100})
	var ids []uuid.UUID
	for range 3 {
		ids = append(ids, s.createEnv(t, key).ID)
	}
	s.delete(t, key, ids[0].String())

	first := s.list(t, key, "?limit=2")
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", first.Code, first.Body)
	}
	page := decodeList(t, first)
	if len(page.Environments) != 2 || page.Page != 1 || page.NextPage != 2 {
		t.Fatalf("first page = %d envs, page %d, next %d, want 2 envs on page 1 with next 2", len(page.Environments), page.Page, page.NextPage)
	}
	if page.Total != 3 || page.Counts[db.EnvRunning] != 2 || page.Counts[db.EnvDeleted] != 1 || page.Counts[db.EnvFailed] != 0 {
		t.Errorf("total, counts = %d, %v, want 3 with 2 running and 1 deleted", page.Total, page.Counts)
	}

	second := decodeList(t, s.list(t, key, "?limit=2&page=2"))
	if len(second.Environments) != 1 || second.NextPage != 0 {
		t.Fatalf("second page = %d envs, next %d, want 1 and no next page", len(second.Environments), second.NextPage)
	}
	var seen []uuid.UUID
	for _, e := range append(page.Environments, second.Environments...) {
		seen = append(seen, e.ID)
	}
	for _, id := range ids {
		if !slices.Contains(seen, id) {
			t.Errorf("environment %s missing from pages %v", id, seen)
		}
	}

	if past := decodeList(t, s.list(t, key, "?limit=2&page=9")); len(past.Environments) != 0 || past.NextPage != 0 {
		t.Errorf("page past the end = %d envs, next %d, want none", len(past.Environments), past.NextPage)
	}

	if def := decodeList(t, s.list(t, key, "")); def.Limit != 10 || def.Page != 1 {
		t.Errorf("defaults = page %d, limit %d, want page 1, limit 10", def.Page, def.Limit)
	}

	deleted := decodeList(t, s.list(t, key, "?status=deleted"))
	if len(deleted.Environments) != 1 || deleted.Environments[0].ID != ids[0] || deleted.Total != 3 {
		t.Errorf("deleted filter = %+v, want only %s with total still 3", deleted.Environments, ids[0])
	}

	running := decodeList(t, s.list(t, key, "?status=RUNNING"))
	if len(running.Environments) != 2 {
		t.Errorf("running filter = %d envs, want 2", len(running.Environments))
	}
	for _, e := range running.Environments {
		if e.Status != db.EnvRunning {
			t.Errorf("running filter returned %s %s", e.ID, e.Status)
		}
	}

	empty := s.list(t, key, "?status=FAILED")
	if !strings.Contains(empty.Body.String(), `"environments":[]`) {
		t.Errorf("empty filter body = %s, want an empty environments array", empty.Body)
	}
}

func TestListEnvsBadParams(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUCore: 10, RAMGB: 20, DiskGB: 50})

	for query, want := range map[string]string{
		"?status=STOPPED": "status",
		"?limit=0":        "limit",
		"?limit=101":      "limit",
		"?limit=ten":      "limit",
		"?page=0":         "page",
		"?page=-1":        "page",
		"?page=two":       "page",
	} {
		rec := s.list(t, key, query)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET /envs%s = %d %s, want 400 mentioning %q", query, rec.Code, rec.Body, want)
		}
	}
}
