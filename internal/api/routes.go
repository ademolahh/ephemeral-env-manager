package api

import "net/http"

func Routes(h *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /callers", h.CreateCaller)
	mux.HandleFunc("GET /callers/{id}/budget", h.GetBudget)
	mux.Handle("PUT /callers/{id}/budget", requireAdmin(http.HandlerFunc(h.SetBudget)))
	mux.HandleFunc("POST /envs", h.CreateEnv)
	mux.HandleFunc("GET /envs/{id}", h.GetEnv)
	mux.HandleFunc("DELETE /envs/{id}", h.DeleteEnv)
	return h.authenticate(mux)
}
