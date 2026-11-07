package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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

var serviceName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type createEnvRequest struct {
	Type     db.EnvType      `json:"type"`
	Provider provider.Type   `json:"provider"`
	Workload string          `json:"workload"`
	Template templateRequest `json:"template"`
}

type templateRequest struct {
	provider.Template
	TTL string `json:"ttl"`
}

func (h *Handler) CreateEnv(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) > maxIdempotencyKeyLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Idempotency-Key longer than %d characters", maxIdempotencyKeyLen))
		return
	}

	var req createEnvRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	tpl, err := req.Template.toTemplate()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Workload == "" {
		writeError(w, http.StatusBadRequest, "workload is required")
		return
	}

	e, err := h.envs.Create(r.Context(), env.CreateRequest{
		CallerID:       callerFrom(r.Context()).ID,
		Type:           req.Type,
		Provider:       req.Provider,
		Workload:       req.Workload,
		Template:       tpl,
		IdempotencyKey: key,
	})

	if err != nil {
		writeServiceError(w, "create environment", err)
		return
	}

	w.Header().Set("Location", "/envs/"+e.ID.String())
	writeJSON(w, http.StatusAccepted, e)
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

	callerID := callerFrom(r.Context()).ID
	envs, err := db.ListCallerEnvironments(r.Context(), h.db, callerID, db.EnvironmentFilter{
		Status: status,
		Limit:  limit + 1,
		Offset: (page - 1) * limit,
	})
	if err != nil {
		writeServiceError(w, "list environments", err)
		return
	}

	counts, err := db.CountCallerEnvironments(r.Context(), h.db, callerID)
	if err != nil {
		writeServiceError(w, "count environments", err)
		return
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

	writeJSON(w, http.StatusOK, resp)
}

func parseListParams(r *http.Request) (db.EnvStatus, int, int, error) {
	q := r.URL.Query()
	var status db.EnvStatus
	if v := q.Get("status"); v != "" {
		status = db.EnvStatus(strings.ToUpper(v))
		if !slices.Contains(envStatuses, status) {
			return "", 0, 0, fmt.Errorf("status %q must be one of PROVISIONING, RUNNING, DELETING, DELETED, FAILED", v)
		}
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

func (t templateRequest) toTemplate() (provider.Template, error) {
	tpl := t.Template
	if tpl.Name == "" || tpl.Version == "" {
		return provider.Template{}, errors.New("template name and version are required")
	}

	if t.TTL == "" {
		return provider.Template{}, errors.New("template ttl is required")
	}

	ttl, err := time.ParseDuration(t.TTL)
	if err != nil || ttl <= 0 {
		return provider.Template{}, fmt.Errorf("template ttl %q must be a positive duration like 4h", t.TTL)
	}
	tpl.TTL = ttl

	if err := validResources("workload", tpl.Workload.Resources); err != nil {
		return provider.Template{}, err
	}

	for _, p := range tpl.Workload.Ports {
		if p < 1 || p > 65535 {
			return provider.Template{}, fmt.Errorf("workload port %d out of range", p)
		}
	}

	seen := map[string]bool{"workload": true}
	for _, s := range tpl.Services {
		if !serviceName.MatchString(s.Name) {
			return provider.Template{}, fmt.Errorf("service name %q must be lowercase letters, digits and hyphens", s.Name)
		}

		if seen[s.Name] {
			return provider.Template{}, fmt.Errorf("service name %q is reserved or duplicated", s.Name)
		}
		seen[s.Name] = true

		if s.Image == "" {
			return provider.Template{}, fmt.Errorf("service %s image is required", s.Name)
		}

		if err := validResources("service "+s.Name, s.Resources); err != nil {
			return provider.Template{}, err
		}
	}

	return tpl, nil
}

func validResources(what string, r provider.Resources) error {
	if r.CPUCore < 1 || r.RAMGB < 1 || r.DiskGB < 1 {
		return fmt.Errorf("%s resources must be at least 1 cpu_core, 1 ram_gb and 1 disk_gb", what)
	}
	return nil
}
