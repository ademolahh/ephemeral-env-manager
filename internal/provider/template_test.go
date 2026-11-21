package provider

import (
	"testing"
)

var dashboard = Template{
	Workload: Workload{Resources: Resources{CPUMillis: 1000, RAMMB: 4096, DiskGB: 10}},
	Services: []Service{
		{Name: "postgres", Resources: Resources{CPUMillis: 1000, RAMMB: 4096, DiskGB: 10}},
		{Name: "redis", Resources: Resources{CPUMillis: 1000, RAMMB: 1024, DiskGB: 2}},
	},
}

var dockerPrices = Prices{
	PerCPUCore: 4048,
	PerRAMGB:   4445,
	PerDiskGB:  110,
}

func TestTotalResources(t *testing.T) {
	tests := []struct {
		name string
		tpl  Template
		want Resources
	}{
		{
			name: "empty",
			tpl:  Template{},
			want: Resources{},
		},
		{
			name: "workload only",
			tpl:  Template{Workload: Workload{Resources: Resources{CPUMillis: 2000, RAMMB: 8192, DiskGB: 20}}},
			want: Resources{CPUMillis: 2000, RAMMB: 8192, DiskGB: 20},
		},
		{
			name: "workload and services",
			tpl:  dashboard,
			want: Resources{CPUMillis: 3000, RAMMB: 9216, DiskGB: 22},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tpl.TotalResources(); got != tt.want {
				t.Errorf("TotalResources() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTotalResourcesDoesNotMutateWorkload(t *testing.T) {
	tpl := dashboard
	before := tpl.Workload.Resources
	tpl.TotalResources()
	if tpl.Workload.Resources != before {
		t.Errorf("workload resources changed to %+v, want %+v", tpl.Workload.Resources, before)
	}
}

func TestHourlyCost(t *testing.T) {
	tests := []struct {
		name   string
		tpl    Template
		prices Prices
		want   int64
	}{
		{
			name:   "empty template",
			tpl:    Template{},
			prices: dockerPrices,
			want:   0,
		},
		{
			name:   "zero prices",
			tpl:    dashboard,
			prices: Prices{},
			want:   0,
		},
		{
			name:   "workload only",
			tpl:    Template{Workload: Workload{Resources: Resources{CPUMillis: 1000, RAMMB: 4096, DiskGB: 10}}},
			prices: dockerPrices,
			want:   22928,
		},
		{
			name:   "workload and services",
			tpl:    dashboard,
			prices: dockerPrices,
			want:   54569,
		},
		{
			name:   "half a core and 512 MiB",
			tpl:    Template{Workload: Workload{Resources: Resources{CPUMillis: 500, RAMMB: 512, DiskGB: 1}}},
			prices: Prices{PerCPUCore: 1000, PerRAMGB: 1000, PerDiskGB: 10},
			want:   1010,
		},
		{
			name:   "partial units round up",
			tpl:    Template{Workload: Workload{Resources: Resources{CPUMillis: 1, RAMMB: 1}}},
			prices: Prices{PerCPUCore: 1000, PerRAMGB: 1000},
			want:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tpl.HourlyCost(tt.prices); got != tt.want {
				t.Errorf("HourlyCost() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResourcesString(t *testing.T) {
	got := Resources{CPUMillis: 1500, RAMMB: 512, DiskGB: 10}.String()
	if want := "1.5 cpu, 512 MiB ram, 10 GB disk"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
