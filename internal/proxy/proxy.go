package proxy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
)

var ErrNoContainer = errors.New("PROXY_CONTAINER is required when PROXY_DOMAIN is set")

type Proxy struct {
	Domain     string
	PublicPort int
	Container  string
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
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy %s: %v", r.Host, err)
			http.Error(w, "environment unavailable", http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := p.EnvID(r.Host)
		if !ok {
			http.Error(w, "environment not found", http.StatusNotFound)
			return
		}

		backend, err := db.RouteBackend(r.Context(), q, id)
		if errors.Is(err, db.ErrRouteNotFound) {
			http.Error(w, "environment not found", http.StatusNotFound)
			return
		}

		if err != nil {
			log.Printf("proxy %s: %v", r.Host, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), backendKey{}, backend)))
	})
}
