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
	writeJSON(w, http.StatusAccepted, h.envs.Expose(e))
}

func (h *Handler) createRequest(ctx context.Context, caller db.AuthenticatedCaller, req createEnvRequest, key string) (env.CreateRequest, error) {
	if len(key) > maxIdempotencyKeyLen {
		return env.CreateRequest{}, badRequest("Idempotency-Key longer than %d characters", maxIdempotencyKeyLen)
	}

	if req.Template.Inline != nil && h.DisableInlineTemplates {
		return env.CreateRequest{}, badRequest("inline templates are disabled; use a stored template as name@version")
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

	if req.Template.Inline != nil {
		tpl.Name, tpl.Version = inlineIdentity(tr)
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

	e, status, err := h.envs.Get(r.Context(), scope(r.Context()), id)
	if err != nil {
		writeServiceError(w, "get environment", err)
		return
	}

	e.InstanceStatus = status
	writeJSON(w, http.StatusOK, h.envs.Expose(e))
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

	for i, e := range resp.Environments {
		resp.Environments[i] = h.envs.Expose(e)
	}

	for _, s := range db.EnvStatuses {
		resp.Counts[s] = counts[s]
		resp.Total += counts[s]
	}

	return resp, nil
}

func parseListParams(r *http.Request) (db.EnvStatus, int, int, error) {
	q := r.URL.Query()
	page, err := intParam(q.Get("page"), "page")
	if err != nil {
		return "", 0, 0, err
	}

	limit, err := intParam(q.Get("limit"), "limit")
	if err != nil {
		return "", 0, 0, err
	}

	return listParams(q.Get("status"), page, limit)
}

func intParam(v, name string) (*int, error) {
	if v == "" {
		return nil, nil
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf("%s must be a number", name)
	}
	return &n, nil
}

func listParams(status string, page, limit *int) (db.EnvStatus, int, int, error) {
	s, err := envStatus(status)
	if err != nil {
		return "", 0, 0, err
	}

	p, l := 1, defaultListLimit
	if page != nil {
		p = *page
	}
	if limit != nil {
		l = *limit
	}

	if p < 1 {
		return "", 0, 0, errors.New("page must be a positive number")
	}
	if l < 1 || l > maxListLimit {
		return "", 0, 0, fmt.Errorf("limit must be between 1 and %d", maxListLimit)
	}
	return s, p, l, nil
}

func envStatus(v string) (db.EnvStatus, error) {
	if v == "" {
		return "", nil
	}

	status := db.EnvStatus(strings.ToUpper(v))
	if !slices.Contains(db.EnvStatuses, status) {
		return "", fmt.Errorf("status %q must be %s", v, oneOf(db.EnvStatuses))
	}

	return status, nil
}

func (h *Handler) DeleteEnv(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}

	e, err := h.envs.Delete(r.Context(), scope(r.Context()), id)
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
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
}

var errCommandTimeout = errors.New("command timed out")

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

	resp, err := h.execEnv(r.Context(), scope(r.Context()), id, req.Cmd)
	if err != nil {
		writeServiceError(w, "exec environment", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) execEnv(ctx context.Context, caller *db.AuthenticatedCaller, id uuid.UUID, cmd []string) (execResponse, error) {
	res, err := h.envs.Exec(ctx, caller, id, cmd)
	resp := execResponse{Output: res.Output, Truncated: res.Truncated}

	if exit, ok := errors.AsType[*provider.ExitError](err); ok {
		resp.ExitCode = exit.Code
		return resp, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return execResponse{}, errCommandTimeout
	}
	if err != nil {
		return execResponse{}, err
	}
	return resp, nil
}
