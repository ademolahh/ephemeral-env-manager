package api

import "net/http"

func Routes(h *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /callers", h.CreateCaller)
	mux.HandleFunc("GET /callers/{id}/budget", h.GetBudget)
	mux.HandleFunc("PUT /callers/{id}/budget", h.SetBudget)
	mux.HandleFunc("POST /envs", h.CreateEnv)
	mux.HandleFunc("GET /envs/{id}", h.GetEnv)
	mux.HandleFunc("DELETE /envs/{id}", h.DeleteEnv)
	return mux
}
