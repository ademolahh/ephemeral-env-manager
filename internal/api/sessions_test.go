package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
)

type fakeGitHub struct {
	*httptest.Server
	id        int64
	login     string
	challenge string
}

func newFakeGitHub(t *testing.T, id int64, login string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{id: id, login: login}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("code") != "good-code" {
			http.Error(w, `{"error": "bad_verification_code"}`, http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
			http.Error(w, `{"error": "invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token": "gho_test", "token_type": "bearer"}`))
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": f.id, "login": f.login})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (s server) withGitHub(t *testing.T, f *fakeGitHub, allowed ...string) server {
	t.Helper()
	gh, err := auth.NewGitHub(auth.GitHubConfig{
		ClientID:     "client",
		ClientSecret: "secret",
		RedirectURL:  "https://eem.test/auth/github/callback",
		AllowedUsers: allowed,
		AuthURL:      f.URL + "/authorize",
		TokenURL:     f.URL + "/token",
		APIURL:       f.URL,
	})
	if err != nil {
		t.Fatalf("NewGitHub() error = %v", err)
	}
	s.handler = Routes(NewHandler(s.tx, s.m, s.sessions, gh))
	return s
}

func (s server) startLogin(t *testing.T, f *fakeGitHub) (state string, cookie *http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/github/login", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login status = %d, want 302; body %s", rec.Code, rec.Body)
	}

	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), f.URL+"/authorize") {
		t.Fatalf("Location = %q, want fake authorize url", rec.Header().Get("Location"))
	}
	q := loc.Query()
	if q.Get("client_id") != "client" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "" {
		t.Errorf("authorize query = %v, want client, S256 PKCE and no scopes", q)
	}
	f.challenge = q.Get("code_challenge")

	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != oauthCookiePath {
		t.Fatalf("oauth cookie = %+v, want HttpOnly, Secure, Lax on %s", cookie, oauthCookiePath)
	}
	return q.Get("state"), cookie
}

func (s server) callback(t *testing.T, query string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?"+query, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func sessionCookieFrom(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestGitHubLogin(t *testing.T) {
	f := newFakeGitHub(t, 42, "octo")
	s := newServer(t).withGitHub(t, f)

	state, oauth := s.startLogin(t, f)
	rec := s.callback(t, url.Values{"code": {"good-code"}, "state": {state}}.Encode(), oauth)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback = %d to %q, want 302 to /; body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}

	cookie := sessionCookieFrom(rec)
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("session cookie = %+v, want HttpOnly, Secure, Strict on /", cookie)
	}

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.AddCookie(cookie)
	me := httptest.NewRecorder()
	s.handler.ServeHTTP(me, req)
	var got meResponse
	if err := json.Unmarshal(me.Body.Bytes(), &got); err != nil || me.Code != http.StatusOK {
		t.Fatalf("GET /me = %d %s", me.Code, me.Body)
	}
	if got.Kind != "caller" || got.GithubLogin == nil || *got.GithubLogin != "octo" || !got.Session || got.PermittedEnv != db.EnvRegularTesting {
		t.Errorf("me = %+v, want octo caller session with REGULAR_TESTING", got)
	}

	f.login = "octo-renamed"
	state, oauth = s.startLogin(t, f)
	again := s.callback(t, url.Values{"code": {"good-code"}, "state": {state}}.Encode(), oauth)
	if again.Code != http.StatusFound || sessionCookieFrom(again) == nil {
		t.Fatalf("second login = %d, want new session", again.Code)
	}
	var callers int
	if err := s.tx.QueryRow(context.Background(), `SELECT count(*) FROM callers WHERE github_id = '42'`).Scan(&callers); err != nil {
		t.Fatalf("count callers: %v", err)
	}
	if callers != 1 {
		t.Errorf("callers for github id 42 = %d, want 1", callers)
	}
}

func TestGitHubCallbackRejected(t *testing.T) {
	f := newFakeGitHub(t, 42, "octo")
	s := newServer(t).withGitHub(t, f)

	tests := []struct {
		name   string
		query  func(state string) url.Values
		cookie bool
		reason string
	}{
		{"no state cookie", func(state string) url.Values { return url.Values{"code": {"good-code"}, "state": {state}} }, false, "expired"},
		{"state mismatch", func(string) url.Values { return url.Values{"code": {"good-code"}, "state": {"forged"}} }, true, "state"},
		{"user denied", func(state string) url.Values { return url.Values{"error": {"access_denied"}, "state": {state}} }, true, "denied"},
		{"bad code", func(state string) url.Values { return url.Values{"code": {"bad-code"}, "state": {state}} }, true, "github"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, oauth := s.startLogin(t, f)
			if !tt.cookie {
				oauth = nil
			}
			rec := s.callback(t, tt.query(state).Encode(), oauth)
			if want := "/?login_error=" + tt.reason; rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
				t.Errorf("callback = %d to %q, want 302 to %q", rec.Code, rec.Header().Get("Location"), want)
			}
			if sessionCookieFrom(rec) != nil {
				t.Error("rejected callback set a session cookie")
			}
		})
	}

	var callers int
	if err := s.tx.QueryRow(context.Background(), `SELECT count(*) FROM callers`).Scan(&callers); err != nil {
		t.Fatalf("count callers: %v", err)
	}
	if callers != 0 {
		t.Errorf("callers = %d, want none created", callers)
	}
}

