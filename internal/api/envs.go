package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

const (
	maxBodyBytes         = 1 << 20
	maxIdempotencyKeyLen = 255
	defaultListLimit     = 10
	maxListLimit         = 100
)

var envStatuses = []db.EnvStatus{db.EnvProvisioning, db.EnvRunning, db.EnvDeleting, db.EnvDeleted, db.EnvFailed}

type listEnvsResponse struct {
	Environments []db.Environment     `json:"environments"`
	Page         int                  `json:"page"`
	Limit        int                  `json:"limit"`
	NextPage     int                  `json:"next_page,omitempty"`
	Total        int                  `json:"total"`
	Counts       map[db.EnvStatus]int `json:"counts"`
}

type createEnvRequest struct {
	Type      db.EnvType         `json:"type"`
	Provider  provider.Type      `json:"provider"`
	Workload  string             `json:"workload"`
	Template  templateField      `json:"template"`
	Resources *resourceOverrides `json:"resources"`
	TTL       string             `json:"ttl"`
}

func (h *Handler) CreateEnv(w http.ResponseWriter, r *http.Request) {
	var req createEnvRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	cr, err := h.createRequest(r.Context(), callerFrom(r.Context()), req, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeServiceError(w, "create environment", err)
		return
	}

	e, err := h.envs.Create(r.Context(), cr)
	if err != nil {
		writeServiceError(w, "create environment", err)
		return
	}

	w.Header().Set("Location", "/envs/"+e.ID.String())
	writeJSON(w, http.StatusAccepted, e)
}

func (h *Handler) createRequest(ctx context.Context, caller db.AuthenticatedCaller, req createEnvRequest, key string) (env.CreateRequest, error) {
	if len(key) > maxIdempotencyKeyLen {
		return env.CreateRequest{}, badRequest("Idempotency-Key longer than %d characters", maxIdempotencyKeyLen)
	}

	tr, err := h.resolveTemplate(ctx, req.Template)
	if err != nil {
		return env.CreateRequest{}, err
	}

	if err := req.override(&tr); err != nil {
		return env.CreateRequest{}, badRequest("%s", err)
	}

	tpl, err := tr.toTemplate()
	if err != nil {
		return env.CreateRequest{}, badRequest("%s", err)
	}

	if req.Workload == "" {
		return env.CreateRequest{}, badRequest("workload is required")
	}

	return env.CreateRequest{
		CallerID:       caller.ID,
		APIKeyID:       caller.KeyID,
		Type:           req.Type,
		Provider:       req.Provider,
		Workload:       req.Workload,
		Template:       tpl,
		IdempotencyKey: key,
	}, nil
}

func (h *Handler) GetEnv(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}

	var e db.Environment
	var status provider.Status
	if _, admin := adminFrom(r.Context()); admin {
		e, status, err = h.envs.GetAny(r.Context(), id)
	} else {
		e, status, err = h.envs.Get(r.Context(), callerFrom(r.Context()), id)
	}
	if err != nil {
		writeServiceError(w, "get environment", err)
		return
	}

	e.InstanceStatus = status
	writeJSON(w, http.StatusOK, e)
}

func (h *Handler) ListEnvs(w http.ResponseWriter, r *http.Request) {
	status, page, limit, err := parseListParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp, err := h.listEnvs(r.Context(), callerFrom(r.Context()), status, page, limit)
	if err != nil {
		writeServiceError(w, "list environments", err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) listEnvs(ctx context.Context, caller db.AuthenticatedCaller, status db.EnvStatus, page, limit int) (listEnvsResponse, error) {
	envs, err := db.ListCallerEnvironments(ctx, h.db, caller.ID, db.EnvironmentFilter{
		APIKeyID: caller.AgentKey(),
		Status:   status,
		Limit:    limit + 1,
		Offset:   (page - 1) * limit,
	})
	if err != nil {
		return listEnvsResponse{}, err
	}

	counts, err := db.CountCallerEnvironments(ctx, h.db, caller.ID, caller.AgentKey())
	if err != nil {
		return listEnvsResponse{}, err
	}

	resp := listEnvsResponse{Environments: envs, Page: page, Limit: limit, Counts: map[db.EnvStatus]int{}}
	if len(envs) > limit {
		resp.Environments = envs[:limit]
		resp.NextPage = page + 1
	}

	if resp.Environments == nil {
		resp.Environments = []db.Environment{}
	}

	for _, s := range envStatuses {
		resp.Counts[s] = counts[s]
		resp.Total += counts[s]
	}

	return resp, nil
}

func parseListParams(r *http.Request) (db.EnvStatus, int, int, error) {
	q := r.URL.Query()
	status, err := envStatus(q.Get("status"))
	if err != nil {
		return "", 0, 0, err
	}

	page, err := positiveParam(q.Get("page"), 1, 0)
	if err != nil {
		return "", 0, 0, errors.New("page must be a positive number")
	}

	limit, err := positiveParam(q.Get("limit"), defaultListLimit, maxListLimit)
	if err != nil {
		return "", 0, 0, fmt.Errorf("limit must be between 1 and %d", maxListLimit)
	}

	return status, page, limit, nil
}

func envStatus(v string) (db.EnvStatus, error) {
	if v == "" {
		return "", nil
	}

	status := db.EnvStatus(strings.ToUpper(v))
	if !slices.Contains(envStatuses, status) {
		return "", fmt.Errorf("status %q must be one of PROVISIONING, RUNNING, DELETING, DELETED, FAILED", v)
	}

	return status, nil
}

func positiveParam(v string, def, max int) (int, error) {
	if v == "" {
		return def, nil
	}

	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || (max > 0 && n > max) {
		return 0, errors.New("out of range")
	}

	return n, nil
}

func (h *Handler) DeleteEnv(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}

	var e db.Environment
	if _, admin := adminFrom(r.Context()); admin {
		e, err = h.envs.DeleteAny(r.Context(), id)
	} else {
		e, err = h.envs.Delete(r.Context(), callerFrom(r.Context()), id)
	}
	if err != nil {
		writeServiceError(w, "delete environment", err)
		return
	}

	status := http.StatusOK
	if e.Status == db.EnvDeleting {
		status = http.StatusAccepted
	}
	writeJSON(w, status, e)
}

type execRequest struct {
	Cmd []string `json:"cmd"`
}

type execResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

func (h *Handler) ExecEnv(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}

	var req execRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.Cmd) == 0 || req.Cmd[0] == "" {
		writeError(w, http.StatusBadRequest, "cmd is required")
		return
	}

	var out string
	if _, admin := adminFrom(r.Context()); admin {
		out, err = h.envs.ExecAny(r.Context(), id, req.Cmd)
	} else {
		out, err = h.envs.Exec(r.Context(), callerFrom(r.Context()), id, req.Cmd)
	}

	if exit, ok := errors.AsType[*provider.ExitError](err); ok {
		writeJSON(w, http.StatusOK, execResponse{Output: out, ExitCode: exit.Code})
		return
	}

	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, "command timed out")
		return
	}

	if err != nil {
		writeServiceError(w, "exec environment", err)
		return
	}

	writeJSON(w, http.StatusOK, execResponse{Output: out})
}
