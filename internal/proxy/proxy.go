package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/metrics"
)

var (
	ErrNoContainer = errors.New("PROXY_CONTAINER is required when PROXY_DOMAIN is set")
	ErrWeakSecret  = fmt.Errorf("PROXY_SECRET must be at least %d bytes when PROXY_DOMAIN is set", minSecretLen)
)

const (
	minSecretLen = 32

	// An environment's access token can arrive as this header (for scripts
	// and agents), as this query parameter (in the access URL) or as this
	// cookie (which the proxy sets after the first visit with the parameter).
	TokenHeader = "X-EEM-Access-Token"
	TokenParam  = "eem_token"
	TokenCookie = "eem_access"
)

type Proxy struct {
	Domain     string
	PublicPort int
	Container  string
	// Secret signs environment access tokens. eem and the proxy must share it.
	Secret []byte
}

// SecretFromEnv reads PROXY_SECRET and checks its length.
func SecretFromEnv() ([]byte, error) {
	secret := []byte(os.Getenv("PROXY_SECRET"))
	if len(secret) < minSecretLen {
		return nil, ErrWeakSecret
	}
	return secret, nil
}

func FromEnv() (*Proxy, error) {
	domain := os.Getenv("PROXY_DOMAIN")
	if domain == "" {
		return nil, nil
	}

	p := &Proxy{Domain: domain, Container: os.Getenv("PROXY_CONTAINER")}
	if p.Container == "" {
		return nil, ErrNoContainer
	}

	secret, err := SecretFromEnv()
	if err != nil {
		return nil, err
	}
	p.Secret = secret

	if v := os.Getenv("PROXY_PUBLIC_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("PROXY_PUBLIC_PORT %q must be a port number", v)
		}
		p.PublicPort = port
	}

	return p, nil
}

func (p *Proxy) Host(id uuid.UUID) string {
	return "env-" + id.String() + "." + p.Domain
}

func (p *Proxy) URL(id uuid.UUID) string {
	if p.PublicPort == 0 || p.PublicPort == 80 {
		return "http://" + p.Host(id)
	}
	return "http://" + p.Host(id) + ":" + strconv.Itoa(p.PublicPort)
}

// AccessToken is the token that opens environment id through the proxy. It
// is derived from the secret, so it needs no storage and stops working when
// the secret rotates.
func (p *Proxy) AccessToken(id uuid.UUID) string {
	mac := hmac.New(sha256.New, p.Secret)
	mac.Write([]byte("eem-env-access:" + id.String()))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// AccessURL opens environment id in a browser: the proxy swaps the token in
// the query for a cookie and redirects to the bare URL.
func (p *Proxy) AccessURL(id uuid.UUID) string {
	return p.URL(id) + "/?" + url.Values{TokenParam: {p.AccessToken(id)}}.Encode()
}

func (p *Proxy) authorized(id uuid.UUID, token string) bool {
	return token != "" && hmac.Equal([]byte(token), []byte(p.AccessToken(id)))
}

func (p *Proxy) EnvID(host string) (uuid.UUID, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	name, ok := strings.CutSuffix(strings.ToLower(host), "."+strings.ToLower(p.Domain))
	if !ok {
		return uuid.Nil, false
	}

	raw, ok := strings.CutPrefix(name, "env-")
	if !ok || len(raw) != 36 {
		return uuid.Nil, false
	}

	id, err := uuid.Parse(raw)
	return id, err == nil
}

type backendKey struct{}

func (p *Proxy) Handler(q db.DBTX) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(&url.URL{Scheme: "http", Host: r.In.Context().Value(backendKey{}).(string)})
			r.Out.Host = r.In.Host
			r.SetXForwarded()
			stripToken(r.Out)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("proxy backend unavailable", "host", r.Host, "err", err)
			http.Error(w, "environment unavailable", http.StatusBadGateway)
		},
	}

	return promhttp.InstrumentHandlerCounter(metrics.ProxyRequests, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := p.EnvID(r.Host)
		if !ok {
			http.Error(w, "environment not found", http.StatusNotFound)
			return
		}

		if !p.authenticate(w, r, id) {
			return
		}

		backend, err := db.RouteBackend(r.Context(), q, id)
		if errors.Is(err, db.ErrRouteNotFound) {
			http.Error(w, "environment not found", http.StatusNotFound)
			return
		}

		if err != nil {
			slog.Error("proxy route lookup", "host", r.Host, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), backendKey{}, backend)))
	}))
}

// authenticate checks the request's access token for environment id. A valid
// token in the query of a GET sets the cookie and redirects to the same URL
// without it, so the token leaves the address bar and browser history.
func (p *Proxy) authenticate(w http.ResponseWriter, r *http.Request, id uuid.UUID) bool {
	if p.authorized(id, r.Header.Get(TokenHeader)) {
		return true
	}

	if c, err := r.Cookie(TokenCookie); err == nil && p.authorized(id, c.Value) {
		return true
	}

	if token := r.URL.Query().Get(TokenParam); p.authorized(id, token) {
		http.SetCookie(w, &http.Cookie{
			Name:     TokenCookie,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
			SameSite: http.SameSiteLaxMode,
		})

		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			u := *r.URL
			q := u.Query()
			q.Del(TokenParam)
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusSeeOther)
			return false
		}
		return true
	}

	w.Header().Set("WWW-Authenticate", `Bearer realm="eem environment"`)
	http.Error(w, "environment access token required: open the environment's access_url or send "+TokenHeader, http.StatusUnauthorized)
	return false
}

// stripToken removes the access token from a request before it reaches the
// workload, which must never see it.
func stripToken(out *http.Request) {
	out.Header.Del(TokenHeader)

	if q := out.URL.Query(); q.Has(TokenParam) {
		q.Del(TokenParam)
		out.URL.RawQuery = q.Encode()
	}

	// Filter the raw header so the workload's own cookies pass through
	// byte for byte.
	var kept []string
	for _, line := range out.Header.Values("Cookie") {
		for part := range strings.SplitSeq(line, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
			if name != TokenCookie && strings.TrimSpace(part) != "" {
				kept = append(kept, strings.TrimSpace(part))
			}
		}
	}
	out.Header.Del("Cookie")
	if len(kept) > 0 {
		out.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}