func TestGitHubAllowedUsers(t *testing.T) {
	f := newFakeGitHub(t, 7, "stranger")
	s := newServer(t).withGitHub(t, f, "octo", " Friend ")

	state, oauth := s.startLogin(t, f)
	rec := s.callback(t, url.Values{"code": {"good-code"}, "state": {state}}.Encode(), oauth)
	if rec.Header().Get("Location") != "/?login_error=not_allowed" || sessionCookieFrom(rec) != nil {
		t.Errorf("stranger callback = %q, want not_allowed without session", rec.Header().Get("Location"))
	}

	if rec := s.postCaller(t, s.adminToken(t), `{"github_id": "7"}`); rec.Code != http.StatusCreated {
		t.Fatalf("pre-provision caller = %d; body %s", rec.Code, rec.Body)
	}
	state, oauth = s.startLogin(t, f)
	rec = s.callback(t, url.Values{"code": {"good-code"}, "state": {state}}.Encode(), oauth)
	if rec.Header().Get("Location") != "/" || sessionCookieFrom(rec) == nil {
		t.Errorf("pre-provisioned callback = %q, want signed in", rec.Header().Get("Location"))
	}

	f.id, f.login = 8, "FRIEND"
	state, oauth = s.startLogin(t, f)
	rec = s.callback(t, url.Values{"code": {"good-code"}, "state": {state}}.Encode(), oauth)
	if rec.Header().Get("Location") != "/" {
		t.Errorf("allowed user callback = %q, want signed in", rec.Header().Get("Location"))
	}
}

func TestGitHubNotConfigured(t *testing.T) {
	s := newServer(t)
	for _, path := range []string{"/auth/github/login", "/auth/github/callback"} {
		if rec := s.request(t, http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}

	rec := s.request(t, http.MethodGet, "/auth/providers", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"github":false}` {
		t.Errorf("GET /auth/providers = %d %s, want github false", rec.Code, rec.Body)
	}
	f := newFakeGitHub(t, 1, "a")
	if rec := s.withGitHub(t, f).request(t, http.MethodGet, "/auth/providers", ""); !strings.Contains(rec.Body.String(), `"github":true`) {
		t.Errorf("configured providers = %s, want github true", rec.Body)
	}
}

func TestLogout(t *testing.T) {
	s := newServer(t)
	_, session := s.signedIn(t, db.EnvRegularTesting)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204; body %s", rec.Code, rec.Body)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		cleared = cleared || (c.Name == sessionCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Error("logout did not clear the session cookie")
	}
	if rec := s.request(t, http.MethodGet, "/me", session); rec.Code != http.StatusUnauthorized {
		t.Errorf("session after logout = %d, want 401", rec.Code)
	}

	if rec := s.request(t, http.MethodPost, "/auth/logout", s.keyWithRole(t, db.RoleUser)); rec.Code != http.StatusBadRequest {
		t.Errorf("logout with api key = %d, want 400", rec.Code)
	}
}

func TestMe(t *testing.T) {
	s := newServer(t)
	callerID, session := s.signedIn(t, db.EnvPRReview)
	agent := s.keyFor(t, callerID, db.NewAPIKey{Role: db.RoleAgent})

	for name, tc := range map[string]struct {
		token string
		want  meResponse
	}{
		"session": {session, meResponse{Kind: "caller", ID: callerID, Role: db.RoleUser, PermittedEnv: db.EnvPRReview, Session: true}},
		"agent":   {agent, meResponse{Kind: "caller", ID: callerID, Role: db.RoleAgent, PermittedEnv: db.EnvPRReview}},
	} {
		rec := s.request(t, http.MethodGet, "/me", tc.token)
		var got meResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%s: GET /me = %d %s", name, rec.Code, rec.Body)
		}
		got.APIKeyID = nil
		if got != tc.want {
			t.Errorf("%s: me = %+v, want %+v", name, got, tc.want)
		}
	}

	rec := s.request(t, http.MethodGet, "/me", s.adminToken(t))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kind":"admin"`) {
		t.Errorf("admin GET /me = %d %s", rec.Code, rec.Body)
	}
}

func TestSessionPermissionIsLive(t *testing.T) {
	s := newServer(t)
	callerID, session := s.signedIn(t, db.EnvRegularTesting)
	if err := db.SetCallerPermission(context.Background(), s.tx, callerID, db.EnvPRReview, 0); err != nil {
		t.Fatalf("SetCallerPermission() error = %v", err)
	}
	if rec := s.request(t, http.MethodGet, "/me", session); !strings.Contains(rec.Body.String(), `"permitted_env":"PR_REVIEW"`) {
		t.Errorf("GET /me after permission change = %s, want PR_REVIEW", rec.Body)
	}
}

func TestCrossOriginRequestRejected(t *testing.T) {
	s := newServer(t)
	_, session := s.signedIn(t, db.EnvRegularTesting)

	req := httptest.NewRequest(http.MethodPost, "/keys", strings.NewReader(`{"name": "stolen"}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST /keys = %d, want 403", rec.Code)
	}
}

func TestMCPRejectsSessions(t *testing.T) {
	s := newServer(t)
	_, session := s.signedIn(t, db.EnvRegularTesting)
	h := NewHandler(s.tx, s.m, s.sessions, nil)

	if _, err := h.verifyMCPToken(context.Background(), session, nil); err == nil {
		t.Error("verifyMCPToken(session) error = nil, want rejection")
	}
	if _, err := h.verifyMCPToken(context.Background(), s.keyWithRole(t, db.RoleAgent), nil); err != nil {
		t.Errorf("verifyMCPToken(api key) error = %v", err)
	}
}
