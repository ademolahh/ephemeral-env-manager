package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func (s server) exec(t *testing.T, key, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/envs/"+id+"/exec", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func decodeExec(t *testing.T, rec *httptest.ResponseRecorder) execResponse {
	t.Helper()
	var got execResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return got
}

func TestExecEnv(t *testing.T) {
	s := newServer(t)
	key := s.keyWithRole(t, db.RoleUser)
	created := s.createEnv(t, key)
	s.fake.execOut = "ok\n"

	rec := s.exec(t, key, created.ID.String(), `{"cmd": ["go", "test", "./..."]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := decodeExec(t, rec); got != (execResponse{Output: "ok\n"}) {
		t.Errorf("response = %+v, want ok with exit code 0", got)
	}
	if len(s.fake.execCmds) != 1 || !slices.Equal(s.fake.execCmds[0], []string{"go", "test", "./..."}) {
		t.Errorf("provider cmds = %v, want [go test ./...]", s.fake.execCmds)
	}
}

func TestExecEnvNonZeroExit(t *testing.T) {
	s := newServer(t)
	key := s.keyWithRole(t, db.RoleUser)
	created := s.createEnv(t, key)
	s.fake.execOut = "FAIL\n"
	s.fake.execErr = fmt.Errorf("exec env x: %w", &provider.ExitError{Code: 3})

	rec := s.exec(t, key, created.ID.String(), `{"cmd": ["false"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := decodeExec(t, rec); got != (execResponse{Output: "FAIL\n", ExitCode: 3}) {
		t.Errorf("response = %+v, want FAIL with exit code 3", got)
	}
}

func TestExecEnvAdmin(t *testing.T) {
	s := newServer(t)
	created := s.createEnv(t, s.keyWithRole(t, db.RoleUser))

	if rec := s.exec(t, s.adminToken(t), created.ID.String(), `{"cmd": ["true"]}`); rec.Code != http.StatusOK {
		t.Errorf("admin status = %d, want 200; body %s", rec.Code, rec.Body)
	}
}

func TestExecEnvErrors(t *testing.T) {
	s := newServer(t)
	key := s.keyWithRole(t, db.RoleUser)
	created := s.createEnv(t, key)
	deleted := s.createEnv(t, key)
	s.delete(t, key, deleted.ID.String())

	tests := []struct {
		name string
		key  string
		id   string
		body string
		err  error
		want int
	}{
		{name: "no api key", id: created.ID.String(), body: `{"cmd": ["true"]}`, want: http.StatusUnauthorized},
		{name: "invalid id", key: key, id: "nope", body: `{"cmd": ["true"]}`, want: http.StatusBadRequest},
		{name: "missing cmd", key: key, id: created.ID.String(), body: `{}`, want: http.StatusBadRequest},
		{name: "empty program", key: key, id: created.ID.String(), body: `{"cmd": [""]}`, want: http.StatusBadRequest},
		{name: "unknown field", key: key, id: created.ID.String(), body: `{"cmd": ["true"], "x": 1}`, want: http.StatusBadRequest},
		{name: "unknown environment", key: key, id: uuid.NewString(), body: `{"cmd": ["true"]}`, want: http.StatusNotFound},
		{name: "other caller", key: s.keyWithRole(t, db.RoleUser), id: created.ID.String(), body: `{"cmd": ["true"]}`, want: http.StatusNotFound},
		{name: "not running", key: key, id: deleted.ID.String(), body: `{"cmd": ["true"]}`, want: http.StatusConflict},
		{name: "timeout", key: key, id: created.ID.String(), body: `{"cmd": ["sleep", "600"]}`, err: fmt.Errorf("exec env x: %w", context.DeadlineExceeded), want: http.StatusGatewayTimeout},
		{name: "provider failure", key: key, id: created.ID.String(), body: `{"cmd": ["true"]}`, err: errors.New("daemon down"), want: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.fake.execErr = tt.err
			if rec := s.exec(t, tt.key, tt.id, tt.body); rec.Code != tt.want {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
}
