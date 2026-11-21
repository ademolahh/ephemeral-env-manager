package api

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/ademolahh/ephemeral-env-manager/internal/metrics"
)

//go:embed web/*
var webFiles embed.FS

func Routes(h *Handler) http.Handler {
	mux := http.NewServeMux()
	handle(mux, "GET /me", http.HandlerFunc(h.Me))
	handle(mux, "POST /auth/logout", http.HandlerFunc(h.Logout))
	handle(mux, "POST /keys", requireCallerSession(http.HandlerFunc(h.CreateKey)))
	handle(mux, "GET /keys", requireCallerSession(http.HandlerFunc(h.ListKeys)))
	handle(mux, "DELETE /keys/{id}", http.HandlerFunc(h.RevokeKey))
	handle(mux, "POST /callers", requireAdmin(http.HandlerFunc(h.CreateCaller)))
	handle(mux, "POST /callers/{id}/keys", requireAdmin(http.HandlerFunc(h.CreateCallerKey)))
	handle(mux, "GET /callers/{id}/keys", requireAdmin(http.HandlerFunc(h.ListCallerKeys)))
	handle(mux, "GET /callers/{id}/budget", http.HandlerFunc(h.GetBudget))
	handle(mux, "PUT /callers/{id}/budget", requireAdmin(http.HandlerFunc(h.SetBudget)))
	handle(mux, "PUT /callers/{id}/permission", requireAdmin(http.HandlerFunc(h.SetPermission)))
	handle(mux, "POST /envs", requireCaller(http.HandlerFunc(h.CreateEnv)))
	handle(mux, "GET /capacity", http.HandlerFunc(h.GetCapacity))
	handle(mux, "PUT /capacity/{provider}", requireAdmin(http.HandlerFunc(h.SetCapacity)))
	handle(mux, "PUT /prices/{provider}", requireAdmin(http.HandlerFunc(h.SetPrices)))
	handle(mux, "GET /envs", requireCaller(http.HandlerFunc(h.ListEnvs)))
	handle(mux, "GET /envs/{id}", http.HandlerFunc(h.GetEnv))
	handle(mux, "DELETE /envs/{id}", http.HandlerFunc(h.DeleteEnv))
	handle(mux, "POST /envs/{id}/exec", http.HandlerFunc(h.ExecEnv))
	handle(mux, "POST /templates", requireAdmin(http.HandlerFunc(h.CreateTemplate)))
	handle(mux, "GET /templates", http.HandlerFunc(h.ListTemplates))
	handle(mux, "GET /templates/{name}/{version}", http.HandlerFunc(h.GetTemplate))

	assets, _ := fs.Sub(webFiles, "web")
	frontend := http.FileServer(http.FS(assets))

	root := http.NewServeMux()
	handle(root, "GET /{$}", frontend)
	handle(root, "GET /app.js", frontend)
	handle(root, "GET /styles.css", frontend)
	handle(root, "POST /admin/login", http.HandlerFunc(h.AdminLogin))
	handle(root, "GET /auth/providers", http.HandlerFunc(h.AuthProviders))
	handle(root, "GET /auth/github/login", http.HandlerFunc(h.GitHubLogin))
	handle(root, "GET /auth/github/callback", http.HandlerFunc(h.GitHubCallback))
	handle(root, "/mcp", h.MCP())
	root.Handle("/", h.authenticate(mux))
	return http.NewCrossOriginProtection().Handler(root)
}

func handle(m *http.ServeMux, pattern string, h http.Handler) {
	m.Handle(pattern, metrics.InstrumentHTTP(pattern, h))
}
