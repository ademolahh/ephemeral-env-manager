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
	mux.HandleFunc("POST /callers", h.CreateCaller)
	mux.HandleFunc("GET /callers/{id}/budget", h.GetBudget)
	mux.Handle("PUT /callers/{id}/budget", requireAdmin(http.HandlerFunc(h.SetBudget)))
	mux.HandleFunc("POST /envs", h.CreateEnv)
	mux.HandleFunc("GET /capacity", h.GetCapacity)
	mux.HandleFunc("GET /envs", h.ListEnvs)
	mux.HandleFunc("GET /envs/{id}", h.GetEnv)
	mux.HandleFunc("DELETE /envs/{id}", h.DeleteEnv)

	assets, _ := fs.Sub(webFiles, "web")
	frontend := http.FileServer(http.FS(assets))
	secured := h.authenticate(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/app.js" || r.URL.Path == "/styles.css") {
			frontend.ServeHTTP(w, r)
			return
		}
		secured.ServeHTTP(w, r)
	})
}
