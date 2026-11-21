package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

const templateBody = `{
	"name": "clear-dashboard",
	"version": "v1",
	"providers": ["docker"],
	"default_provider": "docker",
	"ttl": "4h",
	"workload": {
		"ports": [8080],
		"resources": {"cpu_millis": 1000, "ram_mb": 4096, "disk_gb": 10}
	},
	"services": [
		{"name": "postgres", "image": "postgres:18", "resources": {"cpu_millis": 1000, "ram_mb": 4096, "disk_gb": 10}},
		{"name": "redis", "image": "redis:8", "resources": {"cpu_millis": 1000, "ram_mb": 1024, "disk_gb": 2}}
	]
}`

func (s server) send(t *testing.T, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	s.settle(t)
	return rec
}

func (s server) storeTemplate(t *testing.T) {
	t.Helper()
	if rec := s.send(t, http.MethodPost, "/templates", s.adminToken(t), templateBody); rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d, want 201; body %s", rec.Code, rec.Body)
	}
}

func TestCreateTemplate(t *testing.T) {
	s := newServer(t)
	admin := s.adminToken(t)

	rec := s.send(t, http.MethodPost, "/templates", admin, templateBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/templates/clear-dashboard/v1" {
		t.Errorf("Location = %q", loc)
	}

	rec = s.send(t, http.MethodGet, "/templates/clear-dashboard/v1", s.keyWithRole(t, db.RoleUser), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var got templateRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "clear-dashboard" || got.TTL != "4h" || len(got.Services) != 2 || got.Workload.Resources.RAMMB != 4096 {
		t.Errorf("template = %+v, want stored template", got)
	}
}

func TestCreateTemplateErrors(t *testing.T) {
	s := newServer(t)
	admin := s.adminToken(t)
	s.storeTemplate(t)

	tests := []struct {
		name string
		key  string
		body string
		want int
	}{
		{name: "not admin", key: s.keyWithRole(t, db.RoleUser), body: templateBody, want: http.StatusForbidden},
		{name: "duplicate version", key: admin, body: templateBody, want: http.StatusConflict},
		{name: "missing ttl", key: admin, body: `{"name": "a", "version": "v1", "workload": {"resources": {"cpu_millis": 1000, "ram_mb": 1024, "disk_gb": 1}}}`, want: http.StatusBadRequest},
		{name: "no resources", key: admin, body: `{"name": "a", "version": "v1", "ttl": "1h"}`, want: http.StatusBadRequest},
		{name: "unknown field", key: admin, body: `{"name": "a", "version": "v1", "ttl": "1h", "x": 1}`, want: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := s.send(t, http.MethodPost, "/templates", tt.key, tt.body); rec.Code != tt.want {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
}

func TestListTemplates(t *testing.T) {
	s := newServer(t)
	key := s.keyWithRole(t, db.RoleUser)

	rec := s.send(t, http.MethodGet, "/templates", key, "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"templates":[]}` {
		t.Fatalf("empty list = %d %s, want 200 with no templates", rec.Code, rec.Body)
	}

	s.storeTemplate(t)
	rec = s.send(t, http.MethodGet, "/templates", key, "")
	var got struct {
		Templates []templateRequest `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Templates) != 1 || got.Templates[0].Name != "clear-dashboard" {
		t.Errorf("templates = %+v, want clear-dashboard", got.Templates)
	}
}

func TestGetTemplateNotFound(t *testing.T) {
	s := newServer(t)
	if rec := s.send(t, http.MethodGet, "/templates/nope/v1", s.keyWithRole(t, db.RoleUser), ""); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestCreateEnvFromTemplate(t *testing.T) {
	s := newServer(t)
	s.storeTemplate(t)
	key := s.keyWithRole(t, db.RoleUser)

	rec := s.post(t, key, `{"type": "REGULAR_TESTING", "workload": "img:tag", "template": "clear-dashboard@v1"}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}

	e := decodeEnv(t, rec)
	if e.TemplateName != "clear-dashboard" || e.TemplateVersion != "v1" {
		t.Errorf("template = %s@%s, want clear-dashboard@v1", e.TemplateName, e.TemplateVersion)
	}
	if want := (provider.Resources{CPUMillis: 3000, RAMMB: 9216, DiskGB: 22}); e.Resources != want {
		t.Errorf("resources = %+v, want %+v", e.Resources, want)
	}
	if ttl := e.ExpiresAt.Sub(e.CreatedAt); ttl < 4*time.Hour-time.Minute || ttl > 4*time.Hour+time.Minute {
		t.Errorf("ttl = %s, want 4h", ttl)
	}
}

func TestCreateEnvOverrides(t *testing.T) {
	s := newServer(t)
	s.storeTemplate(t)
	key := s.keyWithRole(t, db.RoleUser)

	rec := s.post(t, key, `{
		"type": "REGULAR_TESTING",
		"workload": "img:tag",
		"template": "clear-dashboard@v1",
		"ttl": "30m",
		"resources": {"workload": {"ram_mb": 8192}, "services": {"postgres": {"cpu_millis": 2000, "disk_gb": 20}}}
	}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}

	e := decodeEnv(t, rec)
	if want := (provider.Resources{CPUMillis: 4000, RAMMB: 13312, DiskGB: 32}); e.Resources != want {
		t.Errorf("resources = %+v, want %+v", e.Resources, want)
	}
	if ttl := e.ExpiresAt.Sub(e.CreatedAt); ttl < 29*time.Minute || ttl > 31*time.Minute {
		t.Errorf("ttl = %s, want 30m", ttl)
	}

	tpl := s.fake.calls[len(s.fake.calls)-1]
	if tpl.Workload.Resources != (provider.Resources{CPUMillis: 1000, RAMMB: 8192, DiskGB: 10}) {
		t.Errorf("workload resources = %+v, want ram overridden", tpl.Workload.Resources)
	}
	if tpl.Services[0].Resources != (provider.Resources{CPUMillis: 2000, RAMMB: 4096, DiskGB: 20}) || tpl.Services[1].Resources != (provider.Resources{CPUMillis: 1000, RAMMB: 1024, DiskGB: 2}) {
		t.Errorf("services = %+v, want only postgres overridden", tpl.Services)
	}

	rec = s.send(t, http.MethodGet, "/templates/clear-dashboard/v1", key, "")
	var stored templateRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil || stored.Workload.Resources.RAMMB != 4096 || stored.TTL != "4h" {
		t.Errorf("stored template = %+v, %v, want unchanged", stored, err)
	}
}

func TestCreateEnvOverridesInlineTemplate(t *testing.T) {
	s := newServer(t)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUMillis: 10000, RAMMB: 20480, DiskGB: 50})

	body := withBody(t, func(m map[string]any) {
		m["resources"] = map[string]any{"workload": map[string]any{"cpu_millis": 2000}}
	})
	rec := s.post(t, key, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	if e := decodeEnv(t, rec); e.Resources.CPUMillis != 4000 {
		t.Errorf("cpu = %d, want 4000", e.Resources.CPUMillis)
	}
}

func TestCreateEnvTemplateErrors(t *testing.T) {
	s := newServer(t)
	s.storeTemplate(t)
	key := s.keyWithRole(t, db.RoleUser)

	tests := []struct {
		name string
		body string
	}{
		{name: "missing template", body: `{"type": "REGULAR_TESTING", "workload": "img"}`},
		{name: "no version", body: `{"type": "REGULAR_TESTING", "workload": "img", "template": "clear-dashboard"}`},
		{name: "unknown template", body: `{"type": "REGULAR_TESTING", "workload": "img", "template": "nope@v1"}`},
		{name: "unknown service", body: `{"type": "REGULAR_TESTING", "workload": "img", "template": "clear-dashboard@v1", "resources": {"services": {"mysql": {"cpu_millis": 1000}}}}`},
		{name: "zero override", body: `{"type": "REGULAR_TESTING", "workload": "img", "template": "clear-dashboard@v1", "resources": {"workload": {"ram_mb": 0}}}`},
		{name: "bad ttl", body: `{"type": "REGULAR_TESTING", "workload": "img", "template": "clear-dashboard@v1", "ttl": "soon"}`},
		{name: "unknown override field", body: `{"type": "REGULAR_TESTING", "workload": "img", "template": "clear-dashboard@v1", "resources": {"workload": {"gpu": 1}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := s.post(t, key, tt.body, nil); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
		})
	}
	if n := s.countEnvironments(t); n != 0 {
		t.Errorf("environments = %d, want 0", n)
	}
}

func TestInlineTemplatesCanBeDisabled(t *testing.T) {
	s := newServer(t)
	h := NewHandler(s.tx, s.m, s.sessions, nil)
	h.DisableInlineTemplates = true
	s.handler = Routes(h)
	key := s.apiKey(t, db.EnvRegularTesting, provider.Resources{CPUMillis: 10000, RAMMB: 20480, DiskGB: 50})

	rec := s.post(t, key, withBody(t, func(map[string]any) {}), nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "inline templates are disabled") {
		t.Errorf("inline create = %d %s, want 400 inline templates are disabled", rec.Code, rec.Body)
	}
}

func TestInlineTemplateVersionTracksSpec(t *testing.T) {
	base := templateRequest{TTL: "1h"}
	base.Name, base.Version = "app", "v1"
	changed := base
	changed.TTL = "2h"

	name, v1 := inlineIdentity(base)
	_, v2 := inlineIdentity(changed)
	if name != "inline:app" || v1 == v2 || !strings.HasPrefix(v1, "sha256:") {
		t.Errorf("inlineIdentity = %s %s and %s, want inline:app with versions that differ by spec", name, v1, v2)
	}
}
