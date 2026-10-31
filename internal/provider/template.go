package provider

import (
	"time"

	"github.com/shopspring/decimal"
)

type Template struct {
	Name            string        `yaml:"name"`
	Version         string        `yaml:"version"`
	Providers       []Type        `yaml:"providers"`
	DefaultProvider Type          `yaml:"default_provider"`
	TTL             time.Duration `yaml:"ttl"`
	Workload        Workload      `yaml:"workload"`
	Services        []Service     `yaml:"services"`
}

type Workload struct {
	Ports       []int             `yaml:"ports"`
	Resources   Resources         `yaml:"resources"`
	Healthcheck string            `yaml:"healthcheck"`
	Env         map[string]string `yaml:"env"`
}

type Service struct {
	Name        string            `yaml:"name"`
	Image       string            `yaml:"image"`
	Resources   Resources         `yaml:"resources"`
	Env         map[string]string `yaml:"env"`
	Healthcheck string            `yaml:"healthcheck"`
}

type Resources struct {
	CPUCore int `yaml:"cpu_core"`
	RAMGB   int `yaml:"ram_gb"`
	DiskGB  int `yaml:"disk_gb"`
}

type Prices struct {
	CPUCore decimal.Decimal
	RAMGB   decimal.Decimal
	DiskGB  decimal.Decimal
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

func (t Template) HourlyCost(p Prices) decimal.Decimal {
	r := t.TotalResources()
	return p.CPUCore.Mul(decimal.NewFromInt(int64(r.CPUCore))).
		Add(p.RAMGB.Mul(decimal.NewFromInt(int64(r.RAMGB)))).
		Add(p.DiskGB.Mul(decimal.NewFromInt(int64(r.DiskGB))))
}
