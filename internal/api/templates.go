package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

var serviceName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type templateRequest struct {
	provider.Template
	TTL string `json:"ttl"`
}

type templateField struct {
	Ref    string
	Inline *templateRequest
}

func (f *templateField) UnmarshalJSON(b []byte) error {
	if bytes.HasPrefix(b, []byte(`"`)) {
		return json.Unmarshal(b, &f.Ref)
	}

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	f.Inline = &templateRequest{}
	return dec.Decode(f.Inline)
}

type resourceOverrides struct {
	Workload *resourcePatch           `json:"workload,omitempty"`
	Services map[string]resourcePatch `json:"services,omitempty"`
}

type resourcePatch struct {
	CPUCore *int `json:"cpu_core,omitempty"`
	RAMGB   *int `json:"ram_gb,omitempty"`
	DiskGB  *int `json:"disk_gb,omitempty"`
}

func (p resourcePatch) apply(r *provider.Resources) {
	if p.CPUCore != nil {
		r.CPUCore = *p.CPUCore
	}
	if p.RAMGB != nil {
		r.RAMGB = *p.RAMGB
	}
	if p.DiskGB != nil {
		r.DiskGB = *p.DiskGB
	}
}

func (req createEnvRequest) override(tr *templateRequest) error {
	if req.TTL != "" {
		tr.TTL = req.TTL
	}

	if req.Resources == nil {
		return nil
	}

	if req.Resources.Workload != nil {
		req.Resources.Workload.apply(&tr.Workload.Resources)
	}

	for name, patch := range req.Resources.Services {
		i := slices.IndexFunc(tr.Services, func(s provider.Service) bool { return s.Name == name })
		if i < 0 {
			return fmt.Errorf("service %q is not in the template", name)
		}
		patch.apply(&tr.Services[i].Resources)
	}

	return nil
}

func (h *Handler) resolveTemplate(ctx context.Context, f templateField) (templateRequest, error) {
	if f.Inline != nil {
		return *f.Inline, nil
	}

	name, version, ok := strings.Cut(f.Ref, "@")
	if !ok || name == "" || version == "" {
		return templateRequest{}, badRequest("template must be an object or name@version")
	}

	spec, err := db.GetTemplate(ctx, h.db, name, version)
	if errors.Is(err, db.ErrTemplateNotFound) {
		return templateRequest{}, badRequest("template %s not found", f.Ref)
	}

	if err != nil {
		return templateRequest{}, err
	}

	var tr templateRequest
	if err := json.Unmarshal(spec, &tr); err != nil {
		return templateRequest{}, fmt.Errorf("decode template %s: %w", f.Ref, err)
	}

	return tr, nil
}

func (h *Handler) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	var req templateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if _, err := req.toTemplate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	spec, err := json.Marshal(req)
	if err != nil {
		writeServiceError(w, "create template", err)
		return
	}

	if err := db.CreateTemplate(r.Context(), h.db, req.Name, req.Version, spec); err != nil {
		writeServiceError(w, "create template", err)
		return
	}

	w.Header().Set("Location", "/templates/"+req.Name+"/"+req.Version)
	writeJSON(w, http.StatusCreated, json.RawMessage(spec))
}

func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	specs, err := db.ListTemplates(r.Context(), h.db)
	if err != nil {
		writeServiceError(w, "list templates", err)
		return
	}

	if specs == nil {
		specs = []json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, map[string][]json.RawMessage{"templates": specs})
}

func (h *Handler) GetTemplate(w http.ResponseWriter, r *http.Request) {
	spec, err := db.GetTemplate(r.Context(), h.db, r.PathValue("name"), r.PathValue("version"))
	if err != nil {
		writeServiceError(w, "get template", err)
		return
	}

	writeJSON(w, http.StatusOK, spec)
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
