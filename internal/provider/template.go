package provider

import "time"

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
