package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/dbtest"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var (
	envID      = uuid.MustParse("2b1f3c4d-5e6f-4a1b-8c9d-0e1f2a3b4c5d")
	testSecret = "0123456789abcdef0123456789abcdef"
)

func TestFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    *Proxy
		wantErr error
		anyErr  bool
	}{
		{
			name: "disabled",
			env:  map[string]string{},
			want: nil,
		},
		{
			name: "domain and container",
			env:  map[string]string{"PROXY_DOMAIN": "env.localhost", "PROXY_CONTAINER": "eem-proxy", "PROXY_SECRET": testSecret},
			want: &Proxy{Domain: "env.localhost", Container: "eem-proxy", Secret: []byte(testSecret)},
		},
		{
			name: "public port",
			env:  map[string]string{"PROXY_DOMAIN": "env.localhost", "PROXY_CONTAINER": "eem-proxy", "PROXY_PUBLIC_PORT": "8000", "PROXY_SECRET": testSecret},
			want: &Proxy{Domain: "env.localhost", Container: "eem-proxy", PublicPort: 8000, Secret: []byte(testSecret)},
		},
		{
			name:    "missing container",
			env:     map[string]string{"PROXY_DOMAIN": "env.localhost"},
			wantErr: ErrNoContainer,
		},
		{
			name:    "missing secret",
			env:     map[string]string{"PROXY_DOMAIN": "env.localhost", "PROXY_CONTAINER": "eem-proxy"},
			wantErr: ErrWeakSecret,
		},
		{
			name:    "short secret",
			env:     map[string]string{"PROXY_DOMAIN": "env.localhost", "PROXY_CONTAINER": "eem-proxy", "PROXY_SECRET": "short"},
			wantErr: ErrWeakSecret,
		},
		{
			name:   "port not a number",
			env:    map[string]string{"PROXY_DOMAIN": "env.localhost", "PROXY_CONTAINER": "eem-proxy", "PROXY_PUBLIC_PORT": "web", "PROXY_SECRET": testSecret},
			anyErr: true,
		},
		{
			name:   "port out of range",
			env:    map[string]string{"PROXY_DOMAIN": "env.localhost", "PROXY_CONTAINER": "eem-proxy", "PROXY_PUBLIC_PORT": "70000", "PROXY_SECRET": testSecret},
			anyErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"PROXY_DOMAIN", "PROXY_CONTAINER", "PROXY_PUBLIC_PORT", "PROXY_SECRET"} {
				t.Setenv(k, tt.env[k])
			}

			got, err := FromEnv()
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("FromEnv() error = %v, want %v", err, tt.wantErr)
				}
				return
			case tt.anyErr:
				if err == nil {
					t.Fatalf("FromEnv() = %+v, want error", got)
				}
				return
			case err != nil:
				t.Fatalf("FromEnv() error = %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FromEnv() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestHostAndURL(t *testing.T) {
	tests := []struct {
		name string
		port int
		want string
	}{
		{name: "no port", port: 0, want: "http://env-" + envID.String() + ".env.localhost"},
		{name: "port 80", port: 80, want: "http://env-" + envID.String() + ".env.localhost"},
		{name: "custom port", port: 8000, want: "http://env-" + envID.String() + ".env.localhost:8000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Proxy{Domain: "env.localhost", PublicPort: tt.port}
			if got, want := p.Host(envID), "env-"+envID.String()+".env.localhost"; got != want {
				t.Errorf("Host() = %q, want %q", got, want)
			}
			if got := p.URL(envID); got != tt.want {
				t.Errorf("URL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnvID(t *testing.T) {
	p := &Proxy{Domain: "env.localhost"}
	host := "env-" + envID.String() + ".env.localhost"

	tests := []struct {
		name string
		host string
		ok   bool
	}{
		{name: "host", host: host, ok: true},
		{name: "host with port", host: host + ":8000", ok: true},
		{name: "uppercase", host: strings.ToUpper(host), ok: true},
		{name: "other domain", host: "env-" + envID.String() + ".example.com"},
		{name: "missing prefix", host: envID.String() + ".env.localhost"},
		{name: "not a uuid", host: "env-abc.env.localhost"},
		{name: "uuid without dashes", host: "env-" + strings.ReplaceAll(envID.String(), "-", "") + ".env.localhost"},
		{name: "subdomain", host: "x.env-" + envID.String() + ".env.localhost"},
		{name: "domain itself", host: "env.localhost"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := p.EnvID(tt.host)
			if ok != tt.ok {
				t.Fatalf("EnvID(%q) ok = %v, want %v", tt.host, ok, tt.ok)
			}
			if ok && id != envID {
				t.Errorf("EnvID(%q) = %s, want %s", tt.host, id, envID)
			}
		})
	}
}

func createEnvironment(t *testing.T, tx pgx.Tx, backend string, status db.EnvStatus) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	callerID, err := db.CreateCaller(ctx, tx, db.NewCaller{
		PermittedEnv:    db.EnvPRReview,
		Quota:           provider.Resources{CPUMillis: 8000, RAMMB: 32768, DiskGB: 100},
		CostBudgetTotal: 10000,
		BudgetExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}

	id, err := db.CreateEnvironment(ctx, tx, db.NewEnvironment{
		CallerID:        callerID,
		Type:            db.EnvPRReview,
		Provider:        provider.Docker,
		Resources:       provider.Resources{CPUMillis: 1000, RAMMB: 1024, DiskGB: 1},
		HourlyRate:      0,
		TTL:             time.Hour,
		TemplateName:    "t",
		TemplateVersion: "v1",
	})
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}

	if status == db.EnvRunning {
		if err := db.MarkEnvironmentRunning(ctx, tx, id, backend); err != nil {
			t.Fatalf("MarkEnvironmentRunning() error = %v", err)
		}
	} else if _, err := tx.Exec(ctx, `UPDATE environments SET backend = $2 WHERE id = $1`, id, backend); err != nil {
		t.Fatalf("set backend: %v", err)
	}

	return id
}

func testProxy() *Proxy {
	return &Proxy{Domain: "env.localhost", Secret: []byte(testSecret)}
}

// serve sends a GET with a valid access token for the environment in host.
func serve(h http.Handler, host, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = host
	if id, ok := testProxy().EnvID(host); ok {
		req.Header.Set(TokenHeader, testProxy().AccessToken(id))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandlerForwardsToBackend(t *testing.T) {
	tx := dbtest.Tx(t)

	var got *http.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("X-Workload", "1")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "from workload")
	}))
	t.Cleanup(backend.Close)

	id := createEnvironment(t, tx, strings.TrimPrefix(backend.URL, "http://"), db.EnvRunning)
	p := testProxy()
	host := p.Host(id) + ":8000"

	rec := serve(p.Handler(tx), host, "/ok?x=1")

	if rec.Code != http.StatusTeapot || rec.Body.String() != "from workload" || rec.Header().Get("X-Workload") != "1" {
		t.Fatalf("response = %d %q %v, want 418 from workload", rec.Code, rec.Body, rec.Header())
	}
	if got.URL.Path != "/ok" || got.URL.RawQuery != "x=1" {
		t.Errorf("backend url = %s, want /ok?x=1", got.URL)
	}
	if got.Host != host {
		t.Errorf("backend Host = %q, want %q", got.Host, host)
	}
	if fh := got.Header.Get("X-Forwarded-Host"); fh != host {
		t.Errorf("X-Forwarded-Host = %q, want %q", fh, host)
	}
}

