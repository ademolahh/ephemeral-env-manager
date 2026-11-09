package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var webFiles embed.FS

func Routes(h *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callers/{id}/budget", h.GetBudget)
	mux.Handle("PUT /callers/{id}/budget", requireAdmin(http.HandlerFunc(h.SetBudget)))
	mux.Handle("POST /envs", requireCaller(http.HandlerFunc(h.CreateEnv)))
	mux.HandleFunc("GET /capacity", h.GetCapacity)
	mux.Handle("PUT /capacity/{provider}", requireAdmin(http.HandlerFunc(h.SetCapacity)))
	mux.Handle("PUT /prices/{provider}", requireAdmin(http.HandlerFunc(h.SetPrices)))
	mux.Handle("GET /envs", requireCaller(http.HandlerFunc(h.ListEnvs)))
	mux.HandleFunc("GET /envs/{id}", h.GetEnv)
	mux.HandleFunc("DELETE /envs/{id}", h.DeleteEnv)
	mux.HandleFunc("POST /envs/{id}/exec", h.ExecEnv)
	mux.Handle("POST /templates", requireAdmin(http.HandlerFunc(h.CreateTemplate)))
	mux.HandleFunc("GET /templates", h.ListTemplates)
	mux.HandleFunc("GET /templates/{name}/{version}", h.GetTemplate)

	assets, _ := fs.Sub(webFiles, "web")
	frontend := http.FileServer(http.FS(assets))

	root := http.NewServeMux()
	root.Handle("GET /{$}", frontend)
	root.Handle("GET /app.js", frontend)
	root.Handle("GET /styles.css", frontend)
	root.HandleFunc("POST /callers", h.CreateCaller)
	root.HandleFunc("POST /admin/login", h.AdminLogin)
	root.Handle("/mcp", h.MCP())
	root.Handle("/", h.authenticate(mux))
	return root
}
