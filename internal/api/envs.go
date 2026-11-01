package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/env"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

const (
	maxBodyBytes         = 1 << 20
	maxIdempotencyKeyLen = 255
)

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

type envResponse struct {
	ID              uuid.UUID          `json:"id"`
	Status          db.EnvStatus       `json:"status"`
	Type            db.EnvType         `json:"type"`
	Provider        provider.Type      `json:"provider"`
	Endpoint        *string            `json:"endpoint"`
	Resources       provider.Resources `json:"resources"`
	HourlyRate      decimal.Decimal    `json:"hourly_rate"`
	TemplateName    string             `json:"template_name"`
	TemplateVersion string             `json:"template_version"`
	FailureReason   *string            `json:"failure_reason,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	ExpiresAt       time.Time          `json:"expires_at"`
}

func (h *Handler) CreateEnv(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) > maxIdempotencyKeyLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Idempotency-Key longer than %d characters", maxIdempotencyKeyLen))
		return
	}

	var req createEnvRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}

		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
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
	writeJSON(w, http.StatusAccepted, toEnvResponse(e))
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

func toEnvResponse(e db.Environment) envResponse {
	return envResponse{
		ID:              e.ID,
		Status:          e.Status,
		Type:            e.Type,
		Provider:        e.Provider,
		Endpoint:        e.Endpoint,
		Resources:       e.Resources,
		HourlyRate:      e.HourlyRate,
		TemplateName:    e.TemplateName,
		TemplateVersion: e.TemplateVersion,
		FailureReason:   e.FailureReason,
		CreatedAt:       e.CreatedAt,
		ExpiresAt:       e.ExpiresAt,
	}
}
