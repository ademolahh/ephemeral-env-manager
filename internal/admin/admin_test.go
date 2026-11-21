package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadyzReportsEachCheck(t *testing.T) {
	tests := []struct {
		name   string
		checks map[string]Check
		status int
		want   map[string]string
	}{
		{
			name:   "all ok",
			checks: map[string]Check{"postgres": func(context.Context) error { return nil }},
			status: http.StatusOK,
			want:   map[string]string{"postgres": "ok"},
		},
		{
			name: "one failing",
			checks: map[string]Check{
				"postgres": func(context.Context) error { return nil },
				"docker":   func(context.Context) error { return errors.New("daemon down") },
			},
			status: http.StatusServiceUnavailable,
			want:   map[string]string{"postgres": "ok", "docker": "daemon down"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Handler(tt.checks).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s: %v", rec.Body, err)
			}
			if rec.Code != tt.status || len(got) != len(tt.want) {
				t.Fatalf("readyz = %d %v, want %d %v", rec.Code, got, tt.status, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("readyz[%s] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestHealthzAndMetrics(t *testing.T) {
	h := Handler(nil)
	for path, want := range map[string]string{"/healthz": "ok", "/metrics": "go_goroutines"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s = %d, want 200 containing %q", path, rec.Code, want)
		}
	}
}

func TestProbe(t *testing.T) {
	ready := true
	srv := httptest.NewServer(Handler(map[string]Check{"dep": func(context.Context) error {
		if !ready {
			return errors.New("down")
		}
		return nil
	}}))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")

	if err := Probe(addr); err != nil {
		t.Errorf("Probe() error = %v, want nil", err)
	}
	ready = false
	if err := Probe(addr); err == nil || !strings.Contains(err.Error(), "down") {
		t.Errorf("Probe() error = %v, want not ready with the failing check", err)
	}
}
