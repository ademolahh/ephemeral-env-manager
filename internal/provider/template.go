package provider

import (
	"time"
)

type Template struct {
	Name            string        `json:"name"`
	Version         string        `json:"version"`
	Providers       []Type        `json:"providers"`
	DefaultProvider Type          `json:"default_provider"`
	TTL             time.Duration `json:"-"`
	Workload        Workload      `json:"workload"`
	Services        []Service     `json:"services"`
}

type Workload struct {
	Ports       []int             `json:"ports"`
	Resources   Resources         `json:"resources"`
	Healthcheck string            `json:"healthcheck"`
	Env         map[string]string `json:"env"`
	Aliases     []string          `json:"aliases,omitempty"`
}

type Service struct {
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Resources   Resources         `json:"resources"`
	Env         map[string]string `json:"env"`
	Healthcheck string            `json:"healthcheck"`
	Aliases     []string          `json:"aliases,omitempty"`
}

type Resources struct {
	CPUCore int `json:"cpu_core"`
	RAMGB   int `json:"ram_gb"`
	DiskGB  int `json:"disk_gb"`
}

type Prices struct {
	CPUCore int64
	RAMGB   int64
	DiskGB  int64
}

func (t Template) TotalResources() Resources {
	total := t.Workload.Resources
	for _, s := range t.Services {
		total.CPUCore += s.Resources.CPUCore
		total.RAMGB += s.Resources.RAMGB
		total.DiskGB += s.Resources.DiskGB
	}
	return total
}

func (t Template) HourlyCost(p Prices) int64 {
	r := t.TotalResources()
	return p.CPUCore*int64(r.CPUCore) + p.RAMGB*int64(r.RAMGB) + p.DiskGB*int64(r.DiskGB)
}
