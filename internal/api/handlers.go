package api

import "net/http"

type Handler struct{}

func (h *Handler) CreateCaller(w http.ResponseWriter, r *http.Request) {}

func (h *Handler) GetBudget(w http.ResponseWriter, r *http.Request) {}

func (h *Handler) SetBudget(w http.ResponseWriter, r *http.Request) {}

func (h *Handler) CreateEnv(w http.ResponseWriter, r *http.Request) {}

func (h *Handler) GetEnv(w http.ResponseWriter, r *http.Request) {}

func (h *Handler) DeleteEnv(w http.ResponseWriter, r *http.Request) {}
