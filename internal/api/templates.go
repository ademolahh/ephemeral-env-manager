package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

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
	Workload *resourcePatch           `json:"workload"`
	Services map[string]resourcePatch `json:"services"`
}

type resourcePatch struct {
	CPUCore *int `json:"cpu_core"`
	RAMGB   *int `json:"ram_gb"`
	DiskGB  *int `json:"disk_gb"`
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

func (h *Handler) templateFor(w http.ResponseWriter, r *http.Request, f templateField) (templateRequest, bool) {
	if f.Inline != nil {
		return *f.Inline, true
	}

	name, version, ok := strings.Cut(f.Ref, "@")
	if !ok || name == "" || version == "" {
		writeError(w, http.StatusBadRequest, "template must be an object or name@version")
		return templateRequest{}, false
	}

	spec, err := db.GetTemplate(r.Context(), h.db, name, version)
	if errors.Is(err, db.ErrTemplateNotFound) {
		writeError(w, http.StatusBadRequest, "template "+f.Ref+" not found")
		return templateRequest{}, false
	}

	if err != nil {
		writeServiceError(w, "create environment", err)
		return templateRequest{}, false
	}

	var tr templateRequest
	if err := json.Unmarshal(spec, &tr); err != nil {
		log.Printf("decode template %s: %v", f.Ref, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return templateRequest{}, false
	}

	return tr, true
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