func TestHandlerNotFound(t *testing.T) {
	tx := dbtest.Tx(t)
	p := testProxy()
	provisioning := createEnvironment(t, tx, "127.0.0.1:1", db.EnvProvisioning)

	for name, host := range map[string]string{
		"unknown host":        "example.com",
		"unknown environment": p.Host(uuid.New()),
		"not running":         p.Host(provisioning),
	} {
		t.Run(name, func(t *testing.T) {
			if rec := serve(p.Handler(tx), host, "/"); rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestHandlerBackendDown(t *testing.T) {
	tx := dbtest.Tx(t)
	backend := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(backend.URL, "http://")
	backend.Close()

	p := testProxy()
	id := createEnvironment(t, tx, addr, db.EnvRunning)

	if rec := serve(p.Handler(tx), p.Host(id), "/"); rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestHandlerDatabaseError(t *testing.T) {
	tx := dbtest.Tx(t)
	if _, err := tx.Exec(context.Background(), "DROP TABLE environments CASCADE"); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	p := testProxy()
	if rec := serve(p.Handler(tx), p.Host(envID), "/"); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestAccessToken(t *testing.T) {
	p := testProxy()
	other := &Proxy{Domain: "env.localhost", Secret: []byte("another-secret-another-secret-00")}

	if again := testProxy(); p.AccessToken(envID) != again.AccessToken(envID) {
		t.Error("AccessToken() differs between proxies with the same secret")
	}
	if p.AccessToken(envID) == p.AccessToken(uuid.New()) {
		t.Error("AccessToken() is the same for two environments")
	}
	if p.AccessToken(envID) == other.AccessToken(envID) {
		t.Error("AccessToken() does not depend on the secret")
	}
	if want := "http://" + p.Host(envID) + "/?eem_token=" + p.AccessToken(envID); p.AccessURL(envID) != want {
		t.Errorf("AccessURL() = %q, want %q", p.AccessURL(envID), want)
	}
}

func TestHandlerRequiresAccessToken(t *testing.T) {
	tx := dbtest.Tx(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "from workload")
	}))
	t.Cleanup(backend.Close)

	p := testProxy()
	id := createEnvironment(t, tx, strings.TrimPrefix(backend.URL, "http://"), db.EnvRunning)
	h := p.Handler(tx)

	for name, token := range map[string]string{
		"missing":           "",
		"wrong":             "not-a-token",
		"other environment": p.AccessToken(uuid.New()),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = p.Host(id)
			if token != "" {
				req.Header.Set(TokenHeader, token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "from workload") {
				t.Errorf("status = %d body %q, want 401 without reaching the workload", rec.Code, rec.Body)
			}
		})
	}
}

func TestHandlerAccessURLSetsCookieAndRedirects(t *testing.T) {
	tx := dbtest.Tx(t)
	var got *http.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
	}))
	t.Cleanup(backend.Close)

	p := testProxy()
	id := createEnvironment(t, tx, strings.TrimPrefix(backend.URL, "http://"), db.EnvRunning)
	h := p.Handler(tx)
	token := p.AccessToken(id)

	req := httptest.NewRequest(http.MethodGet, "/page?x=1&"+TokenParam+"="+token, nil)
	req.Host = p.Host(id)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/page?x=1" {
		t.Fatalf("response = %d Location %q, want 303 to /page?x=1", rec.Code, rec.Header().Get("Location"))
	}
	if got != nil {
		t.Error("redirect reached the workload")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != TokenCookie || cookies[0].Value != token || !cookies[0].HttpOnly {
		t.Fatalf("cookies = %+v, want an HttpOnly %s cookie with the token", cookies, TokenCookie)
	}

	req = httptest.NewRequest(http.MethodGet, "/page?x=1", nil)
	req.Host = p.Host(id)
	req.Header.Set("Cookie", "app_session=abc; "+TokenCookie+"="+token+`; pref="dark mode"`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || got == nil {
		t.Fatalf("status = %d, want 200 from the workload with the cookie", rec.Code)
	}
	if c := got.Header.Get("Cookie"); c != `app_session=abc; pref="dark mode"` {
		t.Errorf("workload Cookie = %q, want the app's cookies only", c)
	}
}

func TestHandlerStripsTokenBeforeWorkload(t *testing.T) {
	tx := dbtest.Tx(t)
	var got *http.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
	}))
	t.Cleanup(backend.Close)

	p := testProxy()
	id := createEnvironment(t, tx, strings.TrimPrefix(backend.URL, "http://"), db.EnvRunning)
	token := p.AccessToken(id)

	req := httptest.NewRequest(http.MethodPost, "/hook?"+TokenParam+"="+token+"&a=b", strings.NewReader("{}"))
	req.Host = p.Host(id)
	req.Header.Set(TokenHeader, token)
	rec := httptest.NewRecorder()
	p.Handler(tx).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || got == nil {
		t.Fatalf("status = %d, want 200 from the workload", rec.Code)
	}
	if got.Header.Get(TokenHeader) != "" || got.URL.Query().Has(TokenParam) || got.URL.Query().Get("a") != "b" {
		t.Errorf("workload saw header %q and query %q, want the token removed and a=b kept", got.Header.Get(TokenHeader), got.URL.RawQuery)
	}
}
