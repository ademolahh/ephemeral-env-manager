package provider

import (
	"time"
)

type Template struct {
	Name            string        `yaml:"name" json:"name"`
	Version         string        `yaml:"version" json:"version"`
	Providers       []Type        `yaml:"providers" json:"providers"`
	DefaultProvider Type          `yaml:"default_provider" json:"default_provider"`
	TTL             time.Duration `yaml:"ttl" json:"-"`
	Workload        Workload      `yaml:"workload" json:"workload"`
	Services        []Service     `yaml:"services" json:"services"`
}

type Workload struct {
	Ports       []int             `yaml:"ports" json:"ports"`
	Resources   Resources         `yaml:"resources" json:"resources"`
	Healthcheck string            `yaml:"healthcheck" json:"healthcheck"`
	Env         map[string]string `yaml:"env" json:"env"`
}

type Service struct {
	Name        string            `yaml:"name" json:"name"`
	Image       string            `yaml:"image" json:"image"`
	Resources   Resources         `yaml:"resources" json:"resources"`
	Env         map[string]string `yaml:"env" json:"env"`
	Healthcheck string            `yaml:"healthcheck" json:"healthcheck"`
}

type Resources struct {
	CPUCore int `yaml:"cpu_core" json:"cpu_core"`
	RAMGB   int `yaml:"ram_gb" json:"ram_gb"`
	DiskGB  int `yaml:"disk_gb" json:"disk_gb"`
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
