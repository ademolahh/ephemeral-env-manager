package provider

import (
	"strconv"
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

// Smallest per-container limits. Docker treats a zero limit as unlimited and
// refuses memory below 6 MiB.
const (
	MinCPUMillis = 10
	MinRAMMB     = 16
)

// Resources are CPU in millicores (1000 = one core), RAM in MiB and disk in GB.
type Resources struct {
	CPUMillis int `json:"cpu_millis"`
	RAMMB     int `json:"ram_mb"`
	DiskGB    int `json:"disk_gb"`
}

func (r Resources) String() string {
	return strconv.FormatFloat(float64(r.CPUMillis)/1000, 'f', -1, 64) + " cpu, " +
		strconv.Itoa(r.RAMMB) + " MiB ram, " + strconv.Itoa(r.DiskGB) + " GB disk"
}

// Prices are hourly prices in micro-USD per CPU core, per GB of RAM and per
// GB of disk.
type Prices struct {
	PerCPUCore int64
	PerRAMGB   int64
	PerDiskGB  int64
}

func (t Template) TotalResources() Resources {
	total := t.Workload.Resources
	for _, s := range t.Services {
		total.CPUMillis += s.Resources.CPUMillis
		total.RAMMB += s.Resources.RAMMB
		total.DiskGB += s.Resources.DiskGB
	}
	return total
}

// HourlyCost is the template's price per hour in micro-USD. Partial cores and
// GB are priced pro rata and the total is rounded up to the next micro-USD.
func (t Template) HourlyCost(p Prices) int64 {
	const scale = 1000 * 1024
	r := t.TotalResources()
	partial := p.PerCPUCore*int64(r.CPUMillis)*1024 + p.PerRAMGB*int64(r.RAMMB)*1000
	return (partial+scale-1)/scale + p.PerDiskGB*int64(r.DiskGB)
}
