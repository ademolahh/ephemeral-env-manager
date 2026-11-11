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

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
)

func (s server) adminToken(t *testing.T) string {
	t.Helper()
	token, _, err := s.sessions.CreateAdmin(context.Background(), uuid.New(), time.Hour)
	if err != nil {
		t.Fatalf("CreateAdmin() error = %v", err)
	}
	return token
}

func (s server) createAdmin(t *testing.T, username, password string) uuid.UUID {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	id, err := db.CreateAdmin(context.Background(), s.tx, username, hash)
	if err != nil {
		t.Fatalf("CreateAdmin() error = %v", err)
	}
	return id
}

func (s server) login(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(body)))
	return rec
}

func (s server) request(t *testing.T, method, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminLogin(t *testing.T) {
	s := newServer(t)
	id := s.createAdmin(t, "root", "correct horse battery")
	before := time.Now()

	rec := s.login(t, `{"username": "root", "password": "correct horse battery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var got adminLoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	want := before.Add(adminSessionTTL)
	if got.ExpiresAt.Before(want.Add(-time.Minute)) || got.ExpiresAt.After(want.Add(time.Minute)) {
		t.Errorf("expires_at = %v, want about %v", got.ExpiresAt, want)
	}

	sess, err := s.sessions.Get(context.Background(), got.Token)
	if err != nil {
		t.Fatalf("sessions.Get() error = %v", err)
	}
	if sess.AdminID == nil || *sess.AdminID != id || sess.CallerID != nil {
		t.Errorf("session = %+v, want admin %s", sess, id)
	}

	rec = s.put(t, got.Token, "/capacity/docker", `{"total": {"cpu_core": 200, "ram_gb": 200, "disk_gb": 2000}}`)
	if rec.Code != http.StatusNoContent {
		t.Errorf("admin action with login token = %d, want 204; body %s", rec.Code, rec.Body)
	}
}

func TestAdminLoginRejected(t *testing.T) {
	s := newServer(t)
	s.createAdmin(t, "root", "correct horse battery")

	tests := []struct {
		name string
		body string
		code int
		want string
	}{
		{name: "wrong password", body: `{"username": "root", "password": "wrong horse battery"}`, code: http.StatusUnauthorized, want: "invalid username or password"},
		{name: "unknown username", body: `{"username": "nobody", "password": "correct horse battery"}`, code: http.StatusUnauthorized, want: "invalid username or password"},
		{name: "missing password", body: `{"username": "root"}`, code: http.StatusBadRequest, want: "required"},
		{name: "missing username", body: `{"password": "correct horse battery"}`, code: http.StatusBadRequest, want: "required"},
		{name: "malformed json", body: `{`, code: http.StatusBadRequest, want: "invalid request body"},
		{name: "unknown field", body: `{"username": "root", "password": "correct horse battery", "role": "admin"}`, code: http.StatusBadRequest, want: "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := s.login(t, tt.body)
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("got %d %s, want %d containing %q", rec.Code, rec.Body, tt.code, tt.want)
			}
			if strings.Contains(rec.Body.String(), "token") {
				t.Errorf("rejected login returned a token: %s", rec.Body)
			}
		})
	}
}

func TestAdminTokenRejected(t *testing.T) {
	s := newServer(t)
	body := `{"total": {"cpu_core": 1, "ram_gb": 1, "disk_gb": 1}}`
	loggedOut := s.adminToken(t)
	if rec := s.request(t, http.MethodPost, "/auth/logout", loggedOut); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204; body %s", rec.Code, rec.Body)
	}

	for name, token := range map[string]string{"logged out": loggedOut, "unknown": auth.RandomString(), "garbage": "a.b.c"} {
		if rec := s.put(t, token, "/capacity/docker", body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s token: got %d %s, want 401", name, rec.Code, rec.Body)
		}
	}
}

func TestAdminSessionExpires(t *testing.T) {
	s := newServer(t)
	token, _, err := s.sessions.CreateAdmin(context.Background(), uuid.New(), time.Second)
	if err != nil {
		t.Fatalf("CreateAdmin() error = %v", err)
	}
	if rec := s.request(t, http.MethodGet, "/capacity", token); rec.Code != http.StatusOK {
		t.Fatalf("fresh session = %d, want 200", rec.Code)
	}
	time.Sleep(1100 * time.Millisecond)
	if rec := s.request(t, http.MethodGet, "/capacity", token); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired session = %d, want 401", rec.Code)
	}
}

func TestAdminTokenAccess(t *testing.T) {
	s := newServer(t)
	admin := s.adminToken(t)

	if rec := s.post(t, admin, validBody, nil); rec.Code != http.StatusForbidden {
		t.Errorf("POST /envs as admin = %d, want 403; body %s", rec.Code, rec.Body)
	}
	if got := s.countEnvironments(t); got != 0 {
		t.Errorf("environments = %d, want 0", got)
	}
	if rec := s.request(t, http.MethodGet, "/envs", admin); rec.Code != http.StatusForbidden {
		t.Errorf("GET /envs as admin = %d, want 403", rec.Code)
	}
	if rec := s.getBudget(t, admin, "me"); rec.Code != http.StatusBadRequest {
		t.Errorf("GET /callers/me/budget as admin = %d, want 400", rec.Code)
	}
	if rec := s.request(t, http.MethodGet, "/capacity", admin); rec.Code != http.StatusOK {
		t.Errorf("GET /capacity as admin = %d, want 200", rec.Code)
	}
}

func TestCallerKeyIsNotAdmin(t *testing.T) {
	s := newServer(t)
	for _, role := range []db.Role{db.RoleUser, db.RoleAgent} {
		key := s.keyWithRole(t, role)
		for _, path := range []string{"/capacity/docker", "/prices/docker"} {
			if rec := s.put(t, key, path, `{}`); rec.Code != http.StatusForbidden {
				t.Errorf("%s PUT %s = %d, want 403", role, path, rec.Code)
			}
		}
	}
}
